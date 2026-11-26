//go:build windows

package daemon

// NewOSServiceManager returns the Windows Task Scheduler ServiceManager.
func NewOSServiceManager(executor CommandExecutor) (ServiceManager, error) {
	return NewWindowsTaskManager(executor)
}
