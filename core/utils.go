package core

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

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

// waitDelay bounds how long a cancelled tool can keep the run waiting on it.
//
// Killing the process group is not atomic against the tool forking: a child it
// spawns just as the signal is delivered can outlive the group, and while that
// child holds the output pipe there is no EOF, so waiting on the killed command
// waits out the survivor instead. Without a delay that parked a superseded lint
// for however long its linter's child would have run. Past this delay the pipes
// are closed and the wait ends regardless. It is generous because it also applies
// to a tool that exited normally, where it only has to cover draining what is
// already buffered.
const waitDelay = time.Second

// buildExecCmd prepares a tool invocation. A nil stdin means the tool is not fed
// the document, which is what a linter that reads the file itself wants.
func buildExecCmd(ctx context.Context, command, dir string, env []string, stdin io.Reader) *exec.Cmd {
	cmd := exec.CommandContext(ctx, shell, shellFlag, command)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = stdin
	cmd.WaitDelay = waitDelay
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
