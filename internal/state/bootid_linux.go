//go:build linux

package state

// NewOSBootIDProvider returns the platform-native BootIDProvider for Linux.
func NewOSBootIDProvider() BootIDProvider {
	return NewLinuxBootIDProvider()
}
