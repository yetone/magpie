//go:build !nogui

package gui

import (
	"os"
	"strings"
	"testing"
)

// The Mac's plain title bar makes a window of its own (toolbarRounder). Done
// in ApplicationStarted, it deadlocked the main thread of an update's hidden
// relaunch on macOS 26, which is still inside FBSWorkspaceScenesClient then
// (eating_dream on X). It is done as the main window is first shown instead.
func TestMacTitleBarNotDoneAtStart(t *testing.T) {
	app, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	i := strings.LastIndex(string(app), "events.Common.ApplicationStarted, func(")
	if i < 0 {
		t.Fatal("no ApplicationStarted handler in app.go")
	}
	body := string(app)[i:]
	body = body[:strings.Index(body, "markReady()")]
	if strings.Contains(strings.ReplaceAll(body, `if runtime.GOOS != "darwin" {
				plainTitlebar(h.main)`, ""), "plainTitlebar(") {
		t.Error("ApplicationStarted calls plainTitlebar on the Mac")
	}
	lw, err := os.ReadFile("lightweight.go")
	if err != nil {
		t.Fatal(err)
	}
	r := string(lw)
	r = r[strings.Index(r, "func (h *host) reveal("):]
	r = r[:strings.Index(r, "\n}\n")]
	if !strings.Contains(r, "plainTitlebar(w)") {
		t.Error("reveal doesn't give the Mac's main window its plain title bar")
	}
}
