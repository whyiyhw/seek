//go:build linux

package taskproc

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// ProbePID answers the liveness question on Linux by reading
// /proc/<pid>/stat. /proc is authoritative and cheap, and it is the only
// portable way to distinguish a zombie (state Z) from a live process
// without the pid potentially being recycled between two syscalls.
func ProbePID(pid int) Probe {
	if pid <= 0 {
		return ProbeGone
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ProbeGone
	}
	// Field 3 (1-based) is the state; the comm field (2) may contain
	// spaces and parens, so parse from the LAST ')' rather than
	// splitting the whole line.
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return ProbeAlive // unparsable but present: treat as alive
	}
	switch s[i+2] {
	case 'Z', 'X', 'x':
		return ProbeZombie
	}
	return ProbeAlive
}

// SignalGroup delivers sig to the whole process group led by pid, with a
// direct-pid fallback. The wrapper is a setsid leader, so -pid is the
// group; the fallback covers a wrapper that raced past setsid.
func SignalGroup(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return syscall.ESRCH
	}
	if err := syscall.Kill(-pid, sig); err == nil {
		return nil
	}
	return syscall.Kill(pid, sig)
}
