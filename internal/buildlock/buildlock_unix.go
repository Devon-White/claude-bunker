//go:build !windows

package buildlock

import "syscall"

// defaultPidAlive reports whether a process with pid is alive. Signal 0 does
// error-checking without sending a signal: nil => alive; EPERM => alive but
// owned by another user; ESRCH => no such process (dead).
func defaultPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
