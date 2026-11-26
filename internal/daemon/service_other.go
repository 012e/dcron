//go:build !linux && !darwin && !windows

package daemon

// NewOSServiceManager returns nil on platforms without a built-in service manager.
func NewOSServiceManager(CommandExecutor) (ServiceManager, error) {
	return nil, nil
}
