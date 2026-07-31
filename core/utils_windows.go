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

// shellQuote renders s as a single argument for cmd. Single quotes mean nothing
// to cmd, so double quotes it is; a double quote cannot appear in a Windows path
// in the first place, and stripping one is better than emitting a command the
// shell would parse as something else entirely.
func shellQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, "") + `"`
}

// makeCmdKillable configures a command so that the command and all of its children will be killed when
// it's cancelled.
func makeCmdKillable(cmd *exec.Cmd) {
	// no-op on windows, not sure how to implement that
}
