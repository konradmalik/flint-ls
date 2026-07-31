package lsp

import (
	"context"

	"github.com/sourcegraph/jsonrpc2"

	"github.com/konradmalik/flint-ls/types"
)

func (h *LspHandler) HandleTextDocumentDidChange(_ context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
	params, err := decodeParams[types.DidChangeTextDocumentParams](req)
	if err != nil {
		return nil, err
	}

	// the server announces full sync, so every change carries the whole
	// document and only the last one matters
	if n := len(params.ContentChanges); n > 0 {
		text := params.ContentChanges[n-1].Text
		if err := h.langHandler.UpdateFile(params.TextDocument.URI, text, &params.TextDocument.Version); err != nil {
			return nil, err
		}
	}

	h.ScheduleLinting(NewNotifier(conn), params.TextDocument.URI, types.EventTypeChange)

	return nil, nil
}
