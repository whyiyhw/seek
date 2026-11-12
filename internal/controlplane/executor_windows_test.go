//go:build windows

package controlplane

import (
	"testing"
	"time"
)

// waitPidGone on Windows: the signal-0 liveness probe has no stdlib
// equivalent here; the kill itself is exercised via taskkill /T in
// killTree, so we skip only the post-kill liveness assertion.
func waitPidGone(t *testing.T, pid int, within time.Duration) {
	t.Skipf("pid liveness probe (signal 0) is unix-only; pid %d", pid)
}
