package cli

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/library"
)

// magpie library mcp off|on <name> (swang20201 on X): off takes the
// server from its agents and keeps it; on gives it back to them; a flag
// is never taken as a name.
func TestLibraryMCPOffOn(t *testing.T) {
	h := t.TempDir()
	for k, v := range map[string]string{"HOME": h, "USERPROFILE": h, "XDG_CONFIG_HOME": filepath.Join(h, ".config"), "CLAUDE_CONFIG_DIR": "", "CODEX_HOME": "", "APPDATA": "", "LOCALAPPDATA": ""} {
		t.Setenv(k, v)
	}
	if err := os.MkdirAll(filepath.Join(h, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h, ".claude/settings.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := library.SaveServer("", library.Server{Name: "fs", Transport: "stdio", Command: "npx", Env: map[string]string{"K": "v"}, Agents: []string{"claude"}}); err != nil {
		t.Fatal(err)
	}
	server := func() library.ServerView {
		t.Helper()
		v, err := library.Read(nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range v.Servers {
			if s.Name == "fs" {
				return s
			}
		}
		t.Fatal("fs is gone")
		return library.ServerView{}
	}

	if err := libraryCmd([]string{"library", "mcp", "off", "fs"}); err != nil {
		t.Fatal(err)
	}
	if s := server(); !s.Hidden || len(s.Agents) != 0 || !slices.Equal(s.Paused, []string{"claude"}) || s.Env["K"] != "v" {
		t.Fatalf("off: %+v", s.Server)
	}
	if err := libraryCmd([]string{"library", "mcp", "on", "--help"}); err == nil {
		t.Error("a flag was taken as a server's name")
	}
	if err := libraryCmd([]string{"library", "mcp", "on", "fs"}); err != nil {
		t.Fatal(err)
	}
	if s := server(); s.Hidden || !slices.Equal(s.Agents, []string{"claude"}) || s.Env["K"] != "v" {
		t.Fatalf("on: %+v", s.Server)
	}
}
