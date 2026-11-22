package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// vibeAppPlist is Vibe Desktop 1.3.0's Info.plist (Vibe.app from Mistral's
// vibe-1.3.0-arm64-mac.zip, its sha512 checked against latest-mac.yml),
// cut to the keys around the ones read.
const vibeAppPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDisplayName</key>
	<string>Vibe</string>
	<key>CFBundleExecutable</key>
	<string>Vibe</string>
	<key>CFBundleIdentifier</key>
	<string>ai.mistral.lechat.desktop</string>
	<key>CFBundleName</key>
	<string>Vibe</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>1.3.0</string>
	<key>CFBundleVersion</key>
	<string>1.3.0</string>
</dict>
</plist>
`

// @jtbbtnt54249 on X (2026-11-18): Mistral Vibe is out on desktop for
// macOS and Windows, add it. Vibe Desktop runs the CLI's app server on the
// CLI's ~/.vibe/config.toml, so magpie's Vibe wiring reaches it as it is,
// but it needs no CLI, and with only the app installed the Vibe agent
// wasn't listed. With the app it is, with the app's version, no CLI
// install offered, and the notice says a new chat in the app takes the
// change.
func TestVibeDesktopAppDetected(t *testing.T) {
	judgeFor(t, "darwin")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("VIBE_HOME", "")
	t.Setenv("PATH", t.TempDir()) // no vibe CLI
	apps, userApps := t.TempDir(), t.TempDir()
	wasFolders, wasSpot, wasRunning := appFolders, spotlightApps, Running
	appFolders = func() []string { return []string{apps, userApps} }
	spotlightApps = func(string) []string { return nil }
	cli := false
	Running = func(...string) bool { return cli }
	t.Cleanup(func() { appFolders, spotlightApps, Running = wasFolders, wasSpot, wasRunning })
	a := vibe(home)
	// a name no machine has: proc.FindTool also looks where users' tools go
	a.Bin = "magpie-test-gone-vibe"
	offered := func() bool {
		for _, x := range installsOf([]*Agent{a}, "darwin", true) {
			if x.ID == vibeID {
				return true
			}
		}
		return false
	}

	// nothing here
	if a.Detected() || a.Notice() != "" {
		t.Fatalf("nothing installed: detected %v, notice %q", a.Detected(), a.Notice())
	}
	cli = true
	if n := a.Notice(); n != noticeVibe.String() {
		t.Fatalf("no app, vibe running: notice %q", n)
	}
	cli = false

	// the app in /Applications, never opened: no ~/.vibe yet
	installApp(t, apps, "Vibe.app", vibeAppPlist)
	if !a.Detected() || a.CLIMissing() || offered() {
		t.Fatalf("app only: detected %v, missing %v, offered %v", a.Detected(), a.CLIMissing(), offered())
	}
	if c, ok := a.CLI(); !ok || c.App != "1.3.0" || c.Version != "" {
		t.Fatalf("the desktop app's version: %+v %v", c, ok)
	}
	for _, running := range []bool{false, true} {
		cli = running
		if n := a.Notice(); n != noticeVibeDesktop.String() {
			t.Fatalf("with the app, vibe running %v: notice %q", running, n)
		}
	}
	cli = false
	// opened: ~/.vibe is the app's too, no leftover
	writeFile(t, filepath.Join(home, ".vibe", "config.toml"), vibeConfig)
	if !a.Detected() || a.CLIMissing() || offered() {
		t.Fatalf("app opened: detected %v, missing %v, offered %v", a.Detected(), a.CLIMissing(), offered())
	}

	// another app called Vibe is not Mistral's
	os.RemoveAll(filepath.Join(apps, "Vibe.app"))
	installApp(t, userApps, "Vibe.app", strings.Replace(vibeAppPlist, "ai.mistral.lechat.desktop", "com.example.vibe", 1))
	if a.HasApp() || a.Notice() != "" {
		t.Fatalf("another bundle id: has app %v, notice %q", a.HasApp(), a.Notice())
	}
	os.RemoveAll(filepath.Join(userApps, "Vibe.app"))
	// in ~/Applications
	installApp(t, userApps, "Vibe.app", vibeAppPlist)
	if !a.HasApp() {
		t.Fatal("the app in ~/Applications not found")
	}
	os.RemoveAll(filepath.Join(userApps, "Vibe.app"))

	// Windows: the one-click installer's le-chat-desktop\Vibe.exe
	exe := filepath.Join(apps, "le-chat-desktop", "Vibe.exe")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !a.HasApp() || a.CLIMissing() || offered() {
		t.Fatalf("with the Windows app: has %v, missing %v, offered %v", a.HasApp(), a.CLIMissing(), offered())
	}

	// Vibe in a WSL distro is not the Windows app's, nor is Avibe, whose
	// CLI is also called vibe
	if (&Agent{ID: "vibe@wsl:Ubuntu", WSL: "Ubuntu", Bin: "vibe"}).HasApp() {
		t.Fatal("WSL twin has the Windows app")
	}
	if (&Agent{ID: "avibe", Bin: "vibe"}).HasApp() {
		t.Fatal("Avibe has Vibe Desktop")
	}
}

// Installed to another folder on Windows, the app is found through the key
// its installer registers: vibe-1.3.0-setup.exe's script writes
// HKCU\Software\102a3a1f-…, the UUID v5 of ai.mistral.lechat.desktop.
func TestVibeDesktopAppRegistered(t *testing.T) {
	app := desktopApps[vibeID]
	if app.guid != "102a3a1f-fbe2-52e4-bb21-8e6b977cc08e" || app.id != "ai.mistral.lechat.desktop" {
		t.Fatalf("%+v", app)
	}
	dir := filepath.Join(t.TempDir(), "tools", "Vibe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Vibe.exe"), []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	was, wasFolders := windowsRegisteredApp, appFolders
	t.Cleanup(func() { windowsRegisteredApp, appFolders = was, wasFolders })
	appFolders = func() []string { return nil }
	windowsRegisteredApp = func(guid string) (string, string) {
		if guid == app.guid {
			return dir, "1.3.0"
		}
		return "", ""
	}
	if !app.installed() || app.version() != "1.3.0" {
		t.Fatalf("registered app: installed %v, version %q", app.installed(), app.version())
	}
}
