// git command runner — same indirection pattern as
// internal/checkpoint/git.go (gitRunner type + runGitReal
// production impl). Duplicated rather than shared because:
//
//   - The runner is 10 lines; extracting to internal/git would
//     create a one-function package with no other purpose, and
//     coupling worktree + checkpoint via a shared package gains
//     nothing on the maintenance side.
//   - Each consumer keeps the freedom to specialise (timeouts,
//     env, GIT_CONFIG_NOSYSTEM, etc.) without affecting the other.

package worktree

import (
	"bytes"
	"context"
	"os/exec"
)

// GitRunner is the indirection that lets tests substitute a fake
// `git` binary. Production uses runGitReal. Signature mirrors the
// checkpoint package: (cwd, args...) → (stdout, stderr, err) so
// tests can assert on the exact invocation sequence.
//
// EXPORTED so cross-package tests (e.g. internal/tools/
// enterworktree) can construct a Manager with a scripted runner
// via NewManagerWithRunner. Production callers use NewManager
// which wires runGitReal.
type GitRunner func(ctx context.Context, cwd string, args ...string) (stdout, stderr string, err error)

// runGitReal invokes the host's `git` binary with `cwd` as the
// working directory. Output captured into byte buffers — git
// errors (non-zero exit) are returned with stdout/stderr intact
// so callers can surface the precise message to the LLM / user.
func runGitReal(ctx context.Context, cwd string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}
