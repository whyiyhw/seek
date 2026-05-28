//go:build !windows

package bash

import (
	"os/exec"
	"syscall"
)

// detachStdin ensures the child process can't read from the controlling
// terminal (/dev/tty) directly. Without this, commands that open /dev/tty
// (sudo, ssh, git credential helpers, docker login, npm init, etc.) steal
// keystrokes from seek's TUI — the user hits Esc but the child process
// consumes it, so the TUI never sees the cancel and appears frozen.
//
// Setsid (create new session) detaches from the controlling terminal,
// so the child gets ENXIO on /dev/tty. Stdin is inherited from the
// parent (seek's stdin). Inherited stdin is harmless because seek
// itself never feeds input to child processes.
//
// NOTE: Setsid creates a new session where the child can fork
// grandchildren that inherit the stdout/stderr pipe fds. The parent
// (bash.go) must kill the process group (negative PID) on context
// cancellation, NOT just the direct child PID — otherwise grandchildren
// keep the pipes open and cmd.Wait() deadlocks.
func detachStdin(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}

// killProcessGroup kills the entire process group of cmd and any
// descendants that escaped to a new PGID/session (sudo, setsid, etc.).
// Order: PGID first (atomic group takedown), then /proc walk for
// escapees, then direct PID as fallback — see issue #9 (WSL sudo).
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	for _, dpid := range descendantPIDs(pid) {
		_ = syscall.Kill(dpid, syscall.SIGKILL)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
