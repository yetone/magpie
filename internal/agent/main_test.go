package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agentenv"
)

// TestMain gives the package a home of its own. A home an agent is found
// through can sit outside HOME — DSH_HOME does — and the package's tests
// sandbox HOME alone: left as the developer has it, a test that picks a model
// writes it into the real ~/.dsh/profiles, and dsh then refuses every turn
// with a model its provider does not list. internal/gateway, internal/provider
// and internal/usage isolate themselves the same way.
func TestMain(m *testing.M) {
	home, _ := os.MkdirTemp("", "magpie-agent-test-")
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
	for _, k := range agentenv.Vars {
		os.Unsetenv(k)
	}
	// whether Codex's ChatGPT account is out of its allowance is asked of
	// OpenAI; never from here
	codexUsedUp = func() bool { return false }
	// Sandboxed config writes must never write into the real OS keychain.
	zedCredential = func(string) error { return nil }
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
