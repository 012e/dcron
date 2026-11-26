//go:build !linux && !darwin && !windows

package state

import "errors"

type noopBootIDProvider struct{}

// NewOSBootIDProvider returns a fallback BootIDProvider on unsupported platforms.
func NewOSBootIDProvider() BootIDProvider {
	return noopBootIDProvider{}
}

func (noopBootIDProvider) BootID() (string, error) {
	return "", errors.New("boot ID unsupported on this platform")
}
