//go:build windows

package core

import (
	"os/exec"
	"strings"
)

const (
	shell     = "cmd"
	shellFlag = "/c"
)

func comparePaths(path1, path2 string) bool {
	return strings.EqualFold(path1, path2)
}

// makeCmdKillable configures a command so that the command and all of its children will be killed when
// it's cancelled.
func makeCmdKillable(cmd *exec.Cmd) {
	// no-op on windows, not sure how to implement that
}
