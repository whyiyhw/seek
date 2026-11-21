//go:build !darwin && !linux && !windows

package taskproc

import (
	"os"
	"syscall"
)

// ProbePID on the remaining platforms uses the portable signal-0
// question. It cannot distinguish a zombie (Windows has no Unix zombie
// state, and the BSDs' os.FindProcess semantics differ), so it reports
// alive-or-gone only — documented in docs/prd/feature-task-lifetime.md §5.
func ProbePID(pid int) Probe {
	if pid <= 0 {
		return ProbeGone
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return ProbeGone
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		return ProbeGone
	}
	return ProbeAlive
}

// SignalGroup falls back to signalling the pid directly: no portable
// process-group kill exists off Unix. Descendants inherit the console
// detachment already (create-new-process-group on spawn).
func SignalGroup(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return syscall.ESRCH
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(sig)
}
