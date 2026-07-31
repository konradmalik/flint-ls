package lsp

import (
	"context"

	"github.com/sourcegraph/jsonrpc2"

	"github.com/konradmalik/flint-ls/types"
)

func (h *LspHandler) HandleTextDocumentFormatting(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
	params, err := decodeParams[types.DocumentFormattingParams](req)
	if err != nil {
		return nil, err
	}

	return h.Formatting(ctx, h.notifier(conn), params.TextDocument.URI, nil, params.Options)
}

func (h *LspHandler) HandleTextDocumentRangeFormatting(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
	params, err := decodeParams[types.DocumentRangeFormattingParams](req)
	if err != nil {
		return nil, err
	}

	return h.Formatting(ctx, h.notifier(conn), params.TextDocument.URI, &params.Range, params.Options)
}
