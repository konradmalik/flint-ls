package lsp

import (
	"encoding/json"

	"github.com/sourcegraph/jsonrpc2"
)

// decodeParams unmarshals the params of req into T, rejecting messages that
// carry none. Every handler needs this, so it lives here instead of being
// repeated per method.
func decodeParams[T any](req *jsonrpc2.Request) (T, error) {
	var params T

	if req.Params == nil {
		return params, &jsonrpc2.Error{Code: jsonrpc2.CodeInvalidParams}
	}
	if err := json.Unmarshal(*req.Params, &params); err != nil {
		return params, err
	}

	return params, nil
}
