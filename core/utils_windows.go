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
	return `"` + escapeInDoubleQuotes(s) + `"`
}

// escapeInSingleQuotes renders s for a spot the command already has inside single
// quotes. cmd has no single quotes, so they are not quotes at all here -- they
// reach the tool as part of the argument, exactly as they did before, and there is
// nothing to escape for.
func escapeInSingleQuotes(s string) string {
	return s
}

// escapeInDoubleQuotes renders s for a spot the command already has inside double
// quotes: a double quote in s would end the string early and cannot appear in a
// Windows path anyway, so it goes. %VAR% expansion happens in there too and has no
// escape on a cmd command line, so a path containing one is beyond help here.
func escapeInDoubleQuotes(s string) string {
	return strings.ReplaceAll(s, `"`, "")
}

// makeCmdKillable configures a command so that the command and all of its children will be killed when
// it's cancelled.
func makeCmdKillable(cmd *exec.Cmd) {
	// no-op on windows, not sure how to implement that
}
