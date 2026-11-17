//go:build !darwin && !linux

package sandbox

// Available reports false on platforms with no supported jail (Windows,
// *BSD, …). macOS uses seatbelt (sandbox_darwin.go); Linux uses Landlock
// (sandbox_linux.go). Callers requiring confinement must check this and
// degrade (autopilot falls back to worktree logical isolation).
func Available() bool { return false }

// Argv returns name+args unchanged (no sandbox on this platform).
func Argv(opt Options, name string, args ...string) (string, []string) {
	return name, args
}
