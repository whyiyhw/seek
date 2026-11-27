//go:build !windows

package taskproc

import (
	"os/exec"
	"syscall"
)

// detach puts the child in its own session + process group with no
// controlling terminal, so it survives its parent's exit and the
// terminal's SIGHUP. This is the whole mechanism behind "task lifetime
// outlives the seek process" — see docs/prd/archive/feature-task-lifetime.md §1.1.
func detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}

// shellArgv is the command interpreter for one task command: /bin/sh -c,
// the same shape the bash tool uses (docs/prd/archive/feature-task-lifetime.md §1.1).
func shellArgv(command string) []string { return []string{"/bin/sh", "-c", command} }
