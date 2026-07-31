package core

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/konradmalik/flint-ls/types"
)

const (
	inputPlaceholder    = "${INPUT}"
	fileextPlaceholder  = "${FILEEXT}"
	filenamePlaceholder = "${FILENAME}"
	rootPlaceholder     = "${ROOT}"
	carriageReturn      = "\r"
)

func normalizedFilenameFromUri(uri types.DocumentURI) (string, error) {
	fname, err := PathFromURI(uri)
	if err != nil {
		return "", fmt.Errorf("invalid uri: %v: %v", err, uri)
	}
	fname = filepath.ToSlash(fname)
	return fname, nil
}

// buildExecCmd prepares a tool invocation. A nil stdin means the tool is not fed
// the document, which is what a linter that reads the file itself wants.
func buildExecCmd(ctx context.Context, command, dir string, env []string, stdin io.Reader) *exec.Cmd {
	cmd := exec.CommandContext(ctx, shell, shellFlag, command)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = stdin
	makeCmdKillable(cmd)

	return cmd
}

func intPtrIfNotZero(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

func boolOrDefault(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}
