//go:build !nogui

package gui

import (
	"fmt"
	"os"
	"syscall"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/yetone/magpie/internal/proc"
)

// revealFolder shows the folder selected in its parent's Explorer window,
// to drag from there: started and left to open, as openFolder does.
func revealFolder(_ *application.App, path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	cmd := proc.Command(explorer())
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CmdLine = fmt.Sprintf(`explorer.exe /select,"%s"`, path)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("explorer: %w", err)
	}
	proc.ClearProcessEfficiencyMode(cmd.Process.Pid)
	go cmd.Wait()
	return nil
}
