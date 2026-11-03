//go:build linux && !nogui

package gui

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/yetone/magpie/internal/proc"
)

// openFolder starts the desktop's opener and leaves the file manager to open;
// Wails' OpenFileManager waits for it and kills it after ten seconds.
func openFolder(_ *application.App, path string) error { return proc.OpenFolder(path) }
