//go:build !darwin && !windows && !linux

package netproxy

// system: elsewhere the desktop's proxy reaches apps as *_PROXY variables.
func system() (Proxy, bool) { return Proxy{}, true }
