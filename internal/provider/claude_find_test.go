package provider

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// Claude Code installed off magpie's PATH, in ~/.local/bin as its
// installer puts it, is found for a sign-in (#839: on Windows, where it is
// claude.exe, the sign-in said "install Claude Code first").
func TestClaudeFoundInLocalBin(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(h, "AppData", "Local"))
	t.Setenv("PATH", t.TempDir())
	name := "claude"
	if runtime.GOOS == "windows" {
		name = "claude.exe"
	}
	bin := filepath.Join(h, ".local", "bin")
	os.MkdirAll(bin, 0o755)
	exe := filepath.Join(bin, name)
	testenv.Program(t, exe, "#!/bin/sh\n")
	if got := claudeExecutable(); got != exe {
		t.Fatalf("claude: %q, want %q", got, exe)
	}
	if cli, ok := findClaudeCLI(); !ok || cli.path != exe {
		t.Fatalf("sign-in's claude: %+v %v", cli, ok)
	}
}
