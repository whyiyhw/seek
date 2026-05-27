//go:build !windows

package pathop

import (
	"os"
	"path/filepath"
	"strings"
)

// IsInPATH reports whether dir appears in the current process PATH.
func IsInPATH(dir string) bool {
	return pathContainsDir(os.Getenv("PATH"), dir, false)
}

// IsInUserPATH reports whether dir is in the user's permanent PATH.
// On non-Windows platforms this always returns false — shell rc edits
// are out of scope for seek.
func IsInUserPATH(dir string) (bool, error) {
	return false, nil
}

// EnsureInPATH ensures dir is in the user's permanent PATH.
// On non-Windows platforms this is deliberately a no-op — modifying
// shell rc files is a separate concern. Returns false on non-Windows.
func EnsureInPATH(dir string) (bool, error) {
	return false, nil
}

// EnsureInPATHWithBroadcast is the same as EnsureInPATH but with a
// system-wide broadcast. No-op on non-Windows.
func EnsureInPATHWithBroadcast(dir string) (bool, error) {
	return false, nil
}
