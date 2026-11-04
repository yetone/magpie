package library

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// eirc on Discord: Qoder and other agents could be given skills and MCP
// servers only user-wide. Each now gets a project's in the folder or file
// it reads them from there.
func TestProjectSkillsMoreAgents(t *testing.T) {
	_, proj := projectLib(t)
	ok(t)(ProjectSkill(proj, "pdf", []string{"qoder", "qoder-cn", "codebuddy", "commandcode", "droid", "cline"}))
	for _, e := range []string{".qoder/skills/pdf", ".codebuddy/skills/pdf", ".commandcode/skills/pdf", ".agents/skills/pdf"} {
		p := filepath.Join(proj, e)
		if !isLink(t, p) || !ours(p, "pdf") {
			t.Errorf("%s isn't a link to the library's", e)
		}
		if _, err := os.Stat(filepath.Join(p, "SKILL.md")); err != nil {
			t.Errorf("%s: %v", e, err)
		}
	}
	g := read(t, filepath.Join(proj, ".gitignore"))
	for _, e := range []string{"/.qoder/skills/pdf", "/.codebuddy/skills/pdf", "/.commandcode/skills/pdf", "/.agents/skills/pdf"} {
		if !strings.Contains(g, e+"\n") {
			t.Errorf(".gitignore lacks %s:\n%s", e, g)
		}
	}
	// Qoder CN shares Qoder's project folder: one of the two taken away
	// leaves it for the other
	ok(t)(ProjectSkill(proj, "pdf", []string{"qoder-cn"}))
	if !isLink(t, filepath.Join(proj, ".qoder/skills/pdf")) {
		t.Error(".qoder/skills/pdf went while Qoder CN still has it")
	}
	gone(t, filepath.Join(proj, ".codebuddy"))
	ok(t)(RemoveProject(proj, false))
	gone(t, filepath.Join(proj, ".qoder"))
	if g := read(t, filepath.Join(proj, ".gitignore")); g != "node_modules/\n" {
		t.Errorf(".gitignore after: %q", g)
	}
}

// Qoder's project servers are .qoder/settings.json's mcpServers, in the
// shape its user-wide settings.json has them (type beside a url, none on
// a command); CodeBuddy's the project's .mcp.json, as Claude Code's;
// Droid's .factory/mcp.json. What the user has in each stays.
func TestProjectServersQoderCodeBuddyDroid(t *testing.T) {
	h, proj := mcpProject(t)
	qs := filepath.Join(proj, ".qoder", "settings.json")
	write(t, qs, `{
  "permissions": {"allow": ["Bash(npm test)"]},
  "mcpServers": {"linear": {"type": "http", "url": "https://mcp.linear.app/mcp"}}
}
`)
	ok(t)(SaveServer("", Server{Name: "events", Transport: "sse", URL: "https://x.example/sse"}))
	for _, name := range []string{"fs", "docs", "events"} {
		ok(t)(ProjectServer(proj, name, []string{"qoder", "codebuddy", "droid"}))
	}

	doc := jsonOf(t, qs)
	if p, _ := doc["permissions"].(map[string]any); p == nil {
		t.Errorf("Qoder's permissions lost:\n%s", read(t, qs))
	}
	s, _ := doc["mcpServers"].(map[string]any)
	if l, _ := s["linear"].(map[string]any); l["url"] != "https://mcp.linear.app/mcp" {
		t.Errorf("the user's server lost: %v", s)
	}
	if fs, _ := s["fs"].(map[string]any); fs["command"] != "npx" || fs["type"] != nil {
		t.Errorf("qoder fs: %v", fs)
	}
	if d, _ := s["docs"].(map[string]any); d["type"] != "http" || d["url"] != "https://docs.example/mcp" {
		t.Errorf("qoder docs: %v", d)
	}
	if e, _ := s["events"].(map[string]any); e["type"] != "sse" {
		t.Errorf("qoder events: %v", e)
	}

	m := servers(t, filepath.Join(proj, ".mcp.json"), "mcpServers")
	if m["team"] == nil || m["fs"] == nil || m["docs"] == nil || m["events"] == nil {
		t.Errorf("codebuddy .mcp.json: %v", m)
	}
	f := filepath.Join(proj, ".factory", "mcp.json")
	if d := servers(t, f, "mcpServers"); d["fs"].(map[string]any)["type"] != "stdio" || d["docs"].(map[string]any)["type"] != "http" || d["events"] == nil {
		t.Errorf("droid: %v", d)
	}
	g := read(t, filepath.Join(proj, ".gitignore"))
	if !strings.Contains(g, "/.factory/mcp.json\n") || strings.Contains(g, ".qoder") || strings.Contains(g, "/.mcp.json") {
		t.Errorf(".gitignore:\n%s", g)
	}
	if _, err := os.Stat(filepath.Join(h, ".qoder", "settings.json")); err == nil {
		t.Error("Qoder's user-wide settings were written")
	}

	ok(t)(RemoveProject(proj, false))
	if s, _ := jsonOf(t, qs)["mcpServers"].(map[string]any); len(s) != 1 || s["linear"] == nil {
		t.Errorf("qoder after: %s", read(t, qs))
	}
	gone(t, filepath.Join(proj, ".factory"))
	if ProjectMCPFile("qoder") != ".qoder/settings.json" || ProjectMCPFile("qoder-cn") != ".qoder/settings.json" || ProjectMCPFile("codebuddy") != ".mcp.json" || ProjectMCPFile("droid") != ".factory/mcp.json" {
		t.Error("ProjectMCPFile")
	}
	if ProjectSkillsDir("qoder") != ".qoder/skills" || ProjectSkillsDir("cline") != ".agents/skills" || ProjectSkillsDir("droid") != ".agents/skills" {
		t.Error("ProjectSkillsDir")
	}
}

// CodeBuddy reads only the first there is of .mcp.json and mcp.json: a
// project with mcp.json alone has its servers put there, so a new
// .mcp.json doesn't hide the user's from it.
func TestProjectServersCodeBuddyMcpJSON(t *testing.T) {
	_, proj := mcpProject(t)
	os.Remove(filepath.Join(proj, ".mcp.json"))
	write(t, filepath.Join(proj, "mcp.json"), `{"mcpServers": {"team": {"command": "team-mcp"}}}`)
	ok(t)(ProjectServer(proj, "fs", []string{"codebuddy"}))
	if _, err := os.Stat(filepath.Join(proj, ".mcp.json")); err == nil {
		t.Errorf(".mcp.json made:\n%s", read(t, filepath.Join(proj, ".mcp.json")))
	}
	if s := servers(t, filepath.Join(proj, "mcp.json"), "mcpServers"); s["team"] == nil || s["fs"] == nil {
		t.Errorf("mcp.json: %v", s)
	}
	if v, _ := Read(nil); !slices.Equal(v.Projects[0].Wrote["mcp.json"], []string{"fs"}) {
		t.Errorf("wrote: %v", v.Projects[0].Wrote)
	}
	ok(t)(ProjectServer(proj, "fs", nil))
	if s := servers(t, filepath.Join(proj, "mcp.json"), "mcpServers"); len(s) != 1 || s["team"] == nil {
		t.Errorf("after: %v", s)
	}
}

// xingchen_c on Discord: DeepSeek Harness couldn't be given a project's
// skills. It reads <projectRoot>/.agents/skills (and .dsh/skills), the
// project root being the nearest folder with a .git, so the project's
// shared folder is where it gets them, listed for it on the page too.
func TestProjectSkillsDsh(t *testing.T) {
	h, proj := projectLib(t)
	write(t, filepath.Join(h, ".dsh/profiles/web/cordis.patch.yml"), "[]\n")
	os.MkdirAll(filepath.Join(proj, ".git"), 0o755)
	ok(t)(ProjectSkill(proj, "pdf", []string{"dsh"}))
	p := filepath.Join(proj, ".agents/skills/pdf")
	if !isLink(t, p) || !ours(p, "pdf") {
		t.Errorf("%s isn't a link to the library's", p)
	}
	if g := read(t, filepath.Join(proj, ".gitignore")); !strings.Contains(g, "/.agents/skills/pdf\n") {
		t.Errorf(".gitignore lacks /.agents/skills/pdf:\n%s", g)
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(v.Agents, func(a AgentView) bool { return a.ID == "dsh" })
	if i < 0 {
		t.Fatal("no dsh on the page")
	}
	if v.Agents[i].ProjectSkills != ".agents/skills" {
		t.Errorf("dsh's project skills: %q", v.Agents[i].ProjectSkills)
	}
	// it reads no project MCP file: its servers are rows in its profiles'
	// patch lists, which have no project layer
	if v.Agents[i].ProjectMCP != "" {
		t.Errorf("dsh's project MCP: %q", v.Agents[i].ProjectMCP)
	}
}
