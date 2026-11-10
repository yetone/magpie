package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tidwall/jsonc"
)

// mcpProject is a sandbox with two library servers on no agent and a
// project folder with a .gitignore and a .mcp.json of the user's.
func mcpProject(t *testing.T) (h, proj string) {
	h = sandbox(t)
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "fs"}}))
	ok(t)(SaveServer("", Server{Name: "docs", Transport: "http", URL: "https://docs.example/mcp"}))
	proj = filepath.Join(t.TempDir(), "app")
	write(t, filepath.Join(proj, ".gitignore"), "node_modules/\n")
	gitInit(t, proj)
	write(t, filepath.Join(proj, ".mcp.json"), `{
  // the team's own
  "mcpServers": {"team": {"command": "team-mcp"}},
  "other": 1
}
`)
	ok(t)(AddProject(proj))
	return h, proj
}

func jsonOf(t *testing.T, p string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(jsonc.ToJSON([]byte(read(t, p))), &m); err != nil {
		t.Fatalf("%s: %v\n%s", p, err, read(t, p))
	}
	return m
}

func servers(t *testing.T, p, key string) map[string]any {
	t.Helper()
	m, _ := jsonOf(t, p)[key].(map[string]any)
	return m
}

func TestProjectServers(t *testing.T) {
	h, proj := mcpProject(t)
	globalClaude := read(t, filepath.Join(h, ".claude.json"))
	globalCodex := read(t, filepath.Join(h, ".codex", "config.toml"))

	ok(t)(ProjectServer(proj, "fs", []string{"claude", "codex", "gemini", "cursor", "opencode"}))
	ok(t)(ProjectServer(proj, "docs", []string{"claude"}))

	// .mcp.json was the user's: their server, their key and comment stay
	mcp := filepath.Join(proj, ".mcp.json")
	s := servers(t, mcp, "mcpServers")
	if s["team"] == nil || s["fs"] == nil || s["docs"] == nil {
		t.Errorf(".mcp.json servers: %v", s)
	}
	if fs := s["fs"].(map[string]any); fs["command"] != "npx" || fs["type"] != "stdio" {
		t.Errorf("fs: %v", fs)
	}
	if !strings.Contains(read(t, mcp), "the team's own") || jsonOf(t, mcp)["other"] != float64(1) {
		t.Errorf(".mcp.json lost the user's:\n%s", read(t, mcp))
	}
	if g := servers(t, filepath.Join(proj, ".gemini", "settings.json"), "mcpServers"); g["fs"] == nil {
		t.Errorf("gemini: %v", g)
	}
	if c := servers(t, filepath.Join(proj, ".cursor", "mcp.json"), "mcpServers"); c["fs"] == nil {
		t.Errorf("cursor: %v", c)
	}
	if o := servers(t, filepath.Join(proj, "opencode.json"), "mcp"); o["fs"] == nil {
		t.Errorf("opencode: %v", o)
	}
	if c := read(t, filepath.Join(proj, ".codex", "config.toml")); !strings.Contains(c, "[mcp_servers.fs]") || !strings.Contains(c, `command = "npx"`) {
		t.Errorf("codex:\n%s", c)
	}
	// the agents' own user-wide files aren't touched
	if read(t, filepath.Join(h, ".claude.json")) != globalClaude || read(t, filepath.Join(h, ".codex", "config.toml")) != globalCodex {
		t.Error("a user-wide file was written")
	}
	// the files magpie made are kept out of git; the user's .mcp.json isn't
	g := excluded(t, proj)
	for _, f := range []string{"/.codex/config.toml", "/.gemini/settings.json", "/.cursor/mcp.json", "/opencode.json"} {
		if !strings.Contains(g, f+"\n") {
			t.Errorf("info/exclude lacks %s:\n%s", f, g)
		}
	}
	if strings.Contains(g, "/.mcp.json") {
		t.Errorf("info/exclude lists the user's .mcp.json:\n%s", g)
	}
	v, _ := Read(nil)
	if len(v.Projects) != 1 || !slices.Equal(v.Projects[0].Wrote[".mcp.json"], []string{"docs", "fs"}) {
		t.Errorf("view: %+v", v.Projects)
	}

	// a server renamed is renamed in the project
	ok(t)(SaveServer("docs", Server{Name: "manual", Transport: "http", URL: "https://docs.example/mcp"}))
	if s := servers(t, mcp, "mcpServers"); s["docs"] != nil || s["manual"] == nil {
		t.Errorf("after rename: %v", s)
	}

	// taken from some agents: out of their files, those magpie made gone
	ok(t)(ProjectServer(proj, "fs", []string{"claude"}))
	for _, f := range []string{".codex", ".gemini", ".cursor", "opencode.json"} {
		gone(t, filepath.Join(proj, f))
	}
	if g := excluded(t, proj); g != "" {
		t.Errorf("info/exclude: %q", g)
	}

	// removing the project takes out only magpie's entries
	ok(t)(RemoveProject(proj, false))
	s = servers(t, mcp, "mcpServers")
	if len(s) != 1 || s["team"] == nil {
		t.Errorf(".mcp.json after: %v", s)
	}
	if v, _ := Read(nil); len(v.Projects) != 0 {
		t.Errorf("projects: %+v", v.Projects)
	}
}

// A server by the name that magpie didn't write is the user's: it's left
// as it is, and said so.
func TestProjectServerNotOurs(t *testing.T) {
	_, proj := mcpProject(t)
	write(t, filepath.Join(proj, ".mcp.json"), `{"mcpServers": {"fs": {"command": "mine"}}}`)
	r, err := ProjectServer(proj, "fs", []string{"claude"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Problems) != 1 || r.Problems[0].What != "project:mcp:fs" {
		t.Errorf("problems: %+v", r.Problems)
	}
	if fs := servers(t, filepath.Join(proj, ".mcp.json"), "mcpServers")["fs"].(map[string]any); fs["command"] != "mine" {
		t.Errorf("fs: %v", fs)
	}
	// taken away again: the user's is still left alone
	ok(t)(ProjectServer(proj, "fs", nil))
	if s := servers(t, filepath.Join(proj, ".mcp.json"), "mcpServers"); s["fs"] == nil {
		t.Error("the user's fs was taken out")
	}
}

// What the user adds to a file magpie made keeps it, out of info/exclude.
func TestProjectServerFileKept(t *testing.T) {
	_, proj := mcpProject(t)
	ok(t)(ProjectServer(proj, "fs", []string{"cursor"}))
	p := filepath.Join(proj, ".cursor", "mcp.json")
	write(t, p, strings.Replace(read(t, p), `"mcpServers"`, `"theirs": true, "mcpServers"`, 1))
	ok(t)(ProjectServer(proj, "fs", nil))
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	if s := servers(t, p, "mcpServers"); len(s) != 0 || jsonOf(t, p)["theirs"] != true {
		t.Errorf("%s:\n%s", p, read(t, p))
	}
	if g := excluded(t, proj); strings.Contains(g, "cursor") {
		t.Errorf("info/exclude: %q", g)
	}
}

func TestProjectServerChecks(t *testing.T) {
	_, proj := mcpProject(t)
	if _, err := ProjectServer(proj, "fs", []string{"goose"}); err == nil {
		t.Error("an agent with no project file was taken")
	}
	if _, err := ProjectServer(proj, "nope", []string{"claude"}); err == nil {
		t.Error("a server not in the library was taken")
	}
	ok(t)(SaveServer("", Server{Name: "events", Transport: "sse", URL: "https://x.example/sse"}))
	r, err := ProjectServer(proj, "events", []string{"codex", "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Problems) != 1 || !strings.Contains(r.Problems[0].Error, "SSE") {
		t.Errorf("problems: %+v", r.Problems)
	}
	gone(t, filepath.Join(proj, ".codex"))
	// a server taken out of the library goes from the project too
	ok(t)(RemoveServer("events"))
	if s := servers(t, filepath.Join(proj, ".mcp.json"), "mcpServers"); s["events"] != nil {
		t.Errorf(".mcp.json: %v", s)
	}
	if v, _ := Read(nil); v.Projects[0].Servers["events"] != nil {
		t.Errorf("view: %+v", v.Projects[0].Servers)
	}
}

// OpenCode's opencode.jsonc is written when that is the project's.
func TestProjectServerOpenCodeJSONC(t *testing.T) {
	_, proj := mcpProject(t)
	write(t, filepath.Join(proj, "opencode.jsonc"), "{\n  // mine\n  \"model\": \"x\"\n}\n")
	ok(t)(ProjectServer(proj, "fs", []string{"opencode"}))
	gone(t, filepath.Join(proj, "opencode.json"))
	if o := servers(t, filepath.Join(proj, "opencode.jsonc"), "mcp"); o["fs"] == nil {
		t.Errorf("opencode.jsonc:\n%s", read(t, filepath.Join(proj, "opencode.jsonc")))
	}
}

// #514: ZCode reads a project's servers from .zcode/config.json's
// mcp.servers, Pi from .pi/mcp.json's mcpServers; each is written beside
// what's in the file, as its user-wide file has them.
func TestProjectServersZCodePi(t *testing.T) {
	h, proj := mcpProject(t)
	zc := filepath.Join(proj, ".zcode", "config.json")
	write(t, zc, `{
  "model": "glm-5.3",
  "mcp": {"servers": {"team": {"command": "team-mcp", "enable": false}}, "other": true}
}
`)
	userZC := filepath.Join(h, ".zcode", "cli", "config.json")
	userWas, _ := os.ReadFile(userZC)
	ok(t)(SaveServer("", Server{Name: "events", Transport: "sse", URL: "https://x.example/sse"}))

	ok(t)(ProjectServer(proj, "fs", []string{"zcode", "pi"}))
	ok(t)(ProjectServer(proj, "docs", []string{"zcode", "pi"}))
	r, err := ProjectServer(proj, "events", []string{"zcode", "pi"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Problems) != 1 || !strings.Contains(r.Problems[0].Error, ".pi/mcp.json can't reach a server over SSE") {
		t.Errorf("problems: %+v", r.Problems)
	}

	// ZCode: the user's model, server and key stay, magpie's beside them
	doc := jsonOf(t, zc)
	if doc["model"] != "glm-5.3" {
		t.Errorf("model lost:\n%s", read(t, zc))
	}
	m, _ := doc["mcp"].(map[string]any)
	s, _ := m["servers"].(map[string]any)
	if m["other"] != true || s["team"] == nil || s["team"].(map[string]any)["enable"] != false {
		t.Fatalf("the user's lost:\n%s", read(t, zc))
	}
	if fs, _ := s["fs"].(map[string]any); fs["type"] != "stdio" || fs["command"] != "npx" || len(fs["args"].([]any)) != 2 {
		t.Errorf("zcode fs: %v", fs)
	}
	if d, _ := s["docs"].(map[string]any); d["type"] != "http" || d["url"] != "https://docs.example/mcp" {
		t.Errorf("zcode docs: %v", d)
	}
	if e, _ := s["events"].(map[string]any); e["type"] != "sse" {
		t.Errorf("zcode events: %v", e)
	}

	// Pi: a file magpie made, in Pi's own shape, kept out of git
	pi := filepath.Join(proj, ".pi", "mcp.json")
	ps := servers(t, pi, "mcpServers")
	if fs, _ := ps["fs"].(map[string]any); fs["command"] != "npx" || fs["type"] != nil {
		t.Errorf("pi fs: %v", fs)
	}
	if d, _ := ps["docs"].(map[string]any); d["url"] != "https://docs.example/mcp" || d["type"] != nil {
		t.Errorf("pi docs: %v", d)
	}
	if ps["events"] != nil {
		t.Errorf("pi got an SSE server: %v", ps)
	}
	g := excluded(t, proj)
	if !strings.Contains(g, "/.pi/mcp.json\n") || strings.Contains(g, ".zcode") {
		t.Errorf("info/exclude:\n%s", g)
	}
	if now, _ := os.ReadFile(userZC); string(now) != string(userWas) {
		t.Errorf("ZCode's own file was written:\n%s", now)
	}
	v, _ := Read(nil)
	if !slices.Equal(v.Projects[0].Wrote[".zcode/config.json"], []string{"docs", "events", "fs"}) || !slices.Equal(v.Projects[0].Wrote[".pi/mcp.json"], []string{"docs", "fs"}) {
		t.Errorf("wrote: %v", v.Projects[0].Wrote)
	}
	if ProjectMCPFile("pi") != ".pi/mcp.json" || !ProjectNoSSE("pi") || ProjectMCPFile("zcode") != ".zcode/config.json" || ProjectNoSSE("zcode") {
		t.Error("ProjectMCPFile/ProjectNoSSE")
	}

	// taken out: only magpie's go, and the file it made with them
	ok(t)(RemoveProject(proj, false))
	s, _ = jsonOf(t, zc)["mcp"].(map[string]any)["servers"].(map[string]any)
	if len(s) != 1 || s["team"] == nil || jsonOf(t, zc)["model"] != "glm-5.3" {
		t.Errorf("zcode after:\n%s", read(t, zc))
	}
	gone(t, filepath.Join(proj, ".pi"))
	if g := excluded(t, proj); g != "" {
		t.Errorf("info/exclude after: %q", g)
	}
}

// #514: a project removed with keep goes off magpie's list alone; the
// skills and servers magpie put in it, and its .gitignore lines, stay.
func TestRemoveProjectKeep(t *testing.T) {
	h, proj := mcpProject(t)
	src := filepath.Join(h, "src")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	ok(t)(InstallSkills(src, []string{"pdf"}, nil))
	ok(t)(ProjectSkill(proj, "pdf", []string{"claude", "codex"}))
	ok(t)(ProjectServer(proj, "fs", []string{"claude", "pi"}))
	files := map[string]string{}
	for _, f := range []string{".mcp.json", ".pi/mcp.json", ".gitignore"} {
		files[f] = read(t, filepath.Join(proj, f))
	}

	ok(t)(RemoveProject(proj, true))
	if v, _ := Read(nil); len(v.Projects) != 0 {
		t.Errorf("projects: %+v", v.Projects)
	}
	check := func(when string) {
		t.Helper()
		for f, was := range files {
			if got := read(t, filepath.Join(proj, f)); got != was {
				t.Errorf("%s: %s changed:\n%s\nwas:\n%s", when, f, got, was)
			}
		}
		for _, e := range []string{".claude/skills/pdf", ".agents/skills/pdf"} {
			if _, err := os.Stat(filepath.Join(proj, e, "SKILL.md")); err != nil {
				t.Errorf("%s: %s: %v", when, e, err)
			}
		}
	}
	check("removed")
	// nothing magpie does later takes them out
	ok(t)(Sync())
	ok(t)(RemoveServer("fs"))
	check("later")
}
