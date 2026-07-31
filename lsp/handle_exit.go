package lsp

import (
	"context"

	"github.com/sourcegraph/jsonrpc2"
)

// HandleExit ends the session. Closing the connection is what actually stops
// the server: it releases main, which then exits with ExitCode.
func (h *LspHandler) HandleExit(_ context.Context, conn *jsonrpc2.Conn, _ *jsonrpc2.Request) (any, error) {
	h.mu.Lock()
	h.exitRequested = true
	h.mu.Unlock()

	h.Close()

	return nil, conn.Close()
}
