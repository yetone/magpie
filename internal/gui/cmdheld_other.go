//go:build !darwin && !nogui

package gui

// cmdClick: only the Mac drags its tray icons with a modifier.
func cmdClick() bool { return false }
