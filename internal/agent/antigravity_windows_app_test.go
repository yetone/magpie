package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// Antigravity's Windows app is found by what its installer registered, with
// none of ~/.gemini/antigravity a fresh install hasn't written (JJasonsun
// on Discord: agy found on Windows, the app not). The registry values are a
// real 2.30.0 install's on the Windows box: HKCU\Software\<guid>'s
// InstallLocation, and its Uninstall entry's DisplayIcon and DisplayVersion
// with no InstallLocation.
func TestAntigravityWindowsAppRegistered(t *testing.T) {
	home := antigravityHome(t)
	app := desktopApps["antigravity"]
	const guid = "121a0be4-63bd-531e-acf8-fc3924c7e984"
	if app.guid != guid {
		t.Fatalf("guid %q", app.guid)
	}
	programs := filepath.Join(t.TempDir(), "Programs")
	at := filepath.Join(programs, "antigravity")
	if err := os.MkdirAll(at, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(at, "Antigravity.exe"), []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	version := "1.11.3"
	real := func(hive, key, name string) string {
		if hive != "HKCU" {
			return ""
		}
		switch key + "|" + name {
		case `Software\` + guid + "|InstallLocation":
			return at
		case `Software\Microsoft\Windows\CurrentVersion\Uninstall\` + guid + "|DisplayIcon":
			return filepath.Join(at, "Antigravity.exe") + ",0"
		case `Software\Microsoft\Windows\CurrentVersion\Uninstall\` + guid + "|DisplayVersion":
			return version
		}
		return ""
	}
	wasReg, wasFolders := windowsRegisteredApp, appFolders
	t.Cleanup(func() { windowsRegisteredApp, appFolders = wasReg, wasFolders })
	windowsRegisteredApp = func(g string) (string, string) { return registeredIn(g, real) }
	appFolders = func() []string { return nil }

	// one before 2.15, which takes no gateway, isn't it
	if antigravity(home).Detected() {
		t.Fatal("an app before 2.15 taken for it")
	}
	version = "2.30.0"
	a := antigravity(home)
	if v := (&Agent{ID: "antigravity"}).AppVersion(); v != "2.30.0" {
		t.Fatalf("AppVersion %q", v)
	}
	if !a.Detected() {
		t.Fatal("the registered app isn't detected")
	}
	if err := a.Field("model").Set("magpie/anth/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	if f := newAgyFile(filepath.Join(home, ".gemini", "config", "config.json")); !f.wired() {
		t.Fatalf("config.json not on magpie:\n%s", readFile(f.path))
	}

	// the installer's default folder, where no registry says
	windowsRegisteredApp = func(string) (string, string) { return "", "" }
	appFolders = func() []string { return []string{programs} }
	if !(&Agent{ID: "antigravity"}).HasApp() {
		t.Fatal("not found under %LOCALAPPDATA%\\Programs")
	}
}
