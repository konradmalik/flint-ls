package lsp

import (
	"context"
	"errors"
	"fmt"
	"sync"
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
	// formats holds the newest formatting request per document, identified by
	// pointer. A run whose request is no longer the one in the table has been
	// superseded.
	formats map[types.DocumentURI]*formatRequest
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

// lintJob tracks the pending and in-flight lint runs of a single document.
// All fields are guarded by LspHandler.mu.
type lintJob struct {
	// timer fires the pending run; nil when nothing is pending.
	timer *time.Timer
	// cancel aborts the run currently executing; nil when none is.
	cancel context.CancelFunc
	// gen identifies the most recently scheduled run. A run that starts or
	// finishes under a stale gen has been superseded and bails out.
	gen uint64
}

// supersede invalidates the pending and in-flight runs of a document. The
// caller must hold LspHandler.mu.
func (job *lintJob) supersede() {
	job.gen++
	if job.timer != nil {
		job.timer.Stop()
		job.timer = nil
	}
	if job.cancel != nil {
		job.cancel()
		job.cancel = nil
	}
}

// formatRequest represents one in-flight formatting request. It is compared by
// pointer, which is what tells a finished run whether a newer request has
// arrived for its document. It carries uri so that it is not zero-sized:
// pointers to distinct zero-size variables may compare equal, which would make
// every request look like every other.
type formatRequest struct {
	uri types.DocumentURI
}

func NewHandler(langHandler *core.LangHandler) *LspHandler {
	return &LspHandler{
		langHandler:  langHandler,
		lintDebounce: defaultLintDebounce,
		lints:        make(map[types.DocumentURI]*lintJob),
		formats:      make(map[types.DocumentURI]*formatRequest),
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
// the newest request for a document answers with edits. Every edit set is a diff
// against the text its own run started from, so a client that applied two of
// them would apply the second against text the first has already replaced.
// Saying so with an error keeps the client honest -- answering with an empty edit
// list would instead claim the document needs no changes.
func (h *LspHandler) Formatting(ctx context.Context, reporter core.Reporter, uri types.DocumentURI, rng *types.Range, opt types.FormattingOptions) ([]types.TextEdit, error) {
	req := h.claimFormatting(uri)
	defer h.releaseFormatting(req)

	edits, err := h.langHandler.RunAllFormatters(ctx, reporter, uri, rng, opt)
	if errors.Is(err, core.ErrDocumentChanged) {
		// the same answer as a superseded request: the client should disregard
		// this response, not treat it as a formatter failure
		logs.Log.Logf(logs.Debug, "format for %v raced an edit", uri)
		return nil, &jsonrpc2.Error{Code: codeContentModified, Message: err.Error()}
	}
	if err != nil {
		return nil, err
	}

	// checked after the run rather than before it, because a request that has
	// already started is precisely the one whose edits would otherwise reach the
	// client after a newer request superseded them
	if !h.formatCurrent(req) {
		logs.Log.Logf(logs.Debug, "format for %v superseded", uri)
		return nil, &jsonrpc2.Error{Code: codeContentModified, Message: "superseded by a newer formatting request"}
	}

	return edits, nil
}

// claimFormatting records this request as the newest one for uri.
func (h *LspHandler) claimFormatting(uri types.DocumentURI) *formatRequest {
	h.mu.Lock()
	defer h.mu.Unlock()

	req := &formatRequest{uri: uri}
	h.formats[uri] = req

	return req
}

// formatCurrent reports whether req is still the newest request for its
// document. A request whose entry is gone has been abandoned -- the document was
// closed, the server is shutting down, or a newer request has already finished
// and cleared it -- and one that finds a different pointer has been superseded.
// Either way the edits it computed describe text nobody is waiting for.
func (h *LspHandler) formatCurrent(req *formatRequest) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	return !h.closed && h.formats[req.uri] == req
}

// releaseFormatting forgets a document once its newest request is done, so the
// map holds only documents being formatted right now. Superseded requests leave
// the newer one's entry alone.
func (h *LspHandler) releaseFormatting(req *formatRequest) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.formats[req.uri] == req {
		delete(h.formats, req.uri)
	}
}

// ScheduleLinting queues a lint run for uri. Runs are debounced per document:
// the run happens once uri has been quiet for the debounce interval, and any
// run already in flight for uri is cancelled because its results are stale.
func (h *LspHandler) ScheduleLinting(reporter core.Reporter, uri types.DocumentURI, eventType types.EventType) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}

	job, ok := h.lints[uri]
	if !ok {
		job = &lintJob{}
		h.lints[uri] = job
	}
	job.supersede()

	gen := job.gen
	logs.Log.Logf(logs.Debug, "lint for %v scheduled in %v", uri, h.lintDebounce)
	job.timer = time.AfterFunc(h.lintDebounce, func() {
		h.runLinting(reporter, uri, job, gen, eventType)
	})
}

// ForgetDocument drops everything scheduled for uri. Used when a document is
// closed: its diagnostics are no longer wanted, and formatting requests still
// queued for it can no longer produce anything useful.
func (h *LspHandler) ForgetDocument(uri types.DocumentURI) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if job, ok := h.lints[uri]; ok {
		job.supersede()
		delete(h.lints, uri)
	}
	// a run still formatting this document finds its entry gone and gives up
	delete(h.formats, uri)
}

func (h *LspHandler) runLinting(reporter core.Reporter, uri types.DocumentURI, job *lintJob, gen uint64, eventType types.EventType) {
	ctx, ok := h.startLinting(uri, job, gen)
	if !ok {
		return
	}
	defer h.finishLinting(uri, job, gen)

	if err := h.langHandler.RunAllLinters(ctx, reporter, uri, eventType); err != nil {
		logs.Log.Logln(logs.Error, err.Error())
		reporter.ReportError(ctx, err)
	}
}

// lintCurrent reports whether job is still the run the handler expects for uri.
// The generation alone is not enough: closing a document drops its job, and the
// one installed when it is reopened starts counting again from the same numbers,
// so a run left over from before the close would otherwise match it. Callers must
// hold LspHandler.mu.
func (h *LspHandler) lintCurrent(uri types.DocumentURI, job *lintJob, gen uint64) bool {
	return h.lints[uri] == job && job.gen == gen
}

// startLinting claims the run identified by gen and returns its context. It
// reports false when a newer run has already superseded this one, which happens
// when the timer fires just as another notification arrives.
func (h *LspHandler) startLinting(uri types.DocumentURI, job *lintJob, gen uint64) (context.Context, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed || !h.lintCurrent(uri, job, gen) {
		return nil, false
	}

	// linting outlives the notification that triggered it, so it gets a context
	// of its own instead of borrowing that notification's
	ctx, cancel := context.WithCancel(context.Background())
	job.timer = nil
	job.cancel = cancel

	return ctx, true
}

// finishLinting releases the run's context and forgets the document once
// nothing is scheduled for it, so the map does not grow with every file opened.
func (h *LspHandler) finishLinting(uri types.DocumentURI, job *lintJob, gen uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.lintCurrent(uri, job, gen) {
		// superseded: whoever took over already cancelled our context
		return
	}

	if job.cancel != nil {
		job.cancel()
		job.cancel = nil
	}
	if job.timer == nil {
		delete(h.lints, uri)
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
	for uri, job := range h.lints {
		job.supersede()
		delete(h.lints, uri)
	}
	// runs still formatting find themselves no longer current and discard their
	// edits; they drop their own entries as they unwind
}
