package lsp

import (
	"context"

	"github.com/sourcegraph/jsonrpc2"

	"github.com/konradmalik/flint-ls/types"
)

func (h *LspHandler) HandleTextDocumentDidOpen(_ context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
	params, err := decodeParams[types.DidOpenTextDocumentParams](req)
	if err != nil {
		return nil, err
	}

	doc := params.TextDocument
	if err := h.langHandler.OpenFile(doc.URI, doc.LanguageID, doc.Version, doc.Text); err != nil {
		return nil, err
	}

	h.ScheduleLinting(NewNotifier(conn), doc.URI, types.EventTypeOpen)

	return nil, nil
}
