package lsp

import (
	"context"

	"github.com/sourcegraph/jsonrpc2"
)

// HandleShutdown stops all outstanding work and replies. The connection stays
// open on purpose: the client is expected to follow up with exit, and closing
// here would race that notification against our own response.
func (h *LspHandler) HandleShutdown(_ context.Context, _ *jsonrpc2.Conn, _ *jsonrpc2.Request) (any, error) {
	h.mu.Lock()
	h.shutdownRequested = true
	h.mu.Unlock()

	h.Close()

	return nil, nil
}
