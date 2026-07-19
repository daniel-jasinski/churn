//go:build windows

package store

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// Mechanism (Windows): the lock file is opened with CreateFile and
// dwShareMode = 0 — no share access whatsoever. While this handle is open,
// any other CreateFile on the path (from this or any other process, whatever
// flags it asks for) fails with ERROR_SHARING_VIOLATION. This is mandatory
// share-mode exclusion enforced by the kernel, unlike O_CREATE|O_EXCL
// semantics, which only guard creation of a file that is deleted on exit and
// so go stale after a crash. The handle is released — and the exclusion
// lifted — when the process exits, however it exits.
//
// TestLockExclusiveAcrossProcesses verifies the exclusion against a real
// second process.

type lockHandle = windows.Handle

func acquireDirLock(path string) (*dirLock, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("store: lock path: %w", err)
	}
	h, err := windows.CreateFile(
		p,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0, // no sharing: the whole point
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return nil, fmt.Errorf("%w (%s)", ErrLocked, path)
		}
		return nil, fmt.Errorf("store: acquiring %s: %w", path, err)
	}
	return &dirLock{handle: h}, nil
}

func closeLockHandle(h lockHandle) error {
	return windows.CloseHandle(h)
}
