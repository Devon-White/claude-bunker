//go:build windows

package buildlock

import "golang.org/x/sys/windows"

// defaultPidAlive reports whether a process with pid is alive on Windows.
// os.FindProcess always "succeeds", so query the process exit code instead:
// STILL_ACTIVE (259) means running.
func defaultPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	const stillActive = 259 // STILL_ACTIVE
	return code == stillActive
}
