//go:build darwin

package daemon

// NewOSServiceManager returns the macOS launchd LaunchAgent ServiceManager.
func NewOSServiceManager(executor CommandExecutor) (ServiceManager, error) {
	return NewLaunchdManager(executor)
}
