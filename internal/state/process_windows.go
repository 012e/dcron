//go:build windows

package state

import (
	"golang.org/x/sys/windows"
)

const stillActive = 259 // STILL_ACTIVE on Windows

// OSProcessChecker checks process liveness using Win32 OpenProcess + GetExitCodeProcess.
type OSProcessChecker struct{}

func (OSProcessChecker) IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }()

	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
