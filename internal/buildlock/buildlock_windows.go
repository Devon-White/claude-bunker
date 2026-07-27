//go:build windows

package buildlock

import (
	"os"

	"golang.org/x/sys/windows"
)

// tryLock takes a non-blocking exclusive lock on the first byte of f via
// LockFileEx. If the region is already locked it returns errWouldBlock.
func tryLock(f *os.File) error {
	ol := new(windows.Overlapped)
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, ol,
	)
	if err == windows.ERROR_LOCK_VIOLATION {
		return errWouldBlock
	}
	return err
}

// unlock releases the LockFileEx lock held on f.
func unlock(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
}
