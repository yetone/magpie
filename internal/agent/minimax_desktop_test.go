package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// miniMaxDesktopPlist is MiniMax Code Desktop 3.1.10's Info.plist (MiniMax
// Code-3.1.10-arm64-mac.zip from filecdn.minimax.chat, the China build) cut
// to the keys magpie reads, in their order and indentation. The
// international build (file.cdn.minimax.io) is the same bundle name with
// the id com.minimax.agent (app-identity.js).
const miniMaxDesktopPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>CFBundleDisplayName</key>
    <string>MiniMax Code</string>
    <key>CFBundleExecutable</key>
    <string>MiniMax Code</string>
    <key>CFBundleIdentifier</key>
    <string>com.minimax.agent.cn</string>
    <key>CFBundleName</key>
    <string>MiniMax Code</string>
    <key>CFBundleShortVersionString</key>
    <string>3.1.10</string>
  </dict>
</plist>
`

// 教父 on Discord (MiniMax Code 3.1.10, magpie v0.1.2629 and v0.1.2679):
// MiniMax Code turned on in magpie with a model picked, and the desktop app
// went on with the user's own MiniMax-M3.1-Flash; Sessions showed nothing.
// Desktop's window keeps the model last picked in it and shows that, not
// config.yaml's defaultModel, while that model is still listed. So with the
// desktop app installed, either build, magpie's notice says to restart it
// and pick a magpie model once in its menu, and its version is shown.
// Without it the mcode advice stays as it was.
func TestMiniMaxDesktopSaysPickInTheApp(t *testing.T) {
	home, _ := miniMaxHome(t)
	apps := t.TempDir()
	wasFolders, wasSpot, wasRunning := appFolders, spotlightApps, Running
	appFolders = func() []string { return []string{apps} }
	spotlightApps = func(string) []string { return nil }
	t.Cleanup(func() { appFolders, spotlightApps, Running = wasFolders, wasSpot, wasRunning })
	mcode := false
	Running = func(...string) bool { return mcode }

	a := miniMax(home)
	if n := a.Notice(); n != "" {
		t.Fatalf("no app, no mcode running: notice %q", n)
	}
	mcode = true
	if n := a.Notice(); n != noticeMinimax.String() {
		t.Fatalf("no app, mcode running: notice %q", n)
	}

	for _, id := range []string{"com.minimax.agent.cn", "com.minimax.agent"} {
		bundle := filepath.Join(apps, "MiniMax Code.app")
		os.RemoveAll(bundle)
		if err := os.MkdirAll(filepath.Join(bundle, "Contents"), 0o755); err != nil {
			t.Fatal(err)
		}
		plist := strings.Replace(miniMaxDesktopPlist, "com.minimax.agent.cn", id, 1)
		if err := os.WriteFile(filepath.Join(bundle, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, running := range []bool{false, true} {
			mcode = running
			if n := a.Notice(); n != noticeMinimaxDesktop.String() {
				t.Errorf("%s installed, mcode running %v: notice %q", id, running, n)
			}
		}
		if v := a.AppVersion(); v != "3.1.10" {
			t.Errorf("%s installed: app version %q", id, v)
		}
	}

	// another app under the same name is not MiniMax Code's
	plist := strings.Replace(miniMaxDesktopPlist, "com.minimax.agent.cn", "com.example.other", 1)
	if err := os.WriteFile(filepath.Join(apps, "MiniMax Code.app", "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	mcode = false
	if n := a.Notice(); n != "" {
		t.Errorf("another bundle id: notice %q", n)
	}
}
