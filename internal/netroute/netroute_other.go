//go:build !linux && !windows

package netroute

// Detect is not implemented on this platform.
func Detect() Info { return Info{} }
