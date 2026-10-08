//go:build !linux && !nogui

package gui

import "github.com/wailsapp/wails/v3/pkg/application"

// setTrayTip: Wails' SetTooltip does it here.
func setTrayTip(*application.SystemTray, string) bool { return true }

// setTrayPassive: only Linux has Omarchy's bar to put the icon in instead.
func setTrayPassive(*application.SystemTray, bool) bool { return true }
