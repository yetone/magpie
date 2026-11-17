package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// DS Bot's own DSH runs on its data folder (~/.ds-bot by default) with the
// profile ds-bot: the Library gives it MCP servers in that profile's patch
// list, its instructions in AGENTS.md there and its skills in its skills
// folder, as dsh's are given in ~/.dsh, and nothing of it reaches ~/.dsh
// (sunset on Discord: DS Bot was in no picker of the Library's MCP page).
func TestDsbotLibraryTarget(t *testing.T) {
	h := sandbox(t)
	home := filepath.Join(h, ".ds-bot")
	p := filepath.Join(home, "profiles", "ds-bot", "cordis.patch.yml")
	dshP := filepath.Join(h, ".dsh", "profiles", "web", "cordis.patch.yml")
	const tmpl = "# Your patch layer for this dsh profile, applied after every bundle layer:\n[]\n"
	write(t, p, tmpl)
	write(t, dshP, tmpl)
	tg := targetByID("ds-bot")
	if tg == nil {
		t.Fatal("ds-bot: no target")
	}
	if tg.MCP == nil || tg.MCP.Path != p || tg.MCP.Format != fmtDsh {
		t.Fatalf("ds-bot MCP: %+v", tg.MCP)
	}
	if tg.Instructions != filepath.Join(home, "AGENTS.md") || tg.Skills != filepath.Join(home, "skills") {
		t.Fatalf("ds-bot: instructions %q skills %q", tg.Instructions, tg.Skills)
	}
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}, Agents: []string{"ds-bot"}}))
	s := read(t, p)
	for _, want := range []string{"- insert: # magpie\n    - id: magpie-mcp-fs\n", "name: \"@deepseek-ai/dsh-mcp-client\"", `serverName: "fs"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in\n%s", want, s)
		}
	}
	if got := read(t, dshP); got != tmpl {
		t.Fatalf("dsh's profile written for DS Bot:\n%s", got)
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, a := range v.Agents {
		if a.ID == "ds-bot" {
			listed = a.MCP == p && a.Instructions != "" && a.Skills != ""
		}
	}
	if !listed {
		t.Fatal("ds-bot: not on the Library's pages")
	}
	for _, sv := range v.Servers {
		if sv.Name == "fs" && (len(sv.Agents) != 1 || sv.Agents[0] != "ds-bot") {
			t.Fatalf("fs is on %v", sv.Agents)
		}
	}
}

// A DSH_HOME naming DS Bot's folder (#5624) doesn't make that folder dsh's
// in the Library too: DS Bot's target has it, and dsh none, so no two
// agents' rows are written into one patch list.
func TestDshNotOnDsbotHome(t *testing.T) {
	h := sandbox(t)
	home := filepath.Join(h, ".ds-bot")
	write(t, filepath.Join(home, "profiles", "ds-bot", "cordis.patch.yml"), "[]\n")
	t.Setenv("DSH_HOME", home)
	if err := os.MkdirAll(filepath.Join(h, ".dsh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if targetByID("ds-bot") == nil {
		t.Fatal("ds-bot: no target")
	}
	if tg := targetByID("dsh"); tg != nil {
		t.Fatalf("dsh given DS Bot's folder: %+v", tg.MCP)
	}
}
