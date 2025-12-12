package lsp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sourcegraph/jsonrpc2"

	"github.com/konradmalik/flint-ls/core"
	"github.com/konradmalik/flint-ls/logs"
	"github.com/konradmalik/flint-ls/types"
)

type LspHandler struct {
	langHandler    *core.LangHandler
	formatMu       sync.Mutex
	lintMu         sync.Mutex
	lintTimer      *time.Timer
	lintDebounce   time.Duration
	formatTimer    *time.Timer
	formatDebounce time.Duration
}

func NewHandler(langHandler *core.LangHandler) *LspHandler {
	return &LspHandler{langHandler: langHandler}
}

func (h *LspHandler) UpdateConfiguration(config *types.Config) {
	if config.LintDebounce > 0 {
		h.lintDebounce = config.LintDebounce
	}
	if config.FormatDebounce > 0 {
		h.formatDebounce = config.FormatDebounce
	}

	h.langHandler.UpdateConfiguration(config)
}

func (h *LspHandler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (result any, err error) {
	switch req.Method {
	case "initialize":
		return h.HandleInitialize(ctx, conn, req)
	case "initialized":
		return
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

func (h *LspHandler) Formatting(ctx context.Context, notifier LspNotifier, uri types.DocumentURI, rng *types.Range, opt types.FormattingOptions) ([]types.TextEdit, error) {
	if h.formatTimer != nil {
		logs.Log.Logf(logs.Debug, "format debounced: %v", h.formatDebounce)
		return []types.TextEdit{}, nil
	}

	h.formatMu.Lock()
	h.formatTimer = time.AfterFunc(h.formatDebounce, func() {
		h.formatMu.Lock()
		h.formatTimer = nil
		h.formatMu.Unlock()
	})
	h.formatMu.Unlock()

	progress := make(chan types.ProgressParams)
	defer close(progress)

	go func() {
		for p := range progress {
			notifier.Progress(ctx, p)
		}
	}()

	return h.langHandler.RunAllFormatters(ctx, uri, rng, opt, progress)
}

var running = make(map[types.DocumentURI]context.CancelFunc)

func (h *LspHandler) ScheduleLinting(notifier LspNotifier, uri types.DocumentURI, eventType types.EventType) {
	if h.lintTimer != nil {
		h.lintTimer.Reset(h.lintDebounce)
		logs.Log.Logf(logs.Debug, "lint debounced: %v", h.formatDebounce)
		return
	}
	h.lintMu.Lock()
	h.lintTimer = time.AfterFunc(h.lintDebounce, func() {
		h.lintTimer = nil

		h.lintMu.Lock()
		cancel, ok := running[uri]
		if ok {
			cancel()
		}

		ctx, cancel := context.WithCancel(context.Background())
		running[uri] = cancel
		h.lintMu.Unlock()

		func() {
			diagnostics := make(chan types.PublishDiagnosticsParams)
			errors := make(chan error)
			progress := make(chan types.ProgressParams)
			defer close(diagnostics)
			defer close(errors)
			defer close(progress)

			go func() {
				for d := range diagnostics {
					notifier.PublishDiagnostics(ctx, d)
				}
			}()

			go func() {
				for e := range errors {
					logs.Log.Logln(logs.Error, e.Error())
					notifier.LogMessage(ctx, types.MessError, e.Error())
				}
			}()

			go func() {
				for p := range progress {
					notifier.Progress(ctx, p)
				}
			}()

			err := h.langHandler.RunAllLinters(ctx, uri, eventType, diagnostics, errors, progress)
			if err != nil {
				logs.Log.Logln(logs.Error, err.Error())
				notifier.LogMessage(ctx, types.MessError, err.Error())
			}
		}()
	})
	h.lintMu.Unlock()
}

func (h *LspHandler) Close() {
	if h.formatTimer != nil {
		h.formatTimer.Stop()
	}
	if h.lintTimer != nil {
		h.lintTimer.Stop()
	}
}
