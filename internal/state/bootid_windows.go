//go:build windows

package state

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const winBootIDSubkey = `SYSTEM\CurrentControlSet\Control\Session Manager\Memory Management\PrefetchParameters`

type windowsBootIDProvider struct{}

// NewOSBootIDProvider returns a BootIDProvider derived from Windows boot metadata.
func NewOSBootIDProvider() BootIDProvider {
	return windowsBootIDProvider{}
}

func (windowsBootIDProvider) BootID() (string, error) {
	if id, ok := readWindowsRegistryBootID(); ok {
		return fmt.Sprintf("win-boot-%d", id), nil
	}
	uptime := time.Duration(windows.DurationSinceBoot())
	bootTime := time.Now().UTC().Add(-uptime).Round(time.Minute)
	return bootTime.Format(time.RFC3339), nil
}

func (windowsBootIDProvider) BootTime() (time.Time, error) {
	uptime := time.Duration(windows.DurationSinceBoot())
	return time.Now().UTC().Add(-uptime), nil
}

func readWindowsRegistryBootID() (uint64, bool) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, winBootIDSubkey, registry.QUERY_VALUE)
	if err != nil {
		return 0, false
	}
	defer func() { _ = k.Close() }()

	val, _, err := k.GetIntegerValue("BootId")
	if err != nil || val == 0 {
		return 0, false
	}
	return val, true
}
