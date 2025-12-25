//go:build !windows

package core

import (
	"os/exec"
	"syscall"
)

const (
	shell     = "sh"
	shellFlag = "-c"
)

func comparePaths(path1, path2 string) bool {
	return path1 == path2
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
