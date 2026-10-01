package agent

import (
	"os"
	"path/filepath"
	"testing"
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
	for _, k := range []string{
		"CLAUDE_CONFIG_DIR", "CODEX_HOME", "COPILOT_HOME", "GROK_HOME", "DSH_HOME",
		"PI_CODING_AGENT_DIR", "PI_CODING_AGENT_SESSION_DIR", "PI_CONFIG_DIR",
		"OMP_PROFILE", "PI_PROFILE", "OPENCODE_DB", "CLINE_DIR", "CLINE_DATA_DIR",
		"CLINE_SESSION_DATA_DIR", "WORKBUDDY_CONFIG_DIR",
	} {
		os.Unsetenv(k)
	}
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
