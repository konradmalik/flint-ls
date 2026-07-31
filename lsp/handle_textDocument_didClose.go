package lsp

import (
	"context"

	"github.com/sourcegraph/jsonrpc2"

	"github.com/konradmalik/flint-ls/types"
)

func (h *LspHandler) HandleTextDocumentDidClose(_ context.Context, _ *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
	params, err := decodeParams[types.DidCloseTextDocumentParams](req)
	if err != nil {
		return nil, err
	}

	// drop scheduled work first: acting on a document that is no longer open
	// would fail to find it anyway
	h.ForgetDocument(params.TextDocument.URI)

	if err := h.langHandler.CloseFile(params.TextDocument.URI); err != nil {
		return nil, err
	}

	return nil, nil
}
