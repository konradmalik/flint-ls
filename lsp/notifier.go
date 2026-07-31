package lsp

import (
	"context"

	"github.com/sourcegraph/jsonrpc2"

	"github.com/konradmalik/flint-ls/logs"
	"github.com/konradmalik/flint-ls/types"
)

// LspNotifier sends server-initiated messages to the client. It implements
// core.Reporter.
type LspNotifier struct {
	conn *jsonrpc2.Conn
}

func NewNotifier(conn *jsonrpc2.Conn) *LspNotifier {
	return &LspNotifier{conn}
}

func (n *LspNotifier) LogMessage(ctx context.Context, typ types.MessageType, message string) {
	n.notify(ctx, "window/logMessage", &types.LogMessageParams{
		Type:    typ,
		Message: message,
	})
}

func (n *LspNotifier) PublishDiagnostics(ctx context.Context, params types.PublishDiagnosticsParams) {
	n.notify(ctx, "textDocument/publishDiagnostics", &params)
}

func (n *LspNotifier) Progress(ctx context.Context, params types.ProgressParams) {
	n.notify(ctx, "$/progress", &params)
}

func (n *LspNotifier) ReportError(ctx context.Context, err error) {
	n.LogMessage(ctx, types.MessError, err.Error())
}

// notify is best-effort: a cancelled run or a closed connection makes the send
// fail, and there is nothing to be done about it but leave a trace in the log.
func (n *LspNotifier) notify(ctx context.Context, method string, params any) {
	if err := n.conn.Notify(ctx, method, params); err != nil {
		logs.Log.Logf(logs.Debug, "dropped %s notification: %v", method, err)
	}
}
