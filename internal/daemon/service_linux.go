//go:build linux

package daemon

// NewOSServiceManager returns the Linux systemd --user ServiceManager.
func NewOSServiceManager(executor CommandExecutor) (ServiceManager, error) {
	return NewSystemdManager(executor)
}
