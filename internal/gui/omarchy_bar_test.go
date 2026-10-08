package gui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// barWin is the desktop app as a Windows; the Bar icon routes reach no
// method of it.
type barWin struct{ Windows }

// Settings' Bar icon tells the app whether magpie's icon is in Omarchy's
// bar, so the tray's own item leaves the bar's tray while the widget is
// there and comes back once it is taken out (Alex on Discord: with Bar icon
// on, Omarchy's bar showed two magpies).
func TestBarIconTellsTheTray(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in omarchy commands are shell scripts")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	bin := t.TempDir()
	for _, name := range []string{"omarchy", "omarchy-shell"} {
		testenv.Program(t, filepath.Join(bin, name), "#!/bin/sh\nexit 0\n")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	priorOm, priorHypr, priorHook := onOmarchy, hyprland, onBarIcon
	t.Cleanup(func() { onOmarchy, hyprland, onBarIcon = priorOm, priorHypr, priorHook })
	onOmarchy, hyprland = true, func() bool { return true }
	var told []bool
	onBarIcon = func(on bool) { told = append(told, on) }

	mux := http.NewServeMux()
	omarchyRoutes(mux, &barWin{})
	post := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/omarchy/widget", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("POST %s: %d %s", body, rec.Code, rec.Body)
		}
	}
	post(`{"on":true}`)
	if len(told) != 1 || !told[0] {
		t.Fatalf("Bar icon on: the tray was told %v, want [true] (it stays in the bar's tray beside the widget)", told)
	}
	post(`{"on":false}`)
	if len(told) != 2 || told[1] {
		t.Fatalf("Bar icon off: the tray was told %v, want [true false] (no magpie left in the bar)", told)
	}
}
