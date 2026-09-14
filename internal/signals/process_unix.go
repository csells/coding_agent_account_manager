//go:build !windows

package signals

import (
	"errors"
	"os"
	"syscall"
)

// IsProcessAlive reports whether a process with the given PID exists.
// On Unix it sends signal 0; EPERM means the process exists but cannot be
// signalled, and only ESRCH (or any other error) means it is gone.
func IsProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := process.Signal(syscall.Signal(0)); err == nil {
		return true
	} else if errors.Is(err, syscall.EPERM) {
		// Process exists but we don't have permission to signal it.
		return true
	}
	return false
}
