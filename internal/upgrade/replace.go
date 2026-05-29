//go:build !windows

package upgrade

import (
	"errors"
	"fmt"
	"io/fs"
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
//
// Permission failures get a SPECIAL error message — the install
// one-liner puts the binary in /usr/local/bin (root-owned), so any
// user running plain `seek -upgrade` without sudo will hit EACCES.
// The default Go error string ("operation not permitted" /
// "permission denied") doesn't say "try sudo"; users have to figure
// that out from context. Detecting fs.ErrPermission and rewriting
// the message saves that one search.
func replaceBinary(newPath, targetPath string) error {
	if err := os.Rename(newPath, targetPath); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return fmt.Errorf("upgrade: cannot write %s — requires elevated privileges. Try: sudo seek -upgrade", targetPath)
		}
		return fmt.Errorf("upgrade: replace %s: %w", targetPath, err)
	}
	return nil
}

// CleanupStaleOld is a no-op on non-Windows. Defined here so callers
// don't need a build tag around the call site.
func CleanupStaleOld(string) {}
