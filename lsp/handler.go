package lsp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sourcegraph/jsonrpc2"

	"github.com/konradmalik/flint-ls/core"
	"github.com/konradmalik/flint-ls/logs"
	"github.com/konradmalik/flint-ls/types"
)

// defaultLintDebounce coalesces the burst of didChange notifications an editor
// sends while typing into a single lint run. Small enough to feel immediate,
// large enough to avoid spawning a linter per keystroke.
const defaultLintDebounce = 100 * time.Millisecond

// codeContentModified tells the client its request was answered by state that
// has since moved on, so the response should be disregarded rather than treated
// as a failure. It is the standard code for a stale result.
//
// https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/#responseMessage
const codeContentModified = -32801

type LspHandler struct {
	langHandler *core.LangHandler

	// mu guards everything below it. It is never held across a lint or format
	// run, only around the bookkeeping for one.
	mu           sync.Mutex
	lintDebounce time.Duration
	lints        map[types.DocumentURI]*lintJob
	// progressSupported is what the client said about work-done progress in
	// initialize. Until then nothing is reported, which is the safe assumption.
	progressSupported bool
	shutdownRequested bool
	exitRequested     bool
	closed            bool
}

// notifier builds the channel back to the client for one piece of work.
func (h *LspHandler) notifier(conn *jsonrpc2.Conn) *LspNotifier {
	h.mu.Lock()
	defer h.mu.Unlock()

	return NewNotifier(conn, h.progressSupported)
}

// lintJob is one scheduled lint run of a single document: a debounce interval
// followed by the run itself. Cancelling it aborts whichever of the two it is
// currently in.
//
// Jobs are compared by pointer, which is what tells a run whether it is still
// the one the handler expects for its document. A generation counter would not
// do: closing a document drops its job, and the one installed when it is
// reopened would start counting from the same numbers, so a run left over from
// before the close would look current again.
type lintJob struct {
	uri    types.DocumentURI
	cancel context.CancelFunc
	// events this run has to lint for, which is its own plus those of any run it
	// took the place of before that run could report
	events types.EventType
	// running says the debounce is over and the linters are going, which is what
	// makes this run's events its own business rather than something a replacement
	// has to pick up
	running atomic.Bool
}

func NewHandler(langHandler *core.LangHandler) *LspHandler {
	return &LspHandler{
		langHandler:  langHandler,
		lintDebounce: defaultLintDebounce,
		lints:        make(map[types.DocumentURI]*lintJob),
	}
}

func (h *LspHandler) UpdateConfiguration(config *types.Config) {
	h.mu.Lock()
	if config.LintDebounce > 0 {
		h.lintDebounce = config.LintDebounce
	}
	h.mu.Unlock()

	h.langHandler.UpdateConfiguration(config)
}

func (h *LspHandler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
	// exit is the one message that must still be served after shutdown
	if req.Method == "exit" {
		return h.HandleExit(ctx, conn, req)
	}

	if h.isShuttingDown() {
		return nil, &jsonrpc2.Error{Code: jsonrpc2.CodeInvalidRequest, Message: "server is shutting down"}
	}

	switch req.Method {
	case "initialize":
		return h.HandleInitialize(ctx, conn, req)
	case "initialized":
		return nil, nil
	case "shutdown":
		return h.HandleShutdown(ctx, conn, req)
	case "textDocument/didOpen":
		return h.HandleTextDocumentDidOpen(ctx, conn, req)
	case "textDocument/didChange":
		return h.HandleTextDocumentDidChange(ctx, conn, req)
	case "textDocument/didSave":
		return h.HandleTextDocumentDidSave(ctx, conn, req)
	case "textDocument/didClose":
		return h.HandleTextDocumentDidClose(ctx, conn, req)
	case "textDocument/formatting":
		return h.HandleTextDocumentFormatting(ctx, conn, req)
	case "textDocument/rangeFormatting":
		return h.HandleTextDocumentRangeFormatting(ctx, conn, req)
	case "workspace/didChangeConfiguration":
		return h.HandleWorkspaceDidChangeConfiguration(ctx, conn, req)
	}

	return nil, &jsonrpc2.Error{Code: jsonrpc2.CodeMethodNotFound, Message: fmt.Sprintf("method not supported: %s", req.Method)}
}

// Formatting runs every configured formatter for uri and returns the edits that
// bring the document to its formatted state.
//
// Requests run concurrently, including several for the same document, but only
// the newest request for a document on its current text answers with edits; see
// core.ErrSuperseded. Saying so with an error keeps the client honest --
// answering with an empty edit list would instead claim the document needs no
// changes.
func (h *LspHandler) Formatting(ctx context.Context, reporter core.Reporter, uri types.DocumentURI, rng *types.Range, opt types.FormattingOptions) ([]types.TextEdit, error) {
	edits, err := h.langHandler.RunAllFormatters(ctx, reporter, uri, rng, opt)
	if errors.Is(err, core.ErrSuperseded) {
		// the client should disregard this response, not treat it as a
		// formatter failure
		logs.Log.Logf(logs.Debug, "format for %v: %v", uri, err)
		return nil, &jsonrpc2.Error{Code: codeContentModified, Message: err.Error()}
	}

	return edits, err
}

// ScheduleLinting queues a lint run for uri. Runs are debounced per document:
// the run happens once uri has been quiet for the debounce interval, and any
// run already in flight for uri is cancelled because its results are stale.
//
// The replacement lints for the superseded run's events as well, as long as that
// run had not started. Otherwise a config that only lints on one kind of event
// would be lost whenever a different kind arrived inside the debounce window --
// a keystroke landing 50ms after a save would drop a save-only linter, and the
// document would show nothing from it until the next save.
func (h *LspHandler) ScheduleLinting(reporter core.Reporter, uri types.DocumentURI, eventType types.EventType) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}

	// whatever was pending or running for this document is superseded: its
	// results describe text the client has already replaced
	events := eventType
	if prev, ok := h.lints[uri]; ok {
		// a run that never got to lint hands its events over; one that is already
		// linting had its chance to report, and inheriting from it would run its
		// linters again on every keystroke that follows
		if !prev.running.Load() {
			events |= prev.events
		}
		prev.cancel()
	}

	// linting outlives the notification that triggered it, so it gets a context
	// of its own instead of borrowing that notification's
	ctx, cancel := context.WithCancel(context.Background())
	job := &lintJob{uri: uri, cancel: cancel, events: events}
	h.lints[uri] = job
	debounce := h.lintDebounce
	h.mu.Unlock()

	logs.Log.Logf(logs.Debug, "lint for %v scheduled in %v", uri, debounce)

	go func() {
		defer h.finishLinting(job)

		// the debounce. a newer notification cancels this job before the interval
		// is up, which is what coalesces a burst of edits into one run
		select {
		case <-ctx.Done():
			logs.Log.Logf(logs.Debug, "lint for %v superseded before it ran", uri)
			return
		case <-time.After(debounce):
		}

		job.running.Store(true)

		if err := h.langHandler.RunAllLinters(ctx, reporter, uri, job.events); err != nil {
			logs.Log.Logln(logs.Error, err.Error())
			reporter.ReportError(ctx, err)
		}
	}()
}

// ForgetDocument drops everything scheduled for uri. Used when a document is
// closed: its diagnostics are no longer wanted. A formatting run still going for
// it finds the document gone and gives up on its own.
func (h *LspHandler) ForgetDocument(uri types.DocumentURI) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if job, ok := h.lints[uri]; ok {
		job.cancel()
		delete(h.lints, uri)
	}
}

// finishLinting releases the run's context and forgets the document unless a
// newer job has taken its place, so the map does not grow with every file opened.
func (h *LspHandler) finishLinting(job *lintJob) {
	h.mu.Lock()
	defer h.mu.Unlock()

	job.cancel()
	if h.lints[job.uri] == job {
		delete(h.lints, job.uri)
	}
}

func (h *LspHandler) isShuttingDown() bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.shutdownRequested
}

// ExitCode reports the process exit status the client asked for: the spec wants
// 1 when exit arrives without a preceding shutdown, 0 otherwise. A connection
// that simply goes away is not an error.
func (h *LspHandler) ExitCode() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.exitRequested && !h.shutdownRequested {
		return 1
	}
	return 0
}

// Close abandons all scheduled work and refuses to schedule more. It does not
// wait for in-flight linters: their contexts are cancelled, which kills the
// external processes, and their results are discarded.
func (h *LspHandler) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.closed = true
	for _, job := range h.lints {
		job.cancel()
	}
	clear(h.lints)
}
