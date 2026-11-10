package library

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// claudeRecord is Claude Code's record of the project at root in h's
// ~/.claude.json.
func claudeRecord(t *testing.T, h, root string) map[string]any {
	t.Helper()
	k, _ := jsonKey(filepath.ToSlash(root))
	raw, ok := edit.GetJSONRaw(filepath.Join(h, ".claude.json"), "projects."+k)
	if !ok {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func anyStrs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// scopeProject is a sandbox with servers fs (Claude Code and Codex) and
// docs (Claude Code) in the global set, erp in none, and a project in git.
func scopeProject(t *testing.T) (h, proj string) {
	h = hubSandbox(t)
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "fs"}, Agents: []string{"claude", "codex"}}))
	ok(t)(SaveServer("", Server{Name: "docs", Transport: "http", URL: "https://docs.example/mcp", Agents: []string{"claude"}}))
	ok(t)(SaveServer("", Server{Name: "erp", Transport: "stdio", Command: "erp-mcp"}))
	proj = filepath.Join(t.TempDir(), "shop")
	write(t, filepath.Join(proj, ".gitignore"), "node_modules/\n")
	gitInit(t, proj)
	ok(t)(AddProject(proj))
	return h, proj
}

// A global server turned off in one project (WhyULose0 on X: design =
// global − search): Claude Code's record of the project disables it,
// Codex's project config turns its table off, and nothing goes into the
// project's git. The user's own disabled names stay theirs.
func TestProjectServerOff(t *testing.T) {
	h, proj := scopeProject(t)
	userCodex := filepath.Join(h, ".codex", "config.toml")
	codexWas := read(t, userCodex)

	if _, err := ProjectServerOff(proj, "fs", []string{"gemini"}); err == nil {
		t.Error("an agent that can't turn one off in a project was taken")
	}
	ok(t)(ProjectServerOff(proj, "fs", []string{"claude", "codex"}))
	ok(t)(ProjectServerOff(proj, "docs", []string{"codex"}))

	rec := claudeRecord(t, h, proj)
	if rec == nil {
		t.Fatalf("no record of %s in ~/.claude.json:\n%s", proj, read(t, filepath.Join(h, ".claude.json")))
	}
	if got := anyStrs(rec["disabledMcpServers"]); !slices.Equal(got, []string{"fs"}) {
		t.Errorf("disabledMcpServers: %v", got)
	}
	if rec["hasTrustDialogAccepted"] != false || rec["mcpServers"] == nil {
		t.Errorf("record made without Claude Code's defaults: %v", rec)
	}
	pc := filepath.Join(proj, ".codex", "config.toml")
	c := read(t, pc)
	if !strings.Contains(c, "[mcp_servers.fs]") || !strings.Contains(c, "enabled = false") || strings.Contains(c, "command") {
		t.Errorf("codex project config:\n%s", c)
	}
	// docs isn't Codex's: a table for it would have no command
	if strings.Contains(c, "docs") {
		t.Errorf("codex got a table for a server it hasn't:\n%s", c)
	}
	if read(t, userCodex) != codexWas {
		t.Error("Codex's own config.toml was written")
	}
	if g := excluded(t, proj); !strings.Contains(g, "/.codex/config.toml\n") {
		t.Errorf("info/exclude:\n%s", g)
	}
	v, _ := Read(nil)
	if !slices.Equal(v.Projects[0].ServersOff["fs"], []string{"claude", "codex"}) {
		t.Errorf("view: %+v", v.Projects[0].ServersOff)
	}

	// the user's own name in the list stays, and the server comes back
	k, _ := jsonKey(filepath.ToSlash(proj))
	if err := edit.SetJSON(filepath.Join(h, ".claude.json"), edit.KV{Path: "projects." + k + ".disabledMcpServers", Value: []string{"fs", "theirs"}}); err != nil {
		t.Fatal(err)
	}
	ok(t)(ProjectServerOff(proj, "fs", nil))
	if got := anyStrs(claudeRecord(t, h, proj)["disabledMcpServers"]); !slices.Equal(got, []string{"theirs"}) {
		t.Errorf("disabledMcpServers after: %v", got)
	}
	gone(t, filepath.Join(proj, ".codex"))
	if g := excluded(t, proj); g != "" {
		t.Errorf("info/exclude after: %q", g)
	}
}

// With the router on, a project that adds a server (e-commerce = global
// + ERP) or turns one off gets its own hub entry, the project's key after
// the agent's id: the router gives that address the project's set and
// starts each server once for every project. Claude Code's entry goes in
// its record of the project, Codex's in the project's config; neither is
// in git, and the server added isn't written into .mcp.json.
func TestProjectSetThroughRouter(t *testing.T) {
	h, proj := scopeProject(t)
	ok(t)(SetMCPHub(true))
	ok(t)(ProjectServer(proj, "erp", []string{"claude", "codex"}))
	ok(t)(ProjectServerOff(proj, "docs", []string{"claude"}))
	ok(t)(ProjectServerOff(proj, "fs", []string{"codex"}))

	key := projectKey(proj)
	rec := claudeRecord(t, h, proj)
	hub, _ := rec["mcpServers"].(map[string]any)["magpie"].(map[string]any)
	if u, _ := hub["url"].(string); !strings.HasSuffix(u, gateway.HubMCPPath+"claude"+gateway.HubProjectSep+key) {
		t.Fatalf("claude's project hub entry: %v", rec["mcpServers"])
	}
	if _, err := os.Stat(filepath.Join(proj, ".mcp.json")); err == nil {
		t.Errorf("the router's server was written into .mcp.json:\n%s", read(t, filepath.Join(proj, ".mcp.json")))
	}
	c := read(t, filepath.Join(proj, ".codex", "config.toml"))
	if !strings.Contains(c, "[mcp_servers.magpie]") || !strings.Contains(c, "codex"+gateway.HubProjectSep+key) || strings.Contains(c, "erp-mcp") {
		t.Errorf("codex project config:\n%s", c)
	}
	if g := excluded(t, proj); !strings.Contains(g, "/.codex/config.toml\n") {
		t.Errorf("info/exclude:\n%s", g)
	}

	l, _ := load()
	names := func(id string) []string {
		var out []string
		for _, s := range l.hubServers(id) {
			out = append(out, s.Name)
		}
		return out
	}
	for id, want := range map[string][]string{
		"claude":                               {"docs", "fs"},
		"claude" + gateway.HubProjectSep + key: {"erp", "fs"},
		"codex":                                {"fs"},
		"codex" + gateway.HubProjectSep + key:  {"erp"},
		"claude" + gateway.HubProjectSep + "0": {"docs", "fs"},
	} {
		if got := names(id); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", id, got, want)
		}
	}
	if gateway.HubAgentOf("claude~"+key) != "claude" || gateway.HubAgentOf("claude@wsl:Ubuntu") != "claude@wsl:Ubuntu" {
		t.Error("HubAgentOf")
	}
	if got := hubNames(theHub{}.Tools(context.Background(), "gemini"+gateway.HubProjectSep+key)); len(got) != 0 {
		t.Errorf("gemini, given none, gets %v in the project", got)
	}

	// back to the global set: the project's entries go
	ok(t)(ProjectServer(proj, "erp", nil))
	ok(t)(ProjectServerOff(proj, "docs", nil))
	ok(t)(ProjectServerOff(proj, "fs", nil))
	if rec := claudeRecord(t, h, proj); rec != nil && rec["mcpServers"].(map[string]any)["magpie"] != nil {
		t.Errorf("claude's project hub entry stayed: %v", rec)
	}
	gone(t, filepath.Join(proj, ".codex"))
}

// A global skill turned off in one project for Claude Code (novel ≠
// coding search) goes in the project's .claude/settings.local.json, which
// Claude Code keeps out of git; a value the user set there stays theirs.
func TestProjectSkillOff(t *testing.T) {
	_, proj := projectLib(t)
	ok(t)(SkillAgents("pdf", []string{"claude", "codex"}))
	if _, err := ProjectSkillsOff(proj, []string{"pdf"}, []string{"codex"}); err == nil {
		t.Error("codex was taken")
	}
	ok(t)(ProjectSkillsOff(proj, []string{"pdf", "docx"}, []string{"claude"}))
	f := filepath.Join(proj, ".claude", "settings.local.json")
	if got := servers(t, f, "skillOverrides"); got["pdf"] != "off" || got["docx"] != "off" {
		t.Errorf("skillOverrides: %v", got)
	}
	if g := excluded(t, proj); !strings.Contains(g, "/.claude/settings.local.json\n") {
		t.Errorf("info/exclude:\n%s", g)
	}
	// the user's own value for docx is theirs from then on
	write(t, f, `{"skillOverrides": {"pdf": "off", "docx": "user-invocable-only"}, "theirs": 1}`)
	ok(t)(ProjectSkillsOff(proj, []string{"pdf", "docx"}, nil))
	if got := servers(t, f, "skillOverrides"); got["pdf"] != nil || got["docx"] != "user-invocable-only" {
		t.Errorf("skillOverrides after: %v", got)
	}

	// a file magpie made, with nothing else in it, goes
	os.Remove(f)
	ok(t)(ProjectSkillsOff(proj, []string{"pdf"}, []string{"claude"}))
	ok(t)(ProjectSkillsOff(proj, []string{"pdf"}, nil))
	gone(t, f)
}

// A new git worktree follows the main project's set (WhyULose0 on X):
// the same skills and servers in the project's folder in it, kept out of
// git by the repository's one info/exclude; a worktree removed is let go.
func TestProjectWorktreeFollows(t *testing.T) {
	_, proj := scopeProject(t)
	src := filepath.Join(t.TempDir(), "src")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	ok(t)(InstallSkills(src, []string{"pdf"}, nil))
	ok(t)(ProjectSkill(proj, "pdf", []string{"claude"}))
	ok(t)(ProjectServer(proj, "erp", []string{"cursor"}))

	// git worktree add ../shop-wt
	wt := filepath.Join(filepath.Dir(proj), "shop-wt")
	own := filepath.Join(proj, ".git", "worktrees", "shop-wt")
	write(t, filepath.Join(wt, ".git"), "gitdir: "+own+"\n")
	write(t, filepath.Join(own, "gitdir"), filepath.Join(wt, ".git")+"\n")
	write(t, filepath.Join(own, "commondir"), "../..\n")
	l, _ := load()
	if !l.treesChanged() {
		t.Fatal("a new worktree isn't noticed")
	}
	keepProjectTrees()
	if p := filepath.Join(wt, ".claude", "skills", "pdf"); !isLink(t, p) {
		t.Errorf("the worktree hasn't the project's skill")
	}
	if s := servers(t, filepath.Join(wt, ".cursor", "mcp.json"), "mcpServers"); s["erp"] == nil {
		t.Errorf("the worktree hasn't the project's server: %v", s)
	}
	if g := excluded(t, proj); strings.Count(g, "/.claude/skills/pdf\n") != 1 || !strings.Contains(g, "/.cursor/mcp.json\n") {
		t.Errorf("info/exclude:\n%s", g)
	}
	v, _ := Read(nil)
	if !slices.Equal(v.Projects[0].Trees, []string{wt}) {
		t.Errorf("trees: %v", v.Projects[0].Trees)
	}
	if l, _ = load(); l.treesChanged() {
		t.Error("synced again with nothing changed")
	}

	// a change to the project reaches the worktree
	ok(t)(ProjectSkill(proj, "pdf", nil))
	gone(t, filepath.Join(wt, ".claude", "skills", "pdf"))

	// git worktree remove: magpie lets it go
	os.RemoveAll(wt)
	os.RemoveAll(own)
	if l, _ = load(); !l.treesChanged() {
		t.Fatal("a worktree removed isn't noticed")
	}
	keepProjectTrees()
	if v, _ := Read(nil); len(v.Projects[0].Trees) != 0 {
		t.Errorf("trees after: %v", v.Projects[0].Trees)
	}
}
