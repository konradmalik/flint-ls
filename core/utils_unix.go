//go:build !windows

package core

import (
	"os/exec"
	"strings"
	"syscall"
)

const (
	shell     = "sh"
	shellFlag = "-c"
)

func comparePaths(path1, path2 string) bool {
	return path1 == path2
}

// shellQuote renders s as a single argument for the shell above. Single quotes
// suppress every kind of expansion, so the only thing left to handle is a quote
// in the string itself: close, emit an escaped one, reopen.
func shellQuote(s string) string {
	return "'" + escapeInSingleQuotes(s) + "'"
}

// escapeInSingleQuotes renders s for a spot the command already has inside single
// quotes. Nothing expands in there, so a single quote in s is the only character
// that matters: it would end the string early, so it is closed, escaped and
// reopened -- the same trick shellQuote plays, without the outer quotes.
func escapeInSingleQuotes(s string) string {
	return strings.ReplaceAll(s, "'", `'\''`)
}

// insideDoubleQuotes escapes what the shell still acts on inside a double quoted
// string. One pass, so an escaping backslash is never escaped again.
var insideDoubleQuotes = strings.NewReplacer(`\`, `\\`, `$`, `\$`, "`", "\\`", `"`, `\"`)

// escapeInDoubleQuotes renders s for a spot the command already has inside double
// quotes, where parameters and command substitutions are still expanded and
// backslashes still escape. Without this a document named $(...) would have its
// name run rather than passed along.
func escapeInDoubleQuotes(s string) string {
	return insideDoubleQuotes.Replace(s)
}

// makeCmdKillable configures a command so that the command and all of its children will be killed when
// it's cancelled.
// See: https://medium.com/@felixge/killing-a-child-process-and-all-of-its-children-in-go-54079af94773
func makeCmdKillable(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		pgid := cmd.Process.Pid
		// negative pid means a group
		return syscall.Kill(-pgid, syscall.SIGKILL)
	}
}
