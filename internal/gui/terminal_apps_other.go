//go:build !darwin || !cgo

package gui

import "runtime"

// discoverTerminals off a Mac: the terminals on PATH (terminal_open.go).
func discoverTerminals() (terminalDiscovery, error) { return discoverOwnTerminals(runtime.GOOS) }
