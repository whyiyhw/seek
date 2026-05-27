//go:build windows

package bash

import "os/exec"

// detachStdin is a no-op on Windows. The controlling-terminal problem
// described in bash_unix.go is Unix-specific; Windows console handles
// and process groups have different semantics.
func detachStdin(cmd *exec.Cmd) {}
