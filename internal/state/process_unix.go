//go:build !windows

package state

import (
	"errors"
	"syscall"
)

// OSProcessChecker checks process liveness using signal 0 on Unix systems.
type OSProcessChecker struct{}

func (OSProcessChecker) IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
