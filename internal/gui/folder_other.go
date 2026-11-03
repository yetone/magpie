//go:build !windows && !linux && !nogui

package gui

import "github.com/wailsapp/wails/v3/pkg/application"

func openFolder(app *application.App, path string) error {
	return app.Env.OpenFileManager(path, false)
}
