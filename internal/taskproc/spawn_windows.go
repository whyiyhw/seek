//go:build windows

package taskproc

import (
	"os/exec"
	"syscall"
)

// detach launches the child in its own console-less process group so it
// outlives the spawning seek process. Windows has no setsid; the
// combination below is the platform's equivalent (the child gets no
// console to die with, and Ctrl-C in the parent's console doesn't reach
// it). Zombie-state modelling does not apply here — see
// docs/prd/archive/feature-task-lifetime.md §5.
func detach(cmd *exec.Cmd) {
	const (
		createNewProcessGroup = 0x00000200
		detachedProcess       = 0x00000008
	)
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags = createNewProcessGroup | detachedProcess
	cmd.SysProcAttr.HideWindow = true
}

// shellArgv is the command interpreter for one task command: cmd.exe /C,
// the same shape the bash tool uses on Windows.
func shellArgv(command string) []string { return []string{"cmd.exe", "/C", command} }
