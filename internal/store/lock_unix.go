//go:build !windows

package store

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// Mechanism (non-Windows): a BSD advisory lock — flock(LOCK_EX|LOCK_NB) — on
// the lock file. The lock belongs to the open file description, so it is
// released automatically when the process exits, however it exits; a crash
// never leaves a stale lock. All churn processes use the same flock call, so
// the advisory nature of the lock is sufficient among ourselves.

type lockHandle = *os.File

func acquireDirLock(path string) (*dirLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("store: opening %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// Best-effort cleanup: we hold no lock, and the open descriptor is the
		// only thing to give back. The flock failure is the error worth
		// reporting, so a close failure here has nowhere useful to go.
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (%s)", ErrLocked, path)
		}
		return nil, fmt.Errorf("store: locking %s: %w", path, err)
	}
	return &dirLock{handle: f}, nil
}

func closeLockHandle(f lockHandle) error {
	// Closing the descriptor releases the flock.
	return f.Close()
}
