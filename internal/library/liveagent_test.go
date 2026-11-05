package library

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// liveAgentHome is LiveAgent's folder in the sandbox, there as LiveAgent
// leaves it, so that it is found (agent.liveagent's detect).
func liveAgentHome(t *testing.T, h string) string {
	t.Helper()
	d := filepath.Join(h, ".liveagent")
	write(t, filepath.Join(d, "config.json"), "{}")
	return d
}

type liveMCP struct {
	Servers  []map[string]any `json:"servers"`
	Selected []string         `json:"selected"`
}

func readLiveMCP(t *testing.T, p string) liveMCP {
	t.Helper()
	var f liveMCP
	// K-brain reads it with plain encoding/json (loadLiveSettings)
	if err := json.Unmarshal([]byte(read(t, p)), &f); err != nil {
		t.Fatalf("%v:\n%s", err, read(t, p))
	}
	return f
}

func liveServer(f liveMCP, id string) map[string]any {
	for _, s := range f.Servers {
		if s["id"] == id {
			return s
		}
	}
	return nil
}

// LiveAgent had neither the MCP picker nor the Skills one (zero2sin on
// Discord): its engine K-brain keeps its servers in
// config.json.live-mcp.json and its skills in skills, both in the folder
// LiveAgent hands it (K-brain 507f560 cmd/kn/backend.go,
// internal/skills/managed.go).
func TestLiveAgentTarget(t *testing.T) {
	h := sandbox(t)
	d := liveAgentHome(t, h)
	tg := targetByID("liveagent")
	if tg == nil {
		t.Fatalf("liveagent is not a target: %v", ids(Targets()))
	}
	if tg.MCP == nil || tg.MCP.Path != filepath.Join(d, "config.json.live-mcp.json") || tg.MCP.Format != fmtLiveAgent {
		t.Errorf("mcp: %+v", tg.MCP)
	}
	if tg.Skills != filepath.Join(d, "skills") || !tg.Copy {
		t.Errorf("skills: %q copy %v", tg.Skills, tg.Copy)
	}
	for _, kind := range []string{"mcp", "skills"} {
		if id, err := Takes("liveagent", kind); id != "liveagent" || err != nil {
			t.Errorf("Takes(liveagent, %s): %q %v", kind, id, err)
		}
	}

	// $LIVEAGENT_HOME, as LiveAgent starts K-brain with it
	other := filepath.Join(h, "elsewhere")
	write(t, filepath.Join(other, "config.json"), "{}")
	t.Setenv("LIVEAGENT_HOME", other)
	if tg := targetByID("liveagent"); tg == nil || tg.MCP.Path != filepath.Join(other, "config.json.live-mcp.json") || tg.Skills != filepath.Join(other, "skills") {
		t.Errorf("$LIVEAGENT_HOME: %+v", tg)
	}
}

// The file magpie writes is K-brain's LiveSettings: servers by id, each
// enabled, stdio or http, as its own GUI saves them; the user's servers,
// their fields, the file's other keys and their pick of servers kept.
func TestLiveAgentMCP(t *testing.T) {
	h := sandbox(t)
	d := liveAgentHome(t, h)
	p := filepath.Join(d, "config.json.live-mcp.json")
	write(t, p, `{
  "servers": [
    {
      "id": "mine",
      "description": "my own",
      "enabled": false,
      "transport": "stdio",
      "command": "uvx",
      "args": ["mine-mcp"],
      "cwd": "/work",
      "timeoutMs": 9000,
      "policy": {"allow": ["read"]}
    }
  ],
  "selected": ["mine"]
}
`)
	agents := []string{"liveagent"}
	stdio := Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}, Env: map[string]string{"K": "V", "T": "${TOKEN}"}, Agents: agents}
	web := Server{Name: "web", Transport: "http", URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer ${TOKEN}"}, Agents: agents}
	for _, s := range []Server{stdio, web} {
		ok(t)(SaveServer("", s))
	}

	f := readLiveMCP(t, p)
	if len(f.Servers) != 3 || f.Servers[0]["id"] != "mine" {
		t.Fatalf("servers, the user's first:\n%s", read(t, p))
	}
	fs := liveServer(f, "fs")
	if fs["transport"] != "stdio" || fs["command"] != "npx" || fs["enabled"] != true {
		t.Errorf("fs: %v", fs)
	} else if a, _ := fs["args"].([]any); len(a) != 2 || a[1] != "@mcp/fs" {
		t.Errorf("fs args: %v", fs)
	} else if e, _ := fs["env"].(map[string]any); e["K"] != "V" || e["T"] != "${TOKEN}" {
		t.Errorf("fs env, ${NAME} as K-brain resolves it: %v", fs)
	}
	w := liveServer(f, "web")
	if w["transport"] != "http" || w["url"] != "https://example.com/mcp" || w["enabled"] != true {
		t.Errorf("web: %v", w)
	} else if hd, _ := w["headers"].(map[string]any); hd["Authorization"] != "Bearer ${TOKEN}" {
		t.Errorf("web headers: %v", w)
	}
	for _, s := range f.Servers {
		if _, ok := s["type"]; ok {
			t.Errorf("%v has a type, which K-brain's servers never name", s)
		}
	}
	mine := liveServer(f, "mine")
	if mine["enabled"] != false || mine["description"] != "my own" || mine["cwd"] != "/work" || mine["timeoutMs"] != float64(9000) || mine["policy"] == nil {
		t.Errorf("the user's server changed: %v", mine)
	}
	// the user picked their servers: magpie's are picked too, or they
	// would be there with no tools (LiveManager.ToolsFor)
	if !slices.Equal(f.Selected, []string{"mine", "fs", "web"}) {
		t.Errorf("selected: %v", f.Selected)
	}
	// the user turns magpie's off in LiveAgent: it stays off
	var raw map[string]any
	json.Unmarshal([]byte(read(t, p)), &raw)
	for _, s := range raw["servers"].([]any) {
		if m := s.(map[string]any); m["id"] == "fs" {
			m["enabled"] = false
			m["timeoutMs"] = 5000
		}
	}
	b, _ := json.MarshalIndent(raw, "", "  ")
	write(t, p, string(b))
	ok(t)(Sync())
	if fs := liveServer(readLiveMCP(t, p), "fs"); fs["enabled"] != false || fs["timeoutMs"] != float64(5000) {
		t.Errorf("a sync undid the user's enabled/timeoutMs: %v", fs)
	}

	// read back, and left as they are by a sync that changes nothing
	tg := targetByID("liveagent")
	got, err := tg.MCP.read()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []Server{stdio, web} {
		if s := got[want.Name]; s == nil || !s.same(&want) {
			t.Errorf("%s read back as %+v", want.Name, s)
		}
	}
	if got["mine"] == nil || got["mine"].Command != "uvx" {
		t.Errorf("mine read back as %+v", got["mine"])
	}
	before := read(t, p)
	ok(t)(Sync())
	if read(t, p) != before {
		t.Errorf("a sync rewrote servers that are as the library has them:\n%s", read(t, p))
	}

	// no SSE: K-brain's http is streamable HTTP alone
	if err := tg.MCP.supports(&Server{Transport: "sse"}); !errors.Is(err, errNoSSE) {
		t.Errorf("sse: %v", err)
	}

	for _, s := range []string{"fs", "web"} {
		ok(t)(RemoveServer(s))
	}
	f = readLiveMCP(t, p)
	if len(f.Servers) != 1 || f.Servers[0]["id"] != "mine" || f.Servers[0]["description"] != "my own" {
		t.Errorf("after:\n%s", read(t, p))
	}
	if !slices.Equal(f.Selected, []string{"mine"}) {
		t.Errorf("selected after: %v", f.Selected)
	}
}

// With no live-mcp.json K-brain reads config.json's mcp servers
// (settingsFromConfigs): magpie's first write carries them over, as
// K-brain's own first save would, rather than leave them behind.
func TestLiveAgentMCPFromConfig(t *testing.T) {
	h := sandbox(t)
	d := liveAgentHome(t, h)
	write(t, filepath.Join(d, "config.json"), `{
  // K-brain's config.json takes comments
  "mcp": {
    "old": {"command": ["node", "server.js", "--x"], "env": {"A": "1"}, "enabled": false, "startupTimeout": 20},
    "remote": {"url": "https://example.com/r", "headers": {"H": "v"}}
  }
}`)
	tg := targetByID("liveagent")
	got, err := tg.MCP.read()
	if err != nil {
		t.Fatal(err)
	}
	if o := got["old"]; o == nil || o.Command != "node" || !slices.Equal(o.Args, []string{"server.js", "--x"}) {
		t.Errorf("old read as %+v", o)
	}
	if r := got["remote"]; r == nil || r.URL != "https://example.com/r" || r.Transport != "http" {
		t.Errorf("remote read as %+v", r)
	}

	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Agents: []string{"liveagent"}}))
	p := filepath.Join(d, "config.json.live-mcp.json")
	f := readLiveMCP(t, p)
	if len(f.Servers) != 3 {
		t.Fatalf("servers:\n%s", read(t, p))
	}
	old := liveServer(f, "old")
	if old["enabled"] != false || old["command"] != "node" || old["timeoutMs"] != float64(20000) {
		t.Errorf("old carried over as %v", old)
	}
	if r := liveServer(f, "remote"); r["transport"] != "http" || r["enabled"] != true {
		t.Errorf("remote carried over as %v", r)
	}
	if st, err := os.Stat(p); err != nil || os.PathSeparator == '/' && st.Mode().Perm() != 0o600 {
		t.Errorf("a new file is the user's alone, as K-brain writes it: %v %v", st.Mode(), err)
	}
	if f.Selected != nil {
		t.Errorf("nothing picked, every server is used: %v", f.Selected)
	}
}

// A file K-brain itself couldn't read is not written over.
func TestLiveAgentMCPUnreadable(t *testing.T) {
	h := sandbox(t)
	d := liveAgentHome(t, h)
	p := filepath.Join(d, "config.json.live-mcp.json")
	write(t, p, `{"servers": [ {"id": "mine", `)
	SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Agents: []string{"liveagent"}})
	if got := read(t, p); got != `{"servers": [ {"id": "mine", ` {
		t.Errorf("wrote over a file it couldn't read:\n%s", got)
	}
}

type liveSkillSettings struct {
	Revision uint64 `json:"revision"`
	Global   struct {
		Enabled  bool     `json:"enabled"`
		Selected []string `json:"selected"`
		Mode     string   `json:"mode"`
	} `json:"global"`
	Projects map[string]any `json:"projects"`
}

func readLiveSkills(t *testing.T, p string) liveSkillSettings {
	t.Helper()
	var s liveSkillSettings
	if err := json.Unmarshal([]byte(read(t, p)), &s); err != nil {
		t.Fatalf("%v:\n%s", err, read(t, p))
	}
	return s
}

// K-brain walks past a link in its skills folder and uses a skill there
// only once picked in skills-settings.json (internal/skills/managed.go
// ScanDetailed, Manager.Effective): LiveAgent gets a copy, picked once.
// One the user unpicks stays unpicked; one taken from LiveAgent is
// unpicked; the user's own picks and fields are kept.
func TestLiveAgentSkills(t *testing.T) {
	h := sandbox(t)
	d := liveAgentHome(t, h)
	sp := filepath.Join(d, "skills-settings.json")
	write(t, sp, `{
  "revision": 4,
  "global": {"enabled": true, "selected": ["mine"], "mode": "custom", "skillNames": ["mine"]},
  "projects": {"/work": {"enabled": false, "selected": []}}
}`)
	skill(t, filepath.Join(d, "skills", "mine"), "mine", "My own")
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	skill(t, filepath.Join(src, "xlsx"), "xlsx", "Sheets")
	if _, err := ProbeSkills(src); err != nil {
		t.Fatal(err)
	}
	ok(t)(InstallSkills(src, []string{"pdf", "xlsx"}, []string{"liveagent"}))

	for _, n := range []string{"pdf", "xlsx"} {
		p := filepath.Join(d, "skills", n)
		st, err := os.Lstat(p)
		if err != nil || !st.IsDir() {
			t.Fatalf("%s is no folder of its own (K-brain refuses a link): %v %v", n, st, err)
		}
		if _, err := os.Stat(filepath.Join(p, "SKILL.md")); err != nil {
			t.Errorf("%s has no SKILL.md", n)
		}
	}
	s := readLiveSkills(t, sp)
	if !slices.Equal(s.Global.Selected, []string{"mine", "pdf", "xlsx"}) || !s.Global.Enabled || s.Global.Mode != "custom" || s.Revision != 5 || s.Projects["/work"] == nil {
		t.Errorf("settings:\n%s", read(t, sp))
	}
	if !strings.Contains(read(t, sp), `"skillNames"`) {
		t.Errorf("lost the user's skillNames:\n%s", read(t, sp))
	}

	// a sync with nothing new writes nothing
	before := read(t, sp)
	ok(t)(Sync())
	if read(t, sp) != before {
		t.Errorf("a sync rewrote the settings:\n%s", read(t, sp))
	}

	// the user unpicks xlsx in LiveAgent: a sync leaves it unpicked
	write(t, sp, strings.Replace(read(t, sp), `"xlsx"`, `"other"`, 1))
	ok(t)(Sync())
	if s := readLiveSkills(t, sp); slices.Contains(s.Global.Selected, "xlsx") {
		t.Errorf("a sync picked xlsx again: %v", s.Global.Selected)
	}

	// pdf taken from LiveAgent: its copy and its pick go, the rest stays
	ok(t)(SkillAgents("pdf", []string{}))
	if _, err := os.Stat(filepath.Join(d, "skills", "pdf")); !os.IsNotExist(err) {
		t.Errorf("pdf's copy is still there: %v", err)
	}
	s = readLiveSkills(t, sp)
	if slices.Contains(s.Global.Selected, "pdf") || !slices.Contains(s.Global.Selected, "mine") || !slices.Contains(s.Global.Selected, "other") {
		t.Errorf("selected after pdf went: %v", s.Global.Selected)
	}
	if _, err := os.Stat(filepath.Join(d, "skills", "mine", "SKILL.md")); err != nil {
		t.Error("the user's own skill went")
	}

	// given again, picked again: a new copy
	ok(t)(SkillAgents("pdf", []string{"liveagent"}))
	if s := readLiveSkills(t, sp); !slices.Contains(s.Global.Selected, "pdf") {
		t.Errorf("pdf given again isn't picked: %v", s.Global.Selected)
	}
}

// With no skills-settings.json, magpie writes K-brain's defaults with its
// picks; a name K-brain refuses (validManagedName) is never put there,
// for its next save would fail on it.
func TestLiveAgentSkillsNewSettings(t *testing.T) {
	h := sandbox(t)
	d := liveAgentHome(t, h)
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	skill(t, filepath.Join(src, "My_Skill"), "My_Skill", "Odd name")
	if _, err := ProbeSkills(src); err != nil {
		t.Fatal(err)
	}
	ok(t)(InstallSkills(src, []string{"pdf", "My_Skill"}, []string{"liveagent"}))
	sp := filepath.Join(d, "skills-settings.json")
	s := readLiveSkills(t, sp)
	if !s.Global.Enabled || !slices.Equal(s.Global.Selected, []string{"pdf"}) || s.Revision != 1 {
		t.Errorf("settings:\n%s", read(t, sp))
	}
}

// LiveAgent's own skills, made in its folder at every start, are not
// offered to bring into the library; a skill of the user's there is.
func TestLiveAgentBuiltinSkillsNotFound(t *testing.T) {
	h := sandbox(t)
	d := liveAgentHome(t, h)
	skill(t, filepath.Join(d, "skills", "skills-creator"), "skills-creator", "built in")
	skill(t, filepath.Join(d, "skills", "skills-installer"), "skills-installer", "built in")
	skill(t, filepath.Join(d, "skills", "mine"), "mine", "My own")
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range foundSkills(l) {
		names = append(names, f.Name)
	}
	if slices.Contains(names, "skills-creator") || slices.Contains(names, "skills-installer") || !slices.Contains(names, "mine") {
		t.Errorf("found: %v", names)
	}
}
