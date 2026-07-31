package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/sourcegraph/jsonrpc2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/konradmalik/flint-ls/core"
	"github.com/konradmalik/flint-ls/types"
)

const testLanguageID = "test"

// neverFires is long enough that a scheduled run cannot go off while a test
// inspects the handler's state, which keeps those tests free of sleeps.
const neverFires = time.Hour

func TestScheduleLintingKeepsOnePendingRunPerDocument(t *testing.T) {
	h := newTestHandler(t, neverFires)
	reporter := &fakeReporter{}
	a := newTestDocument(t, h, "a.txt")
	b := newTestDocument(t, h, "b.txt")

	h.ScheduleLinting(reporter, a, types.EventTypeOpen)
	h.ScheduleLinting(reporter, b, types.EventTypeChange)

	// documents must be debounced independently: a single shared timer would
	// let an edit in b discard the run queued for a
	assert.Len(t, h.pendingLints(), 2)
}

func TestScheduleLintingSupersedesEarlierRunOfSameDocument(t *testing.T) {
	h := newTestHandler(t, neverFires)
	reporter := &fakeReporter{}
	uri := newTestDocument(t, h, "a.txt")

	for range 5 {
		h.ScheduleLinting(reporter, uri, types.EventTypeChange)
	}

	pending := h.pendingLints()
	require.Len(t, pending, 1)
	assert.EqualValues(t, 5, pending[uri].gen, "each schedule should invalidate the previous one")
}

func TestScheduleLintingLintsEveryDocument(t *testing.T) {
	h := newTestHandler(t, time.Millisecond)
	reporter := &fakeReporter{}
	a := newTestDocument(t, h, "a.txt")
	b := newTestDocument(t, h, "b.txt")

	h.ScheduleLinting(reporter, a, types.EventTypeOpen)
	h.ScheduleLinting(reporter, b, types.EventTypeChange)

	for _, uri := range []types.DocumentURI{a, b} {
		assert.Eventually(t, func() bool {
			return len(reporter.diagnosticsFor(uri)) == 1
		}, time.Second, time.Millisecond, "no diagnostics published for %v", uri)
	}
	assert.Empty(t, reporter.reportedErrors())
}

func TestLintJobIsForgottenOnceItFinishes(t *testing.T) {
	h := newTestHandler(t, time.Millisecond)
	reporter := &fakeReporter{}
	uri := newTestDocument(t, h, "a.txt")

	h.ScheduleLinting(reporter, uri, types.EventTypeOpen)

	// a document that is done linting must not be remembered, otherwise the
	// handler grows by one live context per file touched in a session
	assert.Eventually(t, func() bool {
		return len(h.pendingLints()) == 0
	}, time.Second, time.Millisecond)
}

func TestLintRunOfAReopenedDocumentSurvivesTheOldOne(t *testing.T) {
	h := newTestHandler(t, neverFires)
	reporter := &fakeReporter{}
	uri := newTestDocument(t, h, "a.txt")

	// a run is scheduled and claims its turn
	h.ScheduleLinting(reporter, uri, types.EventTypeOpen)
	stale, staleGen := h.currentLintJob(t, uri)
	_, ok := h.startLinting(uri, stale, staleGen)
	require.True(t, ok)

	// the document is closed and reopened, which installs a fresh job whose
	// generation numbers start over from the same place
	h.ForgetDocument(uri)
	h.ScheduleLinting(reporter, uri, types.EventTypeOpen)
	fresh, freshGen := h.currentLintJob(t, uri)
	require.NotSame(t, stale, fresh)
	require.Equal(t, staleGen, freshGen, "the collision this test is about")

	freshCtx, ok := h.startLinting(uri, fresh, freshGen)
	require.True(t, ok)

	// the leftover run finishing must not disturb the new document's run
	h.finishLinting(uri, stale, staleGen)

	assert.NoError(t, freshCtx.Err(), "the reopened document's run was cancelled")
	assert.Len(t, h.pendingLints(), 1, "the reopened document's job was dropped")
}

func TestForgetDocumentDropsScheduledRun(t *testing.T) {
	h := newTestHandler(t, 10*time.Millisecond)
	reporter := &fakeReporter{}
	uri := newTestDocument(t, h, "a.txt")

	h.ScheduleLinting(reporter, uri, types.EventTypeOpen)
	h.ForgetDocument(uri)

	assert.Empty(t, h.pendingLints())
	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, reporter.diagnosticsFor(uri), "a cancelled run must not publish")
}

func TestCloseDropsScheduledRunsAndRefusesNewOnes(t *testing.T) {
	h := newTestHandler(t, 10*time.Millisecond)
	reporter := &fakeReporter{}
	uri := newTestDocument(t, h, "a.txt")

	h.ScheduleLinting(reporter, uri, types.EventTypeOpen)
	h.Close()
	h.ScheduleLinting(reporter, uri, types.EventTypeChange)

	assert.Empty(t, h.pendingLints())
	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, reporter.diagnosticsFor(uri))
}

func TestFormattingDropsSupersededRequests(t *testing.T) {
	const requests = 20

	h := newTestHandler(t, neverFires)
	uri := newTestDocument(t, h, "a.txt")

	// stand in for a run already under way, so every request below has to queue
	// and can be superseded by the ones that arrive after it. holding the lock
	// rather than leaning on a slow formatter is what makes the count exact.
	inProgress, _ := h.claimFormatting(uri)
	inProgress.mu.Lock()

	type outcome struct {
		edits []types.TextEdit
		err   error
	}
	results := make([]outcome, requests)

	var wg sync.WaitGroup
	for i := range requests {
		wg.Go(func() {
			edits, err := h.Formatting(t.Context(), &fakeReporter{}, uri, nil, types.FormattingOptions{})
			results[i] = outcome{edits, err}
		})
	}

	// let every request register before any of them is allowed to run
	require.Eventually(t, func() bool {
		return h.formatRequestsSeen(uri) == requests+1
	}, 10*time.Second, time.Millisecond, "not all requests registered")

	inProgress.mu.Unlock()
	h.releaseFormatting(uri, inProgress)
	wg.Wait()

	var formatted, superseded int
	for _, got := range results {
		if got.err == nil {
			formatted++
			assert.NotEmpty(t, got.edits, "a run that happened should return the edits it computed")
			continue
		}

		var rpcErr *jsonrpc2.Error
		require.ErrorAs(t, got.err, &rpcErr)
		require.EqualValues(t, codeContentModified, rpcErr.Code,
			"a superseded request must say so, not claim the document needs no edits")
		superseded++
	}

	// a client that spams formatting costs one run, not one per request
	assert.Equal(t, 1, formatted, "only the newest request should have been served")
	assert.Equal(t, requests, formatted+superseded, "every request must get an answer")
	assert.Empty(t, h.pendingFormats(), "the jobs must not outlive the burst")
}

func TestFormattingRunsDocumentsInParallel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the barrier below is written as a POSIX shell command")
	}

	const documents = 4

	// this formatter only succeeds once every document has reached it, so the
	// test cannot pass unless the runs genuinely overlap. it gives up after
	// ~2s so that serialized formatting fails the test instead of hanging it.
	barrier := filepath.Join(t.TempDir(), "arrived")
	format := fmt.Sprintf(
		`printf x >> %[1]s; n=200; `+
			`while [ "$(wc -c < %[1]s)" -lt %[2]d ] && [ "$n" -gt 0 ]; do sleep 0.01; n=$((n-1)); done; `+
			`[ "$(wc -c < %[1]s)" -ge %[2]d ] || exit 1; tr a-z A-Z`,
		barrier, documents)

	h := newTestHandlerWithLanguage(t, neverFires, types.Language{FormatCommand: format})

	uris := make([]types.DocumentURI, documents)
	for i := range uris {
		uris[i] = newTestDocument(t, h, fmt.Sprintf("doc%d.txt", i))
	}

	errs := make([]error, documents)
	var wg sync.WaitGroup
	for i, uri := range uris {
		wg.Go(func() {
			_, errs[i] = h.Formatting(t.Context(), &fakeReporter{}, uri, nil, types.FormattingOptions{})
		})
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "document %d did not format alongside the others", i)
	}
}

// TestFormattingRejectsStaleEdits models a client that formats asynchronously
// and so lets the document change under a running formatter. A synchronous
// client cannot reach this, which is why the edit has to be injected by hand.
func TestFormattingRejectsStaleEdits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the barrier below is written as a POSIX shell command")
	}

	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	release := filepath.Join(dir, "release")

	// announces that it is running, then waits to be let go, so the edit below
	// lands while the formatter is definitely mid-run
	format := fmt.Sprintf(
		`printf x > %[1]s; n=500; `+
			`while [ ! -f %[2]s ] && [ "$n" -gt 0 ]; do sleep 0.01; n=$((n-1)); done; cat`,
		started, release)

	h := newTestHandlerWithLanguage(t, neverFires, types.Language{FormatCommand: format})
	uri := newTestDocument(t, h, "a.txt")

	type outcome struct {
		edits []types.TextEdit
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		edits, err := h.Formatting(t.Context(), &fakeReporter{}, uri, nil, types.FormattingOptions{})
		done <- outcome{edits, err}
	}()

	require.Eventually(t, func() bool {
		_, err := os.Stat(started)
		return err == nil
	}, 10*time.Second, time.Millisecond, "the formatter never started")

	// what an async client allows: the buffer changes mid-format
	version := 2
	require.NoError(t, h.langHandler.UpdateFile(uri, "typed while formatting\n", &version))
	require.NoError(t, os.WriteFile(release, nil, 0o600))

	got := <-done

	assert.Nil(t, got.edits, "stale edits must not reach the client")

	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, got.err, &rpcErr)
	assert.EqualValues(t, codeContentModified, rpcErr.Code,
		"an edit that raced the formatter should be reported as stale, not as a formatter failure")
}

func TestFormattingServesSequentialRequests(t *testing.T) {
	h := newTestHandler(t, neverFires)
	uri := newTestDocument(t, h, "a.txt")

	// nothing to supersede: each request must be served on its own
	for range 3 {
		edits, err := h.Formatting(t.Context(), &fakeReporter{}, uri, nil, types.FormattingOptions{})
		require.NoError(t, err)
		assert.NotEmpty(t, edits)
	}

	assert.Empty(t, h.pendingFormats())
}

func TestForgetDocumentDropsQueuedFormatting(t *testing.T) {
	h := newTestHandler(t, neverFires)
	uri := newTestDocument(t, h, "a.txt")

	job, request := h.claimFormatting(uri)
	defer h.releaseFormatting(uri, job)
	h.ForgetDocument(uri)

	assert.True(t, h.formattingSuperseded(job, request),
		"formatting a document that was closed cannot produce anything useful")
	assert.Empty(t, h.pendingFormats())
}

func TestHandleRejectsRequestsAfterShutdown(t *testing.T) {
	h := newTestHandler(t, neverFires)

	_, err := h.Handle(t.Context(), nil, &jsonrpc2.Request{Method: "shutdown"})
	require.NoError(t, err)

	_, err = h.Handle(t.Context(), nil, &jsonrpc2.Request{Method: "textDocument/formatting"})

	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	assert.EqualValues(t, jsonrpc2.CodeInvalidRequest, rpcErr.Code)
}

func TestHandleUnknownMethod(t *testing.T) {
	h := newTestHandler(t, neverFires)

	_, err := h.Handle(t.Context(), nil, &jsonrpc2.Request{Method: "textDocument/hover"})

	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	assert.EqualValues(t, jsonrpc2.CodeMethodNotFound, rpcErr.Code)
}

func TestExitCode(t *testing.T) {
	tests := []struct {
		name     string
		shutdown bool
		exit     bool
		want     int
	}{
		{name: "clean shutdown then exit", shutdown: true, exit: true, want: 0},
		{name: "exit without shutdown", exit: true, want: 1},
		{name: "connection dropped without exit", want: 0},
		{name: "shutdown but no exit", shutdown: true, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(t, neverFires)
			conn := newTestConn(t)

			if tt.shutdown {
				_, err := h.Handle(t.Context(), conn, &jsonrpc2.Request{Method: "shutdown"})
				require.NoError(t, err)
			}
			if tt.exit {
				_, err := h.Handle(t.Context(), conn, &jsonrpc2.Request{Method: "exit", Notif: true})
				require.NoError(t, err)
			}

			assert.Equal(t, tt.want, h.ExitCode())
		})
	}
}

func TestExitIsServedAfterShutdown(t *testing.T) {
	h := newTestHandler(t, neverFires)
	conn := newTestConn(t)

	_, err := h.Handle(t.Context(), conn, &jsonrpc2.Request{Method: "shutdown"})
	require.NoError(t, err)

	// exit is the one message that must not be rejected once shutting down
	_, err = h.Handle(t.Context(), conn, &jsonrpc2.Request{Method: "exit", Notif: true})
	assert.NoError(t, err)
}

func TestDecodeParams(t *testing.T) {
	t.Run("decodes params", func(t *testing.T) {
		raw := json.RawMessage(`{"textDocument":{"uri":"file:///a.txt"}}`)

		params, err := decodeParams[types.DidCloseTextDocumentParams](&jsonrpc2.Request{Params: &raw})

		require.NoError(t, err)
		assert.EqualValues(t, "file:///a.txt", params.TextDocument.URI)
	})

	t.Run("rejects missing params", func(t *testing.T) {
		_, err := decodeParams[types.DidCloseTextDocumentParams](&jsonrpc2.Request{})

		var rpcErr *jsonrpc2.Error
		require.ErrorAs(t, err, &rpcErr)
		assert.EqualValues(t, jsonrpc2.CodeInvalidParams, rpcErr.Code)
	})

	t.Run("rejects malformed params", func(t *testing.T) {
		raw := json.RawMessage(`{"textDocument":42}`)

		_, err := decodeParams[types.DidCloseTextDocumentParams](&jsonrpc2.Request{Params: &raw})

		assert.Error(t, err)
	})
}

func TestOffloadSlowRequests(t *testing.T) {
	tests := []struct {
		name        string
		req         jsonrpc2.Request
		wantInline  bool
		description string
	}{
		{
			name:        "formatting is offloaded",
			req:         jsonrpc2.Request{Method: "textDocument/formatting"},
			description: "formatting waits on an external tool and must not block the read loop",
		},
		{
			name:        "range formatting is offloaded",
			req:         jsonrpc2.Request{Method: "textDocument/rangeFormatting"},
			description: "formatting waits on an external tool and must not block the read loop",
		},
		{
			name:        "document sync stays inline",
			req:         jsonrpc2.Request{Method: "textDocument/didChange", Notif: true},
			wantInline:  true,
			description: "changes only reconstruct the text when applied in the order sent",
		},
		{
			name:        "shutdown stays inline",
			req:         jsonrpc2.Request{Method: "shutdown"},
			wantInline:  true,
			description: "a pipelined exit must not overtake the shutdown before it",
		},
		{
			name:        "initialize stays inline",
			req:         jsonrpc2.Request{Method: "initialize"},
			wantInline:  true,
			description: "documents opened right after must see the initialized state",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocking := blockingHandler{entered: make(chan struct{}), release: make(chan struct{})}
			handler := OffloadSlowRequests(blocking)
			defer close(blocking.release)
			entered, returned := blocking.entered, make(chan struct{})

			go func() {
				handler.Handle(t.Context(), nil, &tt.req)
				close(returned)
			}()
			<-entered

			// an inline handler cannot have returned while the work is blocked
			select {
			case <-returned:
				assert.False(t, tt.wantInline, tt.description)
			case <-time.After(50 * time.Millisecond):
				assert.True(t, tt.wantInline, tt.description)
			}
		})
	}
}

// blockingHandler stays inside Handle until released, which is what makes the
// difference between inline and offloaded dispatch observable.
type blockingHandler struct {
	entered chan struct{}
	release chan struct{}
}

func (h blockingHandler) Handle(context.Context, *jsonrpc2.Conn, *jsonrpc2.Request) {
	close(h.entered)
	<-h.release
}

// pendingLints copies the lint job table so tests can inspect it safely.
func (h *LspHandler) pendingLints() map[types.DocumentURI]lintJob {
	h.mu.Lock()
	defer h.mu.Unlock()

	jobs := make(map[types.DocumentURI]lintJob, len(h.lints))
	for uri, job := range h.lints {
		jobs[uri] = *job
	}
	return jobs
}

// currentLintJob returns the job the handler currently holds for uri.
func (h *LspHandler) currentLintJob(t *testing.T, uri types.DocumentURI) (*lintJob, uint64) {
	t.Helper()

	h.mu.Lock()
	defer h.mu.Unlock()

	job, ok := h.lints[uri]
	require.True(t, ok, "no lint job for %v", uri)

	return job, job.gen
}

// pendingFormats lists the documents with formatting outstanding.
func (h *LspHandler) pendingFormats() []types.DocumentURI {
	h.mu.Lock()
	defer h.mu.Unlock()

	return slices.Collect(maps.Keys(h.formats))
}

// formatRequestsSeen reports how many formatting requests uri's job has taken.
func (h *LspHandler) formatRequestsSeen(uri types.DocumentURI) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()

	if job, ok := h.formats[uri]; ok {
		return job.requests
	}
	return 0
}

func newTestHandler(t *testing.T, debounce time.Duration) *LspHandler {
	t.Helper()

	return newTestHandlerWithLanguage(t, debounce, types.Language{
		LintCommand: "echo 1:problem",
		// the linter reports no filename, so the entry is taken to be about the
		// document that was linted
		LintFormats:        []string{"%l:%m"},
		LintStdin:          true,
		LintIgnoreExitCode: true,
		// changes the text, so a request that was served is distinguishable from
		// one that produced nothing
		FormatCommand: appendingFormatCommand(),
	})
}

// appendingFormatCommand echoes the document back with a character appended.
func appendingFormatCommand() string {
	if runtime.GOOS == "windows" {
		return `set /p line= && call echo %line%X`
	}
	return `echo "$(cat -)X"`
}

func newTestHandlerWithLanguage(t *testing.T, debounce time.Duration, language types.Language) *LspHandler {
	t.Helper()

	langHandler := core.NewHandler(&types.Config{
		Languages: &map[string][]types.Language{testLanguageID: {language}},
	})

	h := NewHandler(langHandler)
	h.UpdateConfiguration(&types.Config{LintDebounce: debounce})
	t.Cleanup(h.Close)

	return h
}

func newTestDocument(t *testing.T, h *LspHandler, name string) types.DocumentURI {
	t.Helper()

	uri := core.ParseLocalFileToURI(filepath.Join(t.TempDir(), name))
	require.NoError(t, h.langHandler.OpenFile(uri, testLanguageID, 1, "some text\n"))

	return uri
}

// newTestConn returns a live connection whose peer never answers. It is enough
// for handlers that only need something to close.
func newTestConn(t *testing.T) *jsonrpc2.Conn {
	t.Helper()

	client, server := net.Pipe()
	noop := jsonrpc2.HandlerWithError(func(context.Context, *jsonrpc2.Conn, *jsonrpc2.Request) (any, error) {
		return nil, nil
	})
	conn := jsonrpc2.NewConn(context.Background(),
		jsonrpc2.NewBufferedStream(server, jsonrpc2.VSCodeObjectCodec{}), noop)

	t.Cleanup(func() {
		_ = conn.Close()
		_ = client.Close()
	})

	return conn
}

// fakeReporter records what lint runs report. Linters report from several
// goroutines at once, so it locks.
type fakeReporter struct {
	mu          sync.Mutex
	diagnostics map[types.DocumentURI][]types.Diagnostic
	errors      []error
}

func (r *fakeReporter) PublishDiagnostics(_ context.Context, params types.PublishDiagnosticsParams) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(params.Diagnostics) == 0 {
		// the run's initial reset, not a result
		return
	}
	if r.diagnostics == nil {
		r.diagnostics = make(map[types.DocumentURI][]types.Diagnostic)
	}
	r.diagnostics[params.URI] = append(r.diagnostics[params.URI], params.Diagnostics...)
}

func (r *fakeReporter) Progress(context.Context, types.ProgressParams) {}

func (r *fakeReporter) ReportError(_ context.Context, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.errors = append(r.errors, err)
}

func (r *fakeReporter) diagnosticsFor(uri types.DocumentURI) []types.Diagnostic {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.diagnostics[uri]
}

func (r *fakeReporter) reportedErrors() []error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.errors
}
