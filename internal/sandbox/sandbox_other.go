//go:build !darwin

package sandbox

import (
	"context"
	"os/exec"
)

// Available reports false on non-macOS platforms. Linux landlock
// (filesystem-only) is a TODO (柱 O M-O.2); Windows has no equivalent.
// Callers requiring confinement must check this and degrade (autopilot
// falls back to worktree logical isolation).
func Available() bool { return false }

// Wrap returns the command UN-sandboxed on unsupported platforms. Gate on
// Available() before relying on confinement.
func Wrap(ctx context.Context, opt Options, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

// Argv returns name+args unchanged (no sandbox on this platform).
func Argv(opt Options, name string, args ...string) (string, []string) {
	return name, args
}
