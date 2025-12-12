package lsp

import (
	"context"

	"github.com/sourcegraph/jsonrpc2"

	"github.com/konradmalik/flint-ls/types"
)

type LspNotifier struct {
	conn *jsonrpc2.Conn
}

func NewNotifier(conn *jsonrpc2.Conn) *LspNotifier {
	return &LspNotifier{conn}
}

func (n *LspNotifier) LogMessage(ctx context.Context, typ types.MessageType, message string) {
	_ = n.conn.Notify(
		ctx,
		"window/logMessage",
		&types.LogMessageParams{
			Type:    typ,
			Message: message,
		})
}

func (n *LspNotifier) PublishDiagnostics(ctx context.Context, params types.PublishDiagnosticsParams) {
	_ = n.conn.Notify(
		ctx,
		"textDocument/publishDiagnostics",
		&params)
}

func (n *LspNotifier) Progress(ctx context.Context, params types.ProgressParams) {
	_ = n.conn.Notify(
		ctx,
		"$/progress",
		&params)
}
