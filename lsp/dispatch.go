package lsp

import (
	"context"

	"github.com/sourcegraph/jsonrpc2"
)

// blockingRequests are the requests whose handler waits on an external tool
// before it can answer, so running one on the read loop stalls the whole
// connection until that tool exits.
//
// Only formatting qualifies. Linting shells out to external tools too, but it is
// triggered by notifications whose handlers merely arm a timer and return, so
// that work already happens off the read loop -- see ScheduleLinting.
var blockingRequests = map[string]bool{
	"textDocument/formatting":      true,
	"textDocument/rangeFormatting": true,
}

// OffloadSlowRequests runs the requests that wait on external tools in their own
// goroutine and keeps everything else on the connection's read loop.
//
// Everything else has to stay inline, because dispatching to goroutines loses
// the order the client sent messages in, and that order is load-bearing:
// document sync (didOpen/didChange/didClose) only reconstructs the right text
// when applied in sequence, initialize has to be seen before the documents it
// applies to, and shutdown has to be seen before exit. So an inline handler must
// never block -- work that takes real time belongs on a goroutine of its own,
// not on a dispatcher that would reorder the message that started it.
func OffloadSlowRequests(handler jsonrpc2.Handler) jsonrpc2.Handler {
	return offloadingHandler{handler}
}

type offloadingHandler struct {
	jsonrpc2.Handler
}

func (h offloadingHandler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	if !req.Notif && blockingRequests[req.Method] {
		go h.Handler.Handle(ctx, conn, req)
		return
	}

	h.Handler.Handle(ctx, conn, req)
}
