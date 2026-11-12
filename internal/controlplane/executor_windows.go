//go:build windows

package controlplane

import (
	"os/exec"
	"strconv"
	"syscall"
)

// prepareChild is a no-op on Windows: console handles and job semantics
// differ from Unix process groups, and killTree (taskkill /T) walks the
// parent→child relationship from the PID instead.
func prepareChild(cmd *exec.Cmd) {}

// killTree terminates the child and its entire tree via taskkill /T
// (tree) /F (force) — the Windows equivalent of kill(-pgid), same
// approach as internal/tools/bash's killProcessGroup. HideWindow keeps
// taskkill from flashing a console.
func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = kill.Run()
}
