package usage

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain gives the package a home of its own: a test that doesn't set one
// reads and writes there, never in the runner's agents or, on Windows, its
// APPDATA and LOCALAPPDATA.
func TestMain(m *testing.M) {
	home, _ := os.MkdirTemp("", "magpie-usage-test-")
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
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
