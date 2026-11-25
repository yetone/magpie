package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// plantApp puts an app bundle named name, with bundle id id, in dir: an
// app installed on a Mac.
func plantApp(t *testing.T, dir, name, id string) {
	t.Helper()
	contents := filepath.Join(dir, name, "Contents")
	if err := os.MkdirAll(contents, 0o755); err != nil {
		t.Fatal(err)
	}
	plist := "<plist><dict><key>CFBundleIdentifier</key><string>" + id +
		"</string><key>CFBundleShortVersionString</key><string>1.0.0</string></dict></plist>"
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Alex on Discord (11-25): Cindy uninstalled, its row still said magpie
// was added, with nothing to take it off. An app magpie is added to by an
// import link is found by its data folder, which an uninstall leaves, its
// database with magpie in it too. On a Mac the app is gone with its
// bundle; another vendor's app of the same name isn't it. Elsewhere the
// folder is taken as it was.
func TestImportAppRemovedFromTheMac(t *testing.T) {
	for _, c := range []struct {
		id, app, bundle string
		agent           func(home string) *Agent
		data            func(cfg string) string
	}{
		{"cindy", "Cindy.app", "com.xd.cindycn", func(string) *Agent { return cindy() }, func(cfg string) string { return filepath.Join(cfg, "Cindy") }},
		{"cindy", "Cindy.app", "com.xd.cindy", func(string) *Agent { return cindy() }, func(cfg string) string { return filepath.Join(cfg, "CindyGlobal") }},
		{"aionui", "AionUi.app", "com.aionui.app", func(string) *Agent { return aionui() }, func(cfg string) string { return filepath.Join(cfg, "AionUi", "aionui") }},
		{"cherrystudio", "Cherry Studio.app", "com.kangfenmao.CherryStudio", cherryStudio, func(cfg string) string { return filepath.Join(cfg, "CherryStudio", "Data") }},
	} {
		t.Run(c.bundle, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
			apps := filepath.Join(home, "Applications")
			wasFolders, wasSpot, wasOS := appFolders, spotlightApps, removedOS
			appFolders = func() []string { return []string{apps} }
			spotlightApps = func(string) []string { return nil }
			t.Cleanup(func() { appFolders, spotlightApps, removedOS = wasFolders, wasSpot, wasOS })
			cfg, err := os.UserConfigDir()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(c.data(cfg), 0o700); err != nil {
				t.Fatal(err)
			}
			removedOS = "darwin"
			if c.agent(home).detect() {
				t.Error("its data folder left behind, no app: detected")
			}
			plantApp(t, apps, c.app, "com.example.other")
			if c.agent(home).detect() {
				t.Error("another vendor's app of the same name taken for it")
			}
			plantApp(t, apps, c.app, c.bundle)
			if !c.agent(home).detect() {
				t.Error("installed, yet not detected")
			}
			os.RemoveAll(filepath.Join(apps, c.app))
			for _, goos := range []string{"windows", "linux"} {
				removedOS = goos
				if !c.agent(home).detect() {
					t.Errorf("on %s its data folder no longer detects it", goos)
				}
			}
		})
	}
}
