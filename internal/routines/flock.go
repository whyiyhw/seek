package routines

import (
	"errors"
	"os"
	"path/filepath"
)

// FileLock is an advisory cross-process lock backed by a real
// file on disk. Acquired with TryLock (non-blocking) — tick uses
// this to skip when a prior tick or Store mutation hasn't
// finished. Implementation lives in flock_unix.go / flock_
// windows.go per build tag.
//
// Lifecycle:
//
//	lk, ok, err := TryLock(path)
//	if err != nil { ... }
//	if !ok { return /* skip — lock held */ }
//	defer lk.Close()
//
// Close releases the lock AND closes the underlying file
// descriptor. Calling Close on a nil receiver is a no-op so
// `defer lk.Close()` is always safe even on the !ok path.
//
// Lock files persist on disk between runs — that's the point
// of advisory file locks. We never delete them; only the inode
// matters for kernel-level locking.
type FileLock struct {
	f *os.File
}

// ErrLockHeld is returned by TryLock when another process holds
// the lock. Distinguished from generic I/O errors so callers can
// surface "skip silently" vs "actual problem to log".
var ErrLockHeld = errors.New("routines: file lock held by another process")

// TryLock attempts a non-blocking exclusive lock on the file at
// path. Returns:
//
//   - (*FileLock, true,  nil)  — got the lock; caller MUST Close to release
//   - (nil,       false, nil)  — lock held by another process; skip silently
//   - (nil,       false, err)  — I/O error (couldn't create file, etc)
//
// The function creates the lock file if missing (and the parent
// directory, lazily). The file is NEVER truncated — its contents
// don't matter; only the inode's kernel-level lock state does.
func TryLock(path string) (*FileLock, bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, false, err
	}
	ok, err := tryFlockNB(f)
	if err != nil {
		_ = f.Close()
		return nil, false, err
	}
	if !ok {
		_ = f.Close()
		return nil, false, nil
	}
	return &FileLock{f: f}, true, nil
}

// Close releases the lock and closes the underlying fd.
// Nil-receiver safe so defer chains work after a missed
// acquire.
func (l *FileLock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	// On Unix flockUnlock requires the fd; on Windows the
	// LockFileEx region is keyed by handle. Doing the unlock
	// BEFORE Close keeps the explicit "release" event before
	// "fd gone" — both are observable in lsof.
	_ = flockUnlock(l.f)
	err := l.f.Close()
	l.f = nil
	return err
}
