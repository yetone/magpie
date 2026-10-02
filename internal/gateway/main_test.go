package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agentenv"
)

func TestMain(m *testing.M) {
	code, err := isolatedTests(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}

// Isolate every process, including the cold-start redaction tests. HOME
// alone does not isolate the macOS Keychain, and account caches eventually
// expire: discovery must keep finding only the test's sign-ins and CLIs.
func isolatedTests(m *testing.M) (int, error) {
	home, err := os.MkdirTemp("", "magpie-gateway-test-")
	if err != nil {
		return 1, err
	}
	defer os.RemoveAll(home)
	for name, value := range map[string]string{
		"HOME": home, "USERPROFILE": home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_CACHE_HOME":  filepath.Join(home, ".cache"),
		"XDG_DATA_HOME":   filepath.Join(home, ".local", "share"),
		"APPDATA":         filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA":    filepath.Join(home, "AppData", "Local"),
	} {
		if err := os.Setenv(name, value); err != nil {
			return 1, err
		}
	}
	for _, name := range agentenv.Vars {
		if err := os.Unsetenv(name); err != nil {
			return 1, err
		}
	}
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		return 1, err
	}
	// Finding an inert CLI also prevents discovery from falling back to
	// one installed at an absolute system path. Tests can prepend their
	// own fakes, as fakeClaude does, and still use ordinary shell tools.
	for _, name := range []string{"security", "secret-tool", "claude", "codex", "cursor-agent", "devin", "grok", "kiro-cli"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			return 1, err
		}
	}
	if err := os.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		return 1, err
	}
	// no route is written to disk behind a test's back; the history's own
	// tests call saveRoute themselves
	keepRoutes = false
	return m.Run(), nil
}
