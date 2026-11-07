package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/yetone/magpie/internal/testenv"
)

// An app's binary by full path, with spaces in it, typed as it is
// (zachzhao1984 on Discord: "/Applications/Agent HUD.app/Contents/MacOS/
// Agent HUD --mcp"). A command line split at each space made it the
// program /Applications/Agent and three arguments; it is the program
// with its spaces, and --mcp.
func spacedApp(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "Agent HUD.app", "Contents", "MacOS", "Agent HUD")
	testenv.Program(t, exe, "#!/bin/sh\n")
	return exe
}

func TestSpacedProgram(t *testing.T) {
	exe := spacedApp(t)
	split := strings.Fields(exe + " --mcp")
	for _, c := range []struct {
		name       string
		cmd        string
		args, want []string
	}{
		{"split at its spaces", split[0], split[1:], []string{exe, "--mcp"}},
		{"with no arguments", split[0], split[1 : len(split)-1], []string{exe}},
		{"given whole", exe, []string{"--mcp"}, []string{exe, "--mcp"}},
		{"a bare name", "npx", []string{"-y", "foo"}, []string{"npx", "-y", "foo"}},
		{"a path no join makes", "/nowhere/x", []string{"y", "--z"}, []string{"/nowhere/x", "y", "--z"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			cmd, args := spacedProgram(c.cmd, c.args)
			if got := append([]string{cmd}, args...); !slices.Equal(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
	// a program that is there, given the words after it, keeps them
	dir := t.TempDir()
	short := filepath.Join(dir, "srv")
	testenv.Program(t, short, "#!/bin/sh\n")
	testenv.Program(t, short+" more", "#!/bin/sh\n")
	if cmd, args := spacedProgram(short, []string{"more"}); cmd != short || !slices.Equal(args, []string{"more"}) {
		t.Fatalf("a program that is there: %q %q", cmd, args)
	}
	// a folder by that name is no program
	if runtime.GOOS != "windows" {
		if err := os.MkdirAll(filepath.Join(dir, "a b"), 0o755); err != nil {
			t.Fatal(err)
		}
		if cmd, _ := spacedProgram(filepath.Join(dir, "a"), []string{"b"}); cmd != filepath.Join(dir, "a") {
			t.Fatalf("a folder taken for the program: %q", cmd)
		}
	}
}

// Saved from the GUI's words, the CLI's or the TUI's, the program is one
// argument with its spaces in the library and in each agent's file,
// Claude Code's JSON and Codex's TOML.
func TestSpacedProgramSaved(t *testing.T) {
	h := sandbox(t)
	exe := spacedApp(t)
	s, err := ServerOf("hud", strings.Fields(exe+" --mcp"))
	if err != nil {
		t.Fatal(err)
	}
	s.Agents = []string{"claude", "codex"}
	ok(t)(SaveServer("", s))
	if got := serverIn(t, "hud"); got == nil || got.Command != exe || !slices.Equal(got.Args, []string{"--mcp"}) {
		t.Fatalf("library has %+v", got)
	}
	var claude struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(read(t, filepath.Join(h, ".claude.json"))), &claude); err != nil {
		t.Fatal(err)
	}
	if e := claude.MCPServers["hud"]; e.Command != exe || !slices.Equal(e.Args, []string{"--mcp"}) {
		t.Fatalf("Claude Code is given %+v", e)
	}
	var codex struct {
		MCPServers map[string]struct {
			Command string   `toml:"command"`
			Args    []string `toml:"args"`
		} `toml:"mcp_servers"`
	}
	if err := toml.Unmarshal([]byte(read(t, filepath.Join(h, ".codex", "config.toml"))), &codex); err != nil {
		t.Fatal(err)
	}
	if e := codex.MCPServers["hud"]; e.Command != exe || !slices.Equal(e.Args, []string{"--mcp"}) {
		t.Fatalf("Codex is given %+v", e)
	}
}
