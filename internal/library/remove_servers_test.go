package library

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/mcpauth"
	"github.com/yetone/magpie/internal/mcpauth/mcpauthtest"
)

// Every server out of the library at once (#1027), each as RemoveServer
// takes it: out of every agent and project magpie gave it to, its sign-in
// forgotten. The servers of the user's own in the same files stay, one by
// a library server's name that magpie never wrote there too, and a name
// the library hasn't is said, the rest gone all the same.
func TestRemoveServers(t *testing.T) {
	h, proj := mcpProject(t) // fs and docs, and the project's own "team"
	f := mcpauthtest.New(t)
	ok(t)(SaveServer("", Server{Name: "neon", Transport: "http", URL: f.URL, Agents: []string{"claude"}}))
	f.SignIn(t, "neon")
	ok(t)(ServerAgents("fs", []string{"claude", "codex"}))
	ok(t)(ServerAgents("docs", []string{"claude"}))
	ok(t)(ProjectServer(proj, "fs", []string{"claude"}))
	// the user's own, beside magpie's: "mine" in Claude Code's file, and
	// a "docs" of their own in Codex's, which the library never gave it
	cj := filepath.Join(h, ".claude.json")
	write(t, cj, strings.Replace(read(t, cj), `"mcpServers": {`, `"mcpServers": {"mine": {"command": "my-mcp"},`, 1))
	cfg := filepath.Join(h, ".codex/config.toml")
	write(t, cfg, read(t, cfg)+"\n[mcp_servers.docs]\ncommand = \"my-docs\"\n")
	if s := servers(t, cj, "mcpServers"); s["mine"] == nil || s["fs"] == nil || s["docs"] == nil || s["neon"] == nil {
		t.Fatalf("claude before: %v", s)
	}

	if _, err := RemoveServers(nil); err == nil {
		t.Error("no servers named was taken")
	}
	if _, err := RemoveServers([]string{"nope"}); err == nil {
		t.Error("only a server the library hasn't was taken")
	}
	res, err := RemoveServers([]string{"fs", "docs", "neon", "nope", "fs"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Unremoved) != 1 || res.Unremoved[0].What != "mcp:nope" {
		t.Errorf("unremoved: %+v", res.Unremoved)
	}
	if v, _ := Read(nil); len(v.Servers) != 0 {
		t.Errorf("left in the library: %+v", v.Servers)
	}
	if s := servers(t, cj, "mcpServers"); len(s) != 1 || s["mine"] == nil {
		t.Errorf("claude after: %v", s)
	}
	c := read(t, cfg)
	if strings.Contains(c, "[mcp_servers.fs]") || !strings.Contains(c, "[mcp_servers.docs]") || !strings.Contains(c, `"my-docs"`) {
		t.Errorf("codex after, its own docs kept:\n%s", c)
	}
	if s := servers(t, filepath.Join(proj, ".mcp.json"), "mcpServers"); len(s) != 1 || s["team"] == nil {
		t.Errorf("the project's .mcp.json after: %v", s)
	}
	if v, _ := Read(nil); len(v.Projects) != 1 || len(v.Projects[0].Wrote) != 0 {
		t.Errorf("projects: %+v", v.Projects)
	}
	if _, signed := mcpauth.Get("neon"); signed {
		t.Error("neon's sign-in was kept")
	}
}

// Some servers on or off for some agents at once (#1027): the ones named
// alone; an agent isn't given one it can't reach, and the others keep
// theirs.
func TestSomeServersAgents(t *testing.T) {
	h := sandbox(t)
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "fs", Agents: []string{"claude"}}))
	ok(t)(SaveServer("", Server{Name: "web", Transport: "sse", URL: "http://localhost:9/sse", Agents: []string{"claude"}}))
	ok(t)(SaveServer("", Server{Name: "git", Transport: "stdio", Command: "git-mcp", Agents: []string{"claude"}}))
	agents := func() map[string][]string {
		v, _ := Read(nil)
		m := map[string][]string{}
		for _, s := range v.Servers {
			m[s.Name] = s.Agents
		}
		return m
	}
	ok(t)(SomeServersAgents([]string{"fs", "web"}, []string{"codex"}, true))
	if m := agents(); !slices.Equal(m["fs"], []string{"claude", "codex"}) || !slices.Equal(m["web"], []string{"claude"}) || !slices.Equal(m["git"], []string{"claude"}) {
		t.Errorf("on for codex: %v", m)
	}
	if c := read(t, filepath.Join(h, ".codex/config.toml")); !strings.Contains(c, "[mcp_servers.fs]") || strings.Contains(c, "web") || strings.Contains(c, "git") {
		t.Errorf("codex's config:\n%s", c)
	}
	ok(t)(SomeServersAgents([]string{"fs", "web"}, []string{"claude"}, false))
	if m := agents(); !slices.Equal(m["fs"], []string{"codex"}) || len(m["web"]) != 0 || !slices.Equal(m["git"], []string{"claude"}) {
		t.Errorf("off for claude: %v", m)
	}
	if s := servers(t, filepath.Join(h, ".claude.json"), "mcpServers"); s["fs"] != nil || s["web"] != nil || s["git"] == nil {
		t.Errorf("claude's: %v", s)
	}
	if _, err := SomeServersAgents(nil, []string{"claude"}, true); err == nil {
		t.Error("no servers named was taken as every server")
	}
	if _, err := SomeServersAgents([]string{"fs"}, nil, true); err == nil {
		t.Error("no agents named was taken")
	}
	if _, err := SomeServersAgents([]string{"fs", "nope"}, []string{"codex"}, false); err == nil {
		t.Error("a server the library hasn't was taken")
	}
	if m := agents(); !slices.Equal(m["fs"], []string{"codex"}) {
		t.Errorf("a refused change wrote: %v", m)
	}
}
