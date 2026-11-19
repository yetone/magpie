//go:build !nogui

package gui

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/yetone/magpie/internal/proc"
)

// openFolder starts Explorer on the folder and leaves it to open: not on the
// window's thread, and not waited for, where Wails' own way holds the thread
// the windows are drawn on until Explorer is done and kills it after ten
// seconds.
func openFolder(_ *application.App, path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	cmd := proc.Command(explorer())
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Explorer reads its own command line: the path quoted, not escaped as Go would
	cmd.SysProcAttr.CmdLine = fmt.Sprintf(`explorer.exe "%s"`, path)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("explorer: %w", err)
	}
	proc.ClearProcessEfficiencyMode(cmd.Process.Pid)
	go cmd.Wait() // it exits 1 when it did open the folder
	return nil
}

// explorer is Explorer's own path: it isn't looked for on PATH, which an app
// started some ways (an updater's restart, a shortcut of another program's)
// can have without the Windows folder, and then "explorer.exe" isn't found.
func explorer() string {
	for _, v := range []string{"SystemRoot", "windir"} {
		if dir := os.Getenv(v); dir != "" {
			if p := filepath.Join(dir, "explorer.exe"); fileExists(p) {
				return p
			}
		}
	}
	return `C:\Windows\explorer.exe`
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
