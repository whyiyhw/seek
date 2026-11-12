//go:build !windows

package controlplane

import (
	"syscall"
	"testing"
	"time"
)

// pidAlive probes a pid with signal 0. True means a process exists
// (may be a recycled pid — polls here run within milliseconds of the
// kill, so recycling is not a concern).
func pidAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// waitPidGone polls until the pid stops responding to signal 0.
func waitPidGone(t *testing.T, pid int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive after %v — process leak", pid, within)
}
