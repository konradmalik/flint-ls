package lsp

import (
	"context"

	"github.com/sourcegraph/jsonrpc2"

	"github.com/konradmalik/flint-ls/types"
)

func (h *LspHandler) HandleInitialize(_ context.Context, _ *jsonrpc2.Conn, req *jsonrpc2.Request) (types.InitializeResult, error) {
	params, err := decodeParams[types.InitializeParams](req)
	if err != nil {
		return types.InitializeResult{}, err
	}

	h.mu.Lock()
	h.progressSupported = params.Capabilities.Window.WorkDoneProgress
	h.mu.Unlock()

	return h.langHandler.Initialize(params)
}
