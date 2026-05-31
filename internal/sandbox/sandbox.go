// Package sandbox provides an OS-level filesystem/network jail for
// seek's dangerous subprocesses (v7 柱 O). It is the kernel-level
// complement to the permission gates (logical) and worktree isolation:
// permission decides "should I", the sandbox enforces "can I".
//
// Platform support is deliberately uneven and honest about it:
//   - macOS: seatbelt via sandbox-exec — confines BOTH file writes and
//     network. (Implemented; verified on darwin.)
//   - Linux: landlock (filesystem only — landlock can't restrict
//     network). (TODO — sandbox_other currently degrades to no-op.)
//   - Windows / others: unsupported → no-op (Available() == false).
//
// Use Available() to decide whether to rely on it; Wrap() on an
// unsupported platform returns the UN-sandboxed command (the caller must
// not assume confinement without checking Available()).
package sandbox

// Options bound a sandboxed command. WritableDirs are the only absolute
// directories writes are permitted to (plus the system temp dir); every
// other write is denied. AllowNetwork gates outbound network.
//
// Available() and Wrap() are defined per-platform (sandbox_darwin.go /
// sandbox_other.go).
type Options struct {
	WritableDirs []string
	AllowNetwork bool
}
