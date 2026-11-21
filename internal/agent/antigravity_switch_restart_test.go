package agent

import (
	"context"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"
)

// A switch of agy's account (#1908) reaches the Antigravity app when it
// starts again: its row offers a Restart while it runs from before the
// switch, connected to magpie or not, and none once it has started since,
// or with no switch on record. The Restart quits it and opens it again.
func TestAntigravityRestartAfterAgySwitch(t *testing.T) {
	home := restartSandbox(t)
	apps := filepath.Join(home, "Applications")
	bundle := macApp(t, apps, "Antigravity.app", "com.google.antigravity", "Electron")
	restartOnMac = true
	appFolders = func() []string { return []string{apps} }
	alive, up := true, time.Hour
	running = func(pat string) []runningProc {
		var out []runningProc
		if p := (runningProc{up, bundle + "/Contents/MacOS/Electron"}); alive && regexp.MustCompile(pat).MatchString(p.cmd) {
			out = append(out, p)
		}
		return out
	}
	var at time.Time
	was := agySwitchedAt
	agySwitchedAt = func() time.Time { return at }
	t.Cleanup(func() { agySwitchedAt = was })
	a := antigravity(home)

	if a.AppBehind() != nil {
		t.Fatal("offered a Restart with no switch of agy's account")
	}
	at = time.Now().Add(-time.Minute)
	got := a.AppBehind()
	if got == nil {
		t.Fatal("no Restart for the Antigravity app started an hour before agy's account was switched")
	}
	if d := time.Since(got.Since); d < 59*time.Minute || d > 61*time.Minute {
		t.Fatalf("since %v ago", d)
	}
	up = 30 * time.Second
	if a.AppBehind() != nil {
		t.Fatal("offered a Restart for an app started after the switch")
	}
	up, alive = time.Hour, false
	if a.AppBehind() != nil {
		t.Fatal("offered a Restart for an app not running")
	}
	alive = true
	var did []string
	appQuit = func(_ context.Context, b string) error { did = append(did, "quit "+b); alive = false; return nil }
	appOpen = func(_ context.Context, b string) error { did = append(did, "open "+b); alive, up = true, 0; return nil }
	if err := a.RestartApp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"quit " + bundle, "open " + bundle}; !slices.Equal(did, want) {
		t.Fatalf("did %q, want %q", did, want)
	}
	if a.AppBehind() != nil {
		t.Fatal("still offered after the restart")
	}
}

// The Antigravity app on Windows is the per-user install's
// Antigravity.exe, never another app's executable of the same name.
func TestAntigravityRestartWindowsImage(t *testing.T) {
	r := restartApps["antigravity"]
	if !r.win(`C:\Users\u\AppData\Local\Programs\antigravity\Antigravity.exe`) {
		t.Fatal("the per-user install isn't the app")
	}
	if r.win(`C:\Tools\other\Antigravity.exe`) {
		t.Fatal("another folder's Antigravity.exe taken for the app")
	}
}
