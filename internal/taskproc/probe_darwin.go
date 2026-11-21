//go:build darwin

package taskproc

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// Darwin process states (sys/proc.h). SIDL (being created) and SZOMB
// (exiting, waiting for its parent) are both "not running work any more";
// only SZOMB is a zombie in the sense seek reports.
const (
	stateSZomb = 5
	stateSIdl  = 1
)

// ProbePID answers the liveness question on macOS via sysctl
// kern.proc.pid. On darwin there is no /proc; kill(pid, 0) can tell
// alive-vs-gone but NOT zombie (a zombie still exists and accepts no
// signals), which is exactly the state `seek ps` has to report honestly
// — hence the sysctl read.
func ProbePID(pid int) Probe {
	if pid <= 0 {
		return ProbeGone
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return ProbeGone
	}
	switch kp.Proc.P_stat {
	case stateSZomb:
		return ProbeZombie
	case stateSIdl:
		return ProbeZombie
	}
	return ProbeAlive
}

// SignalGroup delivers sig to the wrapper's process group, falling back
// to the bare pid. The wrapper is a setsid leader, so -pid is the group.
func SignalGroup(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return syscall.ESRCH
	}
	if err := syscall.Kill(-pid, sig); err == nil {
		return nil
	}
	return syscall.Kill(pid, sig)
}
