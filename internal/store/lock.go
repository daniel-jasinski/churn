package store

import "errors"

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
