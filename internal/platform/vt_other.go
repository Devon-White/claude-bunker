//go:build !windows

package platform

// EnableVTMode is a no-op on non-Windows platforms where VT100
// escape sequences are natively supported.
func EnableVTMode() {}
