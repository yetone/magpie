//go:build !nogui

package gui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/yetone/magpie/internal/settings"
)

// StringKe on Discord: magpie restarted for an update while another app was
// full screen, and from then on its window opened only on that app's Space.
// The window moves to the Space the user is on when shown, and can still go
// full screen; it doesn't join every Space.
func TestMainWindowFollowsActiveSpace(t *testing.T) {
	m := mainMacWindow()
	b := m.CollectionBehavior
	if b&application.MacWindowCollectionBehaviorMoveToActiveSpace == 0 {
		t.Errorf("main window collection behaviour %d does not move to the active Space", b)
	}
	if b&application.MacWindowCollectionBehaviorFullScreenPrimary == 0 {
		t.Errorf("main window collection behaviour %d can no longer go full screen", b)
	}
	if b&(application.MacWindowCollectionBehaviorCanJoinAllSpaces|application.MacWindowCollectionBehaviorFullScreenAuxiliary) != 0 {
		t.Errorf("main window collection behaviour %d joins all Spaces or full-screen ones", b)
	}
	if m.TitleBar != application.MacTitleBarHiddenInset {
		t.Errorf("main window title bar changed: %+v", m.TitleBar)
	}
}

// StringKe on Discord: a full-screen window closed left a black Space
// behind. On the Mac it leaves full screen before it is hidden.
func TestCloseStepLeavesFullscreen(t *testing.T) {
	cases := []struct {
		goos       string
		fullscreen bool
		want       closeAction
	}{
		{"darwin", true, closeLeaveFullscreen},
		{"darwin", false, closeHide},
		{"windows", true, closeHide},
		{"linux", true, closeHide},
		{"linux", false, closeHide},
	}
	for _, c := range cases {
		if got := closeStep(c.goos, c.fullscreen); got != c.want {
			t.Errorf("closeStep(%s, fullscreen=%v) = %v, want %v", c.goos, c.fullscreen, got, c.want)
		}
	}
}

// lainbo on v0.1.819 (#763): with the Dock set to hidden, a full-screen window
// had no close, minimise or full-screen buttons, and only Esc took it out of
// full screen. A full-screen window keeps magpie in the Dock, whatever the setting.
func TestFullscreenKeepsDock(t *testing.T) {
	hidden, window, shown := settings.Settings{DockNever: true}, settings.Settings{DockWindow: true}, settings.Settings{Dock: true}
	cases := []struct {
		s                 settings.Settings
		shown, fullscreen bool
		want              bool
	}{
		{hidden, true, true, true},
		{hidden, true, false, false},
		{hidden, false, false, false},
		{window, true, true, true},
		{window, true, false, true},
		{window, false, false, false},
		{shown, false, false, true},
	}
	for _, c := range cases {
		if got := inDock(c.s, c.shown, c.fullscreen); got != c.want {
			t.Errorf("inDock(%+v, shown=%v, fullscreen=%v) = %v, want %v", c.s, c.shown, c.fullscreen, got, c.want)
		}
	}
}

// ShouguangCao on X: with Show in Dock left as it came, the open window was
// out of Command-Tab, so going back to it to paste a key copied in another
// app took the menu bar. Out of the box magpie is in the Dock while its
// window is open and leaves it when the window closes; Hide keeps it out,
// and Show keeps it in.
func TestDockFollowsWindowByDefault(t *testing.T) {
	cases := []struct {
		name  string
		s     settings.Settings
		shown bool
		want  bool
	}{
		{"default, window open", settings.Settings{}, true, true},
		{"default, window closed", settings.Settings{}, false, false},
		{"an older file's With window, open", settings.Settings{DockWindow: true}, true, true},
		{"Hide, window open", settings.Settings{DockNever: true}, true, false},
		{"Show, window closed", settings.Settings{Dock: true}, false, true},
		{"Show wins over Hide", settings.Settings{Dock: true, DockNever: true}, false, true},
	}
	for _, c := range cases {
		if got := inDock(c.s, c.shown, false); got != c.want {
			t.Errorf("%s: inDock = %v, want %v", c.name, got, c.want)
		}
	}
}

// yogurt36 on Discord: an account shown by its eye while Hide accounts is on
// is shown for the window's session only. Closing the window only hides it,
// so hideMain tells the page; the call it makes is one app.js answers, and
// what answers it forgets every account shown.
func TestClosingHidesShownAccounts(t *testing.T) {
	if mainClosedJS != "window.hideAccounts?.closed?.()" {
		t.Fatalf("hideMain runs %q", mainClosedJS)
	}
	src, err := os.ReadFile("mainwindow.go")
	if err != nil {
		t.Fatal(err)
	}
	_, hide, _ := strings.Cut(string(src), "func (h *host) hideMain() {")
	hide, _, _ = strings.Cut(hide, "\n}\n")
	if i, j := strings.Index(hide, "h.main.ExecJS(mainClosedJS)"), strings.Index(hide, "hideWebview(h.main)"); i < 0 || j < i {
		t.Fatalf("hideMain doesn't tell the page before it hides the window:%s", hide)
	}
	js, err := os.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	_, fn, ok := strings.Cut(string(js), "window.hideAccounts = {")
	if !ok {
		t.Fatal("app.js has no window.hideAccounts")
	}
	_, fn, ok = strings.Cut(fn, "\n    closed: () => {")
	if !ok {
		t.Fatal("window.hideAccounts has no closed")
	}
	fn, _, _ = strings.Cut(fn, "\n")
	if !strings.Contains(fn, "shown.clear()") {
		t.Fatalf("closed forgets no shown account: %q", fn)
	}
}

// The main window on Windows has no title bar of its own; the page's header
// is its title bar (258a14 on X: a wallpaper stopped under a white strip).
// Linux and the Mac keep theirs.
func TestMainWindowFramelessOnWindowsOnly(t *testing.T) {
	for goos, want := range map[string]bool{"windows": true, "darwin": false, "linux": false} {
		if got := mainFrameless(goos); got != want {
			t.Errorf("mainFrameless(%q) = %v, want %v", goos, got, want)
		}
	}
	src, err := os.ReadFile("lightweight.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "Frameless:        mainFrameless(runtime.GOOS),") {
		t.Error("makeMain doesn't make the main window by mainFrameless")
	}
}

// A frameless window is resized by Wails' edges inside the page, which
// Wails arms once its script is ready, before or after magpie's hook runs.
// Restored, they stay on, so the window can be resized; maximised, they
// stay off whichever came first and however often, so the top-right pixel
// is Close's and the top row drags. The page hears the state each time.
// The stand-in keeps what each setResizable call said, as drag.ts does.
func TestCaptionKeepsEdgesOffWhileMaximised(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is needed to run the page's script: ", err)
	}
	stub := `const said=[],heard=[];window._wails={setResizable:v=>said.push(v)};window.magpieCaption=m=>heard.push(m);`
	ready := `window._wails.setResizable(true);` // what Wails runs once its script is ready
	expect := func(edges, m bool) string {
		return fmt.Sprintf(`if(said.at(-1)!==%t||heard.at(-1)!==%t||window.magpieMaximised!==%t){console.log(JSON.stringify({said,heard}));process.exit(1)}`, edges, m, m)
	}
	max, restored := captionJS(true), captionJS(false)
	for name, js := range map[string]string{
		"maximised, hook after Wails":  stub + ready + max + expect(false, true),
		"maximised, hook before Wails": stub + max + ready + expect(false, true),
		"maximised, hook twice":        stub + max + max + ready + ready + expect(false, true),
		"restored, hook after Wails":   stub + ready + restored + expect(true, false),
		"restored, hook before Wails":  stub + restored + ready + expect(true, false),
		"restored after maximised":     stub + ready + max + restored + expect(true, false),
		"maximised after restored":     stub + ready + restored + max + expect(false, true),
		"Wails says not resizable":     stub + restored + `window._wails.setResizable(false);` + restored + expect(false, false),
		"no runtime":                   `const window={};` + max,
	} {
		script := "const window=globalThis;" + js
		if name == "no runtime" {
			script = "(()=>{" + js + "})()"
		}
		out, err := exec.Command(node, "-e", script).CombinedOutput()
		if err != nil {
			t.Errorf("%s: %v %s", name, err, out)
		}
	}
}
