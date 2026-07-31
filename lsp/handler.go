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
	// formats holds a job per document with formatting outstanding. Documents
	// are independent, so they format in parallel; only same-document runs wait
	// for each other.
	formats           map[types.DocumentURI]*formatJob
	shutdownRequested bool
	exitRequested     bool
	closed            bool
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

// formatJob serializes the formatting of a single document.
type formatJob struct {
	// mu is held for the duration of a run. Two runs on the same document must
	// not overlap: both would compute edits against the same original text, and
	// formatters that rewrite the file in place would fight over it.
	mu sync.Mutex
	// requests counts the requests seen for this document, so a run waiting on
	// mu can tell that a newer one has arrived. Guarded by LspHandler.mu.
	requests uint64
	// outstanding is how many requests still reference this job; the job is
	// forgotten when the last one leaves. Guarded by LspHandler.mu.
	outstanding int
	// abandoned marks a document that was closed while requests were queued.
	// Guarded by LspHandler.mu.
	abandoned bool
}

func NewHandler(langHandler *core.LangHandler) *LspHandler {
	return &LspHandler{
		langHandler:  langHandler,
		lintDebounce: defaultLintDebounce,
		lints:        make(map[types.DocumentURI]*lintJob),
		formats:      make(map[types.DocumentURI]*formatJob),
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
// Documents format in parallel; requests for the same document wait for each
// other. A request that finds a newer one queued for its document gives up
// instead of shelling out: the edits it would compute are stale by construction,
// and a client that spams formatting should cost one run, not one per request.
// Saying so with an error keeps the client honest -- answering with an empty
// edit list would instead claim the document needs no changes.
func (h *LspHandler) Formatting(ctx context.Context, reporter core.Reporter, uri types.DocumentURI, rng *types.Range, opt types.FormattingOptions) ([]types.TextEdit, error) {
	job, request := h.claimFormatting(uri)
	defer h.releaseFormatting(uri, job)

	job.mu.Lock()
	defer job.mu.Unlock()

	if h.formattingSuperseded(job, request) {
		logs.Log.Logf(logs.Debug, "format for %v superseded", uri)
		return nil, &jsonrpc2.Error{Code: codeContentModified, Message: "superseded by a newer formatting request"}
	}

	edits, err := h.langHandler.RunAllFormatters(ctx, reporter, uri, rng, opt)
	if errors.Is(err, core.ErrDocumentChanged) {
		// the same answer as a superseded request: the client should disregard
		// this response, not treat it as a formatter failure
		logs.Log.Logf(logs.Debug, "format for %v raced an edit", uri)
		return nil, &jsonrpc2.Error{Code: codeContentModified, Message: err.Error()}
	}

	return edits, err
}

// claimFormatting registers a request against uri's job, creating the job if
// this is the only request for that document, and returns the request's number.
func (h *LspHandler) claimFormatting(uri types.DocumentURI) (*formatJob, uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	job, ok := h.formats[uri]
	if !ok {
		job = &formatJob{}
		h.formats[uri] = job
	}
	job.requests++
	job.outstanding++

	return job, job.requests
}

// formattingSuperseded reports whether the run for request should be skipped
// because the server moved on while it waited for its turn.
func (h *LspHandler) formattingSuperseded(job *formatJob, request uint64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.closed || job.abandoned || job.requests != request
}

// releaseFormatting forgets a document once its last outstanding request is
// done, so the map holds only documents being formatted right now.
func (h *LspHandler) releaseFormatting(uri types.DocumentURI, job *formatJob) {
	h.mu.Lock()
	defer h.mu.Unlock()

	job.outstanding--
	// the entry may already have been replaced by a job for a document that was
	// closed and reopened, which must outlive this one
	if job.outstanding == 0 && h.formats[uri] == job {
		delete(h.formats, uri)
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
	if job, ok := h.formats[uri]; ok {
		// requests still queued keep their reference to the job and clean it up
		// themselves; the flag is what tells them to give up
		job.abandoned = true
		delete(h.formats, uri)
	}
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

// current reports whether job is still the run the handler expects for uri. The
// generation alone is not enough: closing a document drops its job, and the one
// installed when it is reopened starts counting again from the same numbers, so
// a run left over from before the close would otherwise match it. Callers must
// hold LspHandler.mu.
func (h *LspHandler) current(uri types.DocumentURI, job *lintJob, gen uint64) bool {
	return h.lints[uri] == job && job.gen == gen
}

// startLinting claims the run identified by gen and returns its context. It
// reports false when a newer run has already superseded this one, which happens
// when the timer fires just as another notification arrives.
func (h *LspHandler) startLinting(uri types.DocumentURI, job *lintJob, gen uint64) (context.Context, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed || !h.current(uri, job, gen) {
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

	if !h.current(uri, job, gen) {
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
	// formatting requests still queued check closed and give up rather than
	// shell out on the way down; they drop their own jobs as they unwind
}
