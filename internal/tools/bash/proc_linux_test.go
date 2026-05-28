//go:build linux

package bash

import (
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDescendantPIDs_FindsNestedChildren(t *testing.T) {
	// Keep sh alive while both background sleeps run; without wait sh exits
	// immediately and reparents the sleeps, so /proc/<sh>/children is empty.
	cmd := exec.Command("/bin/sh", "-c", "sleep 600 & sleep 600 & wait")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		pid := cmd.Process.Pid
		for _, dpid := range descendantPIDs(pid) {
			_ = syscall.Kill(dpid, syscall.SIGKILL)
		}
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	// sh forks background jobs sequentially; wait until both sleep exist.
	deadline := time.Now().Add(time.Second)
	var pids []int
	for time.Now().Before(deadline) {
		pids = descendantPIDs(cmd.Process.Pid)
		if len(pids) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(pids) < 2 {
		t.Fatalf("expected at least 2 descendants, got %v", pids)
	}
}

// TestBash_Timeout_KillsEscapedProcessGroup verifies that timeout kills
// descendants that create their own session/PGID (setsid, sudo). Without
// recursive /proc cleanup, only the shell wrapper dies and cmd.Wait()
// blocks until the grandchild exits — see issue #9.
//
// Linux-only: setsid(1) is util-linux; descendantPIDs walks /proc.
func TestBash_Timeout_KillsEscapedProcessGroup(t *testing.T) {
	start := time.Now()
	out, err := run(t, yolo(t), Args{Command: "setsid sleep 30", TimeoutMS: 300})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed > 3*time.Second {
		t.Errorf("setsid sleep should be killed by timeout quickly, took %v", elapsed)
	}
	if !strings.Contains(out, "TIMED OUT") {
		t.Errorf("expected timeout marker: %s", out)
	}
}
