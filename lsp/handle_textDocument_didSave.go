package lsp

import (
	"context"

	"github.com/sourcegraph/jsonrpc2"

	"github.com/konradmalik/flint-ls/types"
)

func (h *LspHandler) HandleTextDocumentDidSave(_ context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
	params, err := decodeParams[types.DidSaveTextDocumentParams](req)
	if err != nil {
		return nil, err
	}

	// the client only sends the text when it registered for it; without it our
	// copy is already current thanks to didChange
	if params.Text != nil {
		if err := h.langHandler.UpdateFile(params.TextDocument.URI, *params.Text, nil); err != nil {
			return nil, err
		}
	}

	h.ScheduleLinting(h.notifier(conn), params.TextDocument.URI, types.EventTypeSave)

	return nil, nil
}
