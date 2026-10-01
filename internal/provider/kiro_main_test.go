package provider

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain keeps the package's tests off the Kiro sign-ins of whoever runs
// them: Accounts() reads them, and asks Kiro who the account is. It also
// gives the package a home of its own, so what a test leaves behind it —
// a CLI's answer kept by refresh after the test is over — never lands in
// the runner's magpie, nor in APPDATA on Windows.
func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "magpie-kiro")
	home := filepath.Join(dir, "home")
	os.MkdirAll(home, 0o755)
	for k, v := range map[string]string{
		"HOME": home, "USERPROFILE": home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_CACHE_HOME":  filepath.Join(home, ".cache"),
		"XDG_DATA_HOME":   filepath.Join(home, ".local", "share"),
		"APPDATA":         filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA":    filepath.Join(home, "AppData", "Local"),
	} {
		os.Setenv(k, v)
	}
	kiroCLIDB = func() string { return filepath.Join(dir, "kiro-cli", "data.sqlite3") }
	kiroIDEDir = func() string { return filepath.Join(dir, "sso") }
	askKiroIdentity = func(string, string) (string, string) { return "", "" }
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
