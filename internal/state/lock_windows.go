//go:build windows

package state

import "syscall"

// stillActive is the STILL_ACTIVE exit code reported for a running process.
const stillActive = 259

// processAlive reports whether pid is a live process.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	// PROCESS_QUERY_LIMITED_INFORMATION is enough to read the exit code and
	// works across integrity levels more often than PROCESS_QUERY_INFORMATION.
	const processQueryLimitedInformation = 0x1000

	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		// Access denied means the process exists but cannot be queried.
		return err == syscall.ERROR_ACCESS_DENIED
	}
	defer func() { _ = syscall.CloseHandle(h) }()

	var code uint32
	if err = syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}

	return code == stillActive
}
