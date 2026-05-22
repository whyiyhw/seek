//go:build !windows

package upgrade

import (
	"fmt"
	"os"
)

// replaceBinary moves newPath into targetPath atomically. On POSIX an
// os.Rename across two files in the same directory is atomic — and
// safe even while the running process has targetPath open, because
// the kernel keeps the old inode alive until every file descriptor
// referencing it is closed.
//
// Caller is responsible for cleaning up newPath if this function
// returns an error (which would only happen if it never got renamed).
func replaceBinary(newPath, targetPath string) error {
	if err := os.Rename(newPath, targetPath); err != nil {
		return fmt.Errorf("upgrade: replace %s: %w", targetPath, err)
	}
	return nil
}

// CleanupStaleOld is a no-op on non-Windows. Defined here so callers
// don't need a build tag around the call site.
func CleanupStaleOld(string) {}
