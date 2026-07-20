package store

import (
	"errors"
	"os"
	"path/filepath"
)

// ErrLocked is returned by Open when another process holds the data
// directory's churn.lock.
var ErrLocked = errors.New("store: data directory is locked by another process")

// dirLock is an OS-level exclusive lock on the data directory's churn.lock,
// held for the life of the process (or until release). The mechanism is
// platform-specific — see lock_windows.go and lock_unix.go. In both cases
// the operating system drops the lock when the process dies, however it
// dies, so a crash never leaves a stale lock behind.
type dirLock struct {
	handle lockHandle
}

// release drops the lock. Safe to call once; the process exiting releases it
// implicitly.
func (l *dirLock) release() error {
	return closeLockHandle(l.handle)
}

// InUse reports whether another process currently holds dir's lock — that is,
// whether a churn server or import is live in it.
//
// This is a DIAGNOSTIC probe, not a gate: it takes the lock and immediately
// releases it, so the answer is a snapshot that may be stale by the time the
// caller acts on it. Callers must still handle ErrLocked from Open; the point
// of InUse is to tell an operator *why* a directory looks the way it does
// before anything is opened. It creates the lock file if absent, but nothing
// else, and a missing directory is simply not in use.
func InUse(dir string) (bool, error) {
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	lock, err := acquireDirLock(filepath.Join(dir, LockFileName))
	if errors.Is(err, ErrLocked) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, lock.release()
}
