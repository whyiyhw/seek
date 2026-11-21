//go:build windows

package taskproc

import (
	"os/exec"
	"strconv"
	"syscall"
)

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259
)

// ProbePID answers the liveness question on Windows with
// OpenProcess + GetExitCodeProcess. syscall.Signal(0) — the portable
// trick used elsewhere — is NOT valid here: os.Process.Signal on Windows
// rejects everything except Kill/Interrupt, so a signal-based probe would
// report every running task as gone. A pid we cannot open is gone (or
// owned by someone else, which for our own tasks means gone).
//
// Windows has no Unix zombie state, so this reports alive-or-gone only —
// documented in docs/prd/feature-task-lifetime.md §5.
func ProbePID(pid int) Probe {
	if pid <= 0 {
		return ProbeGone
	}
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return ProbeGone
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return ProbeAlive // opened but unreadable: treat as alive
	}
	if code == stillActive {
		return ProbeAlive
	}
	return ProbeGone
}

// SignalGroup terminates the task and its child tree with taskkill /T /F
// — the same call internal/tools/bash uses, because Windows has no
// process-group signals. SIGTERM and SIGKILL both map to a forced tree
// kill: there is no graceful half, and pretending otherwise would make
// `seek kill --grace` lie about what it can do.
func SignalGroup(pid int, _ syscall.Signal) error {
	if pid <= 0 {
		return syscall.ESRCH
	}
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return kill.Run()
}
