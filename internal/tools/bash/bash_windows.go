//go:build windows

package bash

import "os/exec"

// detachStdin is a no-op on Windows. The controlling-terminal problem
// described in bash_unix.go is Unix-specific; Windows console handles
// and process groups have different semantics.
func detachStdin(cmd *exec.Cmd) {}

// killProcessGroup is a no-op on Windows. Process groups work differently
// (job objects), and the shell itself handles orphan cleanup. The bash.go
// Unix path's orphan-grandchild deadlock doesn't apply here.
func killProcessGroup(cmd *exec.Cmd) {}
