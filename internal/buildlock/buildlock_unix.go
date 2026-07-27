//go:build !windows

package buildlock

import (
	"os"
	"syscall"
)

// tryLock takes a non-blocking exclusive advisory lock (flock LOCK_EX|LOCK_NB).
// If another open file description holds the lock it returns errWouldBlock;
// other errors are returned as-is.
func tryLock(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
		return errWouldBlock
	}
	return err
}

// unlock releases the advisory lock held on f.
func unlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
