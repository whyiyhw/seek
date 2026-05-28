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
// Setsid (create new session) detaches from the controlling terminal;
// the child gets ENXIO on /dev/tty. Stdin is already /dev/null by
// default (Go's exec.Cmd leaves it nil), so normal stdin reads also
// fail with EOF.
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

// killProcessGroup kills the entire process group of cmd.
// Negative PID = process group on Unix (PGID == PID of session leader).
// Without this, grandchildren orphaned by Setsid keep pipe fds open
// and cmd.Wait() deadlocks.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
