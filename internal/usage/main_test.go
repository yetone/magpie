package usage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agentenv"
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
	// The package's ledger reads what sessions finds, which asks each agent's
	// own variable before its folder in the home: one left in the shell would
	// point a test at the runner's real agent (#522).
	for _, k := range agentenv.Vars {
		os.Unsetenv(k)
	}
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
