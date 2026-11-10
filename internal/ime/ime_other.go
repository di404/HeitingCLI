//go:build !darwin

// Package ime is only implemented on macOS.
package ime

// SetEnabled does nothing on this platform.
func SetEnabled(bool) {}
