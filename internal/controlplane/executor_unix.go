//go:build !windows

package controlplane

import (
	"os/exec"
	"syscall"
)

// prepareChild puts the seek child into its own process group so a
// timeout/cancel can take down the whole tree with one kill(-pgid).
// exec.CommandContext would only signal the direct child; seek's own
// children (bash tool runs) inherit our stdout pipe fds, so an
// orphaned grandchild would keep the pipe copy open and deadlock
// cmd.Wait — the exact failure internal/tools/bash documents (issue
// #9 lineage). Setpgid rather than Setsid: no controlling-terminal
// problem exists here (serve has no TUI), and staying in the session
// keeps semantics simple.
func prepareChild(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killTree SIGKILLs the child's whole process group, then the child
// PID itself as a fallback. Unlike internal/tools/bash we skip the
// /proc descendant walk: grandchildren that seek's bash tool Setsid'd
// are seek's own teardown problem, and the kill-grace expiry (not a
// hung Wait) is the plane's accepted bound for any pipe-holder that
// survives them.
func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
