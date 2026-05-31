//go:build !linux

package sandbox

// RunTrampolineIfRequested is a no-op on platforms without a re-exec
// trampoline. macOS confines via the sandbox-exec wrapper binary (no
// self-re-exec needed); only Linux Landlock requires applying the jail to
// the process itself before exec (see sandbox_linux.go). main() calls
// this unconditionally on every platform.
func RunTrampolineIfRequested() {}
