//go:build windows

package bash

import (
	"os/exec"
	"strconv"
	"syscall"
)

// detachStdin is a no-op on Windows. The controlling-terminal problem
// described in bash_unix.go is Unix-specific; Windows console handles
// and process groups have different semantics.
func detachStdin(cmd *exec.Cmd) {}

// killProcessGroup terminates the process and its entire child tree.
// Windows has no Unix-style process groups / signals, so we shell out to
// taskkill /T (tree) /F (force) /PID. This walks the parent→child
// relationship from cmd's PID, so cmd.exe and everything it spawned go
// down together — the equivalent of the Unix /proc-walk + SIGKILL.
//
// Why taskkill and not a Job Object: a Job Object is more elegant
// (KILL_ON_JOB_CLOSE handles teardown atomically) but must be created and
// assigned at launch time and threaded through to the kill site, which
// this `killProcessGroup(cmd)` signature can't carry. taskkill needs only
// the PID, which is stable here — the bg wait goroutine hasn't reaped the
// process yet when Kill fires (PRD feature-bash-monitor §8). HideWindow
// keeps taskkill from flashing a console in the user's TUI.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = kill.Run()
}
