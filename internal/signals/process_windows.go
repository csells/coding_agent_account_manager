//go:build windows

package signals

// IsProcessAlive reports whether a process with the given PID exists.
func IsProcessAlive(pid int) bool {
	// Not reliably detectable on Windows without platform-specific APIs.
	// Treat as unknown/dead so stale pid files do not block startup.
	return false
}
