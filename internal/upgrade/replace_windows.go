//go:build windows

package upgrade

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// replaceBinary handles Windows' "running .exe is locked" rule. We
// rename the current binary out of the way first (this Windows DOES
// allow — the running process keeps its handle), then put the new
// binary in place. The leftover .old can't be deleted while the
// process is alive, so we leave it; a follow-up cleanupStaleOld()
// call at next startup removes it.
//
// Permission failures get a special error message — if the user
// dropped the binary into C:\Program Files\ instead of a user-
// owned PATH dir, both rename calls below fail with "Access is
// denied" (mapped to fs.ErrPermission). The default error string
// doesn't suggest "run as administrator"; rewriting it does.
func replaceBinary(newPath, targetPath string) error {
	oldPath := targetPath + ".old"
	// Best-effort: a previous failed upgrade may have left an .old
	// behind. Remove it before reusing the name.
	_ = os.Remove(oldPath)

	if err := os.Rename(targetPath, oldPath); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return fmt.Errorf("upgrade: cannot write %s — requires elevated privileges. Re-run from a PowerShell launched as Administrator", targetPath)
		}
		return fmt.Errorf("upgrade: rename current to .old: %w", err)
	}
	if err := os.Rename(newPath, targetPath); err != nil {
		// Roll back: put the original back so the user isn't left
		// with no binary at all.
		_ = os.Rename(oldPath, targetPath)
		if errors.Is(err, fs.ErrPermission) {
			return fmt.Errorf("upgrade: cannot install new binary at %s — requires elevated privileges. Re-run from a PowerShell launched as Administrator", targetPath)
		}
		return fmt.Errorf("upgrade: install new binary: %w", err)
	}
	return nil
}

// CleanupStaleOld removes the leftover "<exe>.old" file from a
// previous Windows upgrade. Called at startup; safe to call when no
// stale file exists or on non-Windows (the file simply isn't found).
func CleanupStaleOld(exePath string) {
	_ = os.Remove(exePath + ".old")
}
