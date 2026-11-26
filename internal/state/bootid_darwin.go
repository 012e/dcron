//go:build darwin

package state

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

type darwinBootIDProvider struct{}

// NewOSBootIDProvider returns a BootIDProvider derived from macOS kern.boottime.
func NewOSBootIDProvider() BootIDProvider {
	return darwinBootIDProvider{}
}

func (darwinBootIDProvider) BootID() (string, error) {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return "", fmt.Errorf("read kern.boottime: %w", err)
	}
	return fmt.Sprintf("%d.%06d", tv.Sec, tv.Usec), nil
}

func (darwinBootIDProvider) BootTime() (time.Time, error) {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return time.Time{}, fmt.Errorf("read kern.boottime: %w", err)
	}
	return time.Unix(int64(tv.Sec), int64(tv.Usec)*1000).UTC(), nil
}
