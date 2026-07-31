package core

import (
	"context"

	"github.com/konradmalik/flint-ls/types"
)

// Reporter receives the incremental results of a lint or format run.
//
// Linting and formatting shell out to external tools and can produce results
// over time, so results are pushed to the client as they arrive instead of
// being collected and returned at the end. Implementations must be safe for
// concurrent use: a single run may report from several goroutines at once.
type Reporter interface {
	// PublishDiagnostics sends a complete set of diagnostics for one document.
	PublishDiagnostics(ctx context.Context, params types.PublishDiagnosticsParams)
	// Progress reports work-done progress for a long running operation.
	Progress(ctx context.Context, params types.ProgressParams)
	// ReportError surfaces a non-fatal error to the user. Errors are already
	// written to the local log by the caller, so implementations should only
	// forward them to the client.
	ReportError(ctx context.Context, err error)
}
