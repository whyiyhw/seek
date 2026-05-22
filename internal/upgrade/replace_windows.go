//go:build windows

package upgrade

import (
	"fmt"
	"os"
)

// replaceBinary handles Windows' "running .exe is locked" rule. We
// rename the current binary out of the way first (this Windows DOES
// allow — the running process keeps its handle), then put the new
// binary in place. The leftover .old can't be deleted while the
// process is alive, so we leave it; a follow-up cleanupStaleOld()
// call at next startup removes it.
func replaceBinary(newPath, targetPath string) error {
	oldPath := targetPath + ".old"
	// Best-effort: a previous failed upgrade may have left an .old
	// behind. Remove it before reusing the name.
	_ = os.Remove(oldPath)

	if err := os.Rename(targetPath, oldPath); err != nil {
		return fmt.Errorf("upgrade: rename current to .old: %w", err)
	}
	if err := os.Rename(newPath, targetPath); err != nil {
		// Roll back: put the original back so the user isn't left
		// with no binary at all.
		_ = os.Rename(oldPath, targetPath)
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
