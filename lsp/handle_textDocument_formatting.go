package lsp

import (
	"context"
	"encoding/json"

	"github.com/konradmalik/flint-ls/types"
	"github.com/sourcegraph/jsonrpc2"
)

func (h *LspHandler) HandleTextDocumentFormatting(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (result any, err error) {
	if req.Params == nil {
		return nil, &jsonrpc2.Error{Code: jsonrpc2.CodeInvalidParams}
	}

	var params types.DocumentFormattingParams
	if err := json.Unmarshal(*req.Params, &params); err != nil {
		return nil, err
	}

	notifier := NewNotifier(conn)
	return h.Formatting(ctx, *notifier, params.TextDocument.URI, nil, params.Options)
}

func (h *LspHandler) HandleTextDocumentRangeFormatting(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (result any, err error) {
	if req.Params == nil {
		return nil, &jsonrpc2.Error{Code: jsonrpc2.CodeInvalidParams}
	}

	var params types.DocumentRangeFormattingParams
	if err := json.Unmarshal(*req.Params, &params); err != nil {
		return nil, err
	}

	notifier := NewNotifier(conn)
	return h.Formatting(ctx, *notifier, params.TextDocument.URI, &params.Range, params.Options)
}
