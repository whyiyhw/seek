//go:build !windows

package routines

import (
	"errors"
	"os"
	"syscall"
)

// tryFlockNB attempts a non-blocking exclusive flock on f.
// Returns (true, nil) on success, (false, nil) when another
// process holds it (EWOULDBLOCK), or (false, err) for any other
// error. syscall.Flock is the canonical advisory-lock primitive
// on Linux/BSD/macOS; the LOCK_NB flag is the difference between
// "block forever" (default) and "fail fast" (what we want).
func tryFlockNB(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	// EWOULDBLOCK / EAGAIN both signal "held by another"; treat
	// them as the skip-silently signal.
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return false, err
}

// flockUnlock releases the lock without closing the fd. Caller
// closes the fd separately so lsof shows the release event
// before the descriptor disappears.
func flockUnlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
