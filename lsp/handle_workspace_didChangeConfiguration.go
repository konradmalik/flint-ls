package lsp

import (
	"context"

	"github.com/sourcegraph/jsonrpc2"

	"github.com/konradmalik/flint-ls/types"
)

func (h *LspHandler) HandleWorkspaceDidChangeConfiguration(_ context.Context, _ *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
	params, err := decodeParams[types.DidChangeConfigurationParams](req)
	if err != nil {
		return nil, err
	}

	h.UpdateConfiguration(&params.Settings)

	return nil, nil
}
