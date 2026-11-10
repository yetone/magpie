package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// antigravity is a sandbox home where Antigravity's CLI is set up, its
// ~/.gemini/config the folder it keeps its customizations in.
func antigravity(t *testing.T) (home, cfg string) {
	t.Helper()
	h := sandbox(t)
	write(t, filepath.Join(h, ".gemini/antigravity-cli/settings.json"), "{}")
	return h, filepath.Join(h, ".gemini", "config")
}

// #277: Antigravity is a target, reading each from ~/.gemini/config.
func TestAntigravityTarget(t *testing.T) {
	_, cfg := antigravity(t)
	tg := targetByID("agy")
	if tg == nil {
		t.Fatalf("agy isn't a target: %v", ids(Targets()))
	}
	if tg.Instructions != filepath.Join(cfg, "GEMINI.md") || tg.MCP == nil ||
		tg.MCP.Path != filepath.Join(cfg, "mcp_config.json") || tg.Skills != filepath.Join(cfg, "skills") {
		t.Errorf("target: %+v %+v", tg, tg.MCP)
	}
	for _, kind := range []string{"instructions", "mcp", "skills"} {
		if id, err := Takes("antigravity-cli", kind); id != "agy" || err != nil {
			t.Errorf("%s: %q %v", kind, id, err)
		}
	}
	if ProjectSkillsDir("agy") != ".agents/skills" {
		t.Errorf("project skills: %q", ProjectSkillsDir("agy"))
	}
}

// Servers are written as agy's own `mcp add` writes them — a remote one's
// serverUrl, never url or httpUrl — into the empty file Antigravity leaves,
// keeping what the user added to an entry and their own servers.
func TestAntigravityMCP(t *testing.T) {
	_, cfg := antigravity(t)
	p := filepath.Join(cfg, "mcp_config.json")
	write(t, p, "")
	agents := []string{"agy"}
	stdio := Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}, Env: map[string]string{"K": "V"}, Agents: agents}
	web := Server{Name: "web", Transport: "http", URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer x"}, Agents: agents}
	sse := Server{Name: "old", Transport: "sse", URL: "https://example.com/sse", Agents: agents}
	for _, s := range []Server{stdio, web, sse} {
		ok(t)(SaveServer("", s))
	}

	var f struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(read(t, p)), &f); err != nil {
		t.Fatalf("%v:\n%s", err, read(t, p))
	}
	w := f.MCPServers["web"]
	if w["serverUrl"] != "https://example.com/mcp" || w["url"] != nil || w["httpUrl"] != nil || w["type"] != nil {
		t.Errorf("web: %v", w)
	}
	if h, _ := w["headers"].(map[string]any); h["Authorization"] != "Bearer x" {
		t.Errorf("web headers: %v", w)
	}
	if o := f.MCPServers["old"]; o["serverUrl"] != "https://example.com/sse" || o["type"] != "sse" {
		t.Errorf("sse: %v", o)
	}
	if fs := f.MCPServers["fs"]; fs["command"] != "npx" || fs["serverUrl"] != nil {
		t.Errorf("fs: %v", fs)
	}
	got, err := targetByID("agy").MCP.read()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []Server{stdio, web, sse} {
		if s := got[want.Name]; s == nil || !s.same(&want) {
			t.Errorf("%s read back as %+v", want.Name, s)
		}
	}

	// agy's own rewrite (`agy mcp disable`) adds "disabled" and drops an
	// empty args or env; the server is still the library's, as it was
	write(t, p, `{"mcpServers": {"mine": {"url": "https://mine.example/mcp"},
		"web": {"disabled": true, "headers": {"Authorization": "Bearer x"}, "serverUrl": "https://example.com/mcp"},
		"fs": {"args": ["-y", "@mcp/fs"], "command": "npx", "env": {"K": "V"}},
		"old": {"serverUrl": "https://example.com/sse", "type": "sse"}}}`)
	before := read(t, p)
	ok(t)(Sync())
	if read(t, p) != before {
		t.Errorf("a sync rewrote servers that are as the library has them:\n%s", read(t, p))
	}
	ok(t)(SaveServer("web", Server{Name: "web", Transport: "http", URL: "https://example.com/v2", Agents: agents}))
	json.Unmarshal([]byte(read(t, p)), &f)
	if w := f.MCPServers["web"]; w["disabled"] != true || w["serverUrl"] != "https://example.com/v2" {
		t.Errorf("web after edit: %v", w)
	}

	for _, s := range []string{"fs", "web", "old"} {
		ok(t)(RemoveServer(s))
	}
	got, _ = targetByID("agy").MCP.read()
	if len(got) != 1 || got["mine"] == nil || got["mine"].URL != "https://mine.example/mcp" {
		t.Errorf("after: %+v\n%s", got, read(t, p))
	}
}

// Instructions go into ~/.gemini/config/GEMINI.md, which Antigravity alone
// reads, not Gemini CLI's ~/.gemini/GEMINI.md; skills into its skills.
func TestAntigravityInstructionsAndSkills(t *testing.T) {
	h, cfg := antigravity(t)
	shared := "Use tabs."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"agy"}}))
	if s := read(t, filepath.Join(cfg, "GEMINI.md")); s != blockBegin+"\nUse tabs.\n"+blockEnd+"\n" {
		t.Errorf("~/.gemini/config/GEMINI.md:\n%q", s)
	}
	if s := read(t, filepath.Join(h, ".gemini", "GEMINI.md")); s != "" {
		t.Errorf("Gemini CLI's GEMINI.md was written:\n%q", s)
	}

	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"agy"}))
	if _, err := os.Stat(filepath.Join(cfg, "skills", "pdf", "SKILL.md")); err != nil {
		t.Error(err)
	}
	ok(t)(RemoveSkill("pdf"))
	if _, err := os.Lstat(filepath.Join(cfg, "skills", "pdf")); !os.IsNotExist(err) {
		t.Error("the skill is still there")
	}
}

// xuanwo on X: the Antigravity app, an agent of its own beside agy, is a
// target too, reading the same ~/.gemini/config (antigravity.google/docs/
// skills, /mcp: the App and the CLI share it).
func TestAntigravityAppTarget(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".gemini/antigravity/agyhub_summaries_proto.pb"), "x")
	cfg := filepath.Join(h, ".gemini", "config")
	a, err := agent.Find("antigravity")
	if err != nil {
		t.Fatal(err)
	}
	tg := targetOf(a)
	if tg == nil || tg.MCP == nil || tg.MCP.Path != filepath.Join(cfg, "mcp_config.json") ||
		tg.Skills != filepath.Join(cfg, "skills") || tg.Instructions != filepath.Join(cfg, "GEMINI.md") {
		t.Fatalf("target: %+v", tg)
	}
	if ProjectSkillsDir("antigravity") != ".agents/skills" {
		t.Errorf("project skills: %q", ProjectSkillsDir("antigravity"))
	}
}
