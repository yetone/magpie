package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Cindy 0.1.94's global build (com.xd.cindy) keeps its data in
// CindyGlobal, and makes the cn build's Cindy too, for the shared-*
// folders both builds write there. Cindy came first whenever it was there,
// so with the MCP router on the Library wrote [mcp_servers.magpie] into
// Cindy/codex-home/config.toml, which nothing reads, and the global
// build's Codex, reading CindyGlobal/codex-home/config.toml, had no MCP
// from magpie. It goes to the folder with Cindy's database now, and what
// magpie wrote into the other is taken out again: the file, and the
// codex-home folder magpie made, when nothing else is in them.
func TestCindyGlobalBuildMCP(t *testing.T) {
	h := sandbox(t)
	holdApp(t, h, "Cindy.app", "com.xd.cindy")
	t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	cn, global := filepath.Join(cfg, "Cindy"), filepath.Join(cfg, "CindyGlobal")
	write(t, filepath.Join(cn, "shared-system-skills", "x", "SKILL.md"), "x")
	write(t, filepath.Join(global, "cindy-local-v1.db"), "")
	want := filepath.Join(global, "codex-home", "config.toml")
	stray := filepath.Join(cn, "codex-home", "config.toml")

	tg := targetByID("cindy")
	if tg == nil || tg.MCP == nil || tg.MCP.Path != want {
		t.Fatalf("cindy's MCP file isn't %s: %+v", want, tg)
	}

	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}, Agents: []string{"cindy"}}))
	ok(t)(SetMCPHub(true))
	if got := read(t, want); !strings.Contains(got, "[mcp_servers.magpie]") || !strings.Contains(got, "/mcp/magpie/hub/cindy") {
		t.Fatalf("CindyGlobal/codex-home/config.toml:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(cn, "codex-home")); !os.IsNotExist(err) {
		t.Fatalf("Cindy/codex-home written (%v):\n%s", err, read(t, stray))
	}

	// what an earlier magpie wrote into Cindy with the router on, the
	// hub's entry (the servers it serves aren't written beside it, and
	// are no names of magpie's for Cindy now), beside one of the user's
	write(t, stray, "[mcp_servers.magpie]\nurl = \"http://127.0.0.1:3425/mcp/magpie/hub/cindy\"\n\n[mcp_servers.mine]\ncommand = \"uvx\"\n")
	ok(t)(Sync())
	if got := read(t, stray); strings.Contains(got, "mcp_servers.magpie") || !strings.Contains(got, "[mcp_servers.mine]") {
		t.Fatalf("Cindy/codex-home/config.toml after a sync:\n%s", got)
	}
	write(t, stray, "[mcp_servers.magpie]\nurl = \"http://127.0.0.1:3425/mcp/magpie/hub/cindy\"\n")
	ok(t)(Sync())
	if _, err := os.Stat(filepath.Join(cn, "codex-home")); !os.IsNotExist(err) {
		t.Errorf("Cindy/codex-home left (%v):\n%s", err, read(t, stray))
	}
	if read(t, filepath.Join(cn, "shared-system-skills", "x", "SKILL.md")) != "x" {
		t.Error("Cindy's shared-system-skills touched")
	}
	if got := read(t, want); !strings.Contains(got, "[mcp_servers.magpie]") {
		t.Errorf("CindyGlobal lost the hub's entry:\n%s", got)
	}

	// with the cn build's data in Cindy too (used less lately), its
	// Codex's file there is that build's, and stays as it is
	cnDB := filepath.Join(cn, "cindy-local-v1.db")
	write(t, cnDB, "")
	if err := os.Chtimes(cnDB, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	mine := "[mcp_servers.magpie]\nurl = \"http://127.0.0.1:3425/mcp/magpie/hub/cindy\"\n"
	write(t, stray, mine)
	ok(t)(Sync())
	if got := read(t, stray); got != mine {
		t.Errorf("the cn build's own codex-home written:\n%s", got)
	}
}
