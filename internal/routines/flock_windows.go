//go:build windows

package routines

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// tryFlockNB attempts a non-blocking exclusive lock on f via
// LockFileEx (the Windows equivalent of flock). We lock the
// entire file (offset 0, length max) with LOCKFILE_FAIL_IMMEDIATELY
// — that's the "fail fast" flag matching Unix LOCK_NB. The lock
// is keyed by the underlying handle, so the matching Unlock
// must use the same handle (we keep f.Fd() stable until Close).
func tryFlockNB(f *os.File) (bool, error) {
	var ol windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,             // reserved
		0xFFFFFFFF,    // nNumberOfBytesToLockLow — entire file
		0xFFFFFFFF,    // nNumberOfBytesToLockHigh
		&ol,
	)
	if err == nil {
		return true, nil
	}
	// ERROR_LOCK_VIOLATION is what LockFileEx returns when the
	// region is held by another handle (after FAIL_IMMEDIATELY
	// stops it from blocking). Treat as the skip-silently signal,
	// matching Unix EWOULDBLOCK semantics.
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return false, err
}

// flockUnlock releases the lock without closing the handle.
// Matches Unix flockUnlock so the higher-level FileLock.Close
// path is platform-uniform.
func flockUnlock(f *os.File) error {
	var ol windows.Overlapped
	return windows.UnlockFileEx(
		windows.Handle(f.Fd()),
		0,
		0xFFFFFFFF,
		0xFFFFFFFF,
		&ol,
	)
}
