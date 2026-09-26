package library

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/tidwall/jsonc"
)

// sandbox is a home with every agent magpie can give the library to, and
// nothing on PATH, so only these are found.
func sandbox(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("PATH", "")
	for _, k := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "PI_CODING_AGENT_DIR", "COPILOT_HOME", "APPDATA"} {
		t.Setenv(k, "")
	}
	for _, f := range []string{
		".claude/settings.json", ".codex/config.toml", ".gemini/settings.json",
		".config/opencode/opencode.json", ".pi/agent/settings.json", ".config/goose/config.yaml",
		".cursor/cli-config.json", ".copilot/settings.json", ".config/crush/crush.json",
	} {
		write(t, filepath.Join(h, f), "")
	}
	return h
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// ok fails the test on an error or a problem: ok(t)(SaveServer(…)).
func ok(t *testing.T) func(*Result, error) *Result {
	return func(r *Result, err error) *Result {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Problems) > 0 {
			t.Fatalf("problems: %+v", r.Problems)
		}
		return r
	}
}

func ids(ts []*Target) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Agent.ID)
	}
	return out
}

func TestTargets(t *testing.T) {
	sandbox(t)
	got := ids(Targets())
	for _, id := range []string{"claude", "codex", "gemini", "opencode", "pi", "goose", "cursor", "copilot", "crush"} {
		if !slices.Contains(got, id) {
			t.Errorf("%s not a target: %v", id, got)
		}
	}
}

// Every format writes a server so that reading it back gives it again, and
// taking it out leaves the file as the user had it.
func TestServerEveryFormat(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".claude.json"), `{"numStartups": 3, "mcpServers": {"mine": {"command": "x"}}}`)
	write(t, filepath.Join(h, ".codex/config.toml"), "model = \"gpt-5\"\n\n[mcp_servers.mine]\ncommand = \"x\"\n\n[profiles.a]\nmodel = \"o3\"\n")
	write(t, filepath.Join(h, ".config/goose/config.yaml"), "GOOSE_MODEL: x\nextensions:\n  developer:\n    enabled: true\n    type: builtin\n    name: developer\n")
	write(t, filepath.Join(h, ".config/opencode/opencode.json"), "{\n  // mine\n  \"theme\": \"x\"\n}\n")
	before := map[string]string{}
	for _, tg := range Targets() {
		if tg.MCP != nil {
			before[tg.Agent.ID] = read(t, tg.MCP.Path)
		}
	}
	all := ids(Targets())
	stdio := Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs", "/tmp/a b"},
		Env: map[string]string{"TOKEN": "t\"q"}, Agents: all}
	ok(t)(SaveServer("", stdio))
	remote := Server{Name: "web", Transport: "http", URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer x"}, Agents: all}
	ok(t)(SaveServer("", remote))

	for _, tg := range Targets() {
		if tg.MCP == nil {
			continue
		}
		got, err := tg.MCP.read()
		if err != nil {
			t.Fatalf("%s: %v", tg.Agent.ID, err)
		}
		for _, want := range []Server{stdio, remote} {
			s := got[want.Name]
			if s == nil || !s.same(&want) {
				t.Errorf("%s: %s read back as %+v\n%s", tg.Agent.ID, want.Name, s, read(t, tg.MCP.Path))
			}
		}
		if got["mine"] == nil && strings.Contains(before[tg.Agent.ID], `"mine"`) {
			t.Errorf("%s lost the user's server", tg.Agent.ID)
		}
	}
	if s := read(t, filepath.Join(h, ".codex/config.toml")); !strings.HasPrefix(s, "model = \"gpt-5\"") || !strings.Contains(s, "[profiles.a]") {
		t.Errorf("codex config lost the user's:\n%s", s)
	}
	if s := read(t, filepath.Join(h, ".config/opencode/opencode.json")); !strings.Contains(s, "// mine") {
		t.Errorf("opencode lost its comment:\n%s", s)
	}

	ok(t)(RemoveServer("fs"))
	ok(t)(RemoveServer("web"))
	for _, tg := range Targets() {
		if tg.MCP == nil {
			continue
		}
		got, _ := tg.MCP.read()
		if got["fs"] != nil || got["web"] != nil {
			t.Errorf("%s still has them:\n%s", tg.Agent.ID, read(t, tg.MCP.Path))
		}
	}
	for _, id := range []string{"codex", "goose"} {
		tg := targetByID(id)
		if a, b := strings.TrimSpace(before[id]), strings.TrimSpace(read(t, tg.MCP.Path)); a != b {
			t.Errorf("%s isn't as it was:\n%s\n---\n%s", id, a, b)
		}
	}
}

func TestServerKeepsUsersKeys(t *testing.T) {
	h := sandbox(t)
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Agents: []string{"codex", "copilot", "gemini"}}))
	cfg := filepath.Join(h, ".codex/config.toml")
	write(t, cfg, strings.Replace(read(t, cfg), "command = ", "startup_timeout_sec = 30\ncommand = ", 1))
	cp := filepath.Join(h, ".copilot/mcp-config.json")
	write(t, cp, strings.Replace(read(t, cp), `"*"`, `"read"`, 1))
	gm := filepath.Join(h, ".gemini/settings.json")
	var g map[string]any
	json.Unmarshal(jsonc.ToJSON([]byte(read(t, gm))), &g)
	g["mcpServers"].(map[string]any)["fs"].(map[string]any)["trust"] = true
	b, _ := json.Marshal(g)
	write(t, gm, string(b))

	ok(t)(SaveServer("fs", Server{Name: "fs", Transport: "stdio", Command: "uvx", Agents: []string{"codex", "copilot", "gemini"}}))
	if s := read(t, cfg); !strings.Contains(s, "startup_timeout_sec = 30") || !strings.Contains(s, `"uvx"`) {
		t.Errorf("codex:\n%s", s)
	}
	if s := read(t, cp); !strings.Contains(s, `"read"`) || !strings.Contains(s, `"uvx"`) {
		t.Errorf("copilot:\n%s", s)
	}
	if s := read(t, gm); !strings.Contains(s, `"trust":true`) || !strings.Contains(s, `"uvx"`) {
		t.Errorf("gemini:\n%s", s)
	}
}

func TestDelCodexRemovesServerSubtables(t *testing.T) {
	const before = `[user]
note = '''
[mcp_servers.x.fake]
'''

`
	const server = "[mcp_servers.x]\ncommand = \"runner\"\n\n"
	const env = "[mcp_servers.x.env]\nTOKEN = \"value\"\n\n"
	const arrays = `[[mcp_servers.x.env_vars]]
name = "FIRST"
source = "local"

[[mcp_servers.x.env_vars]]
name = "SECOND"
source = "local"

`
	const after = `[[skills.config]]
path = "/keep-the-skill"

[mcp_servers.xy]
command = "same prefix but another server"

[mcp_servers.y]
command = "keep"
`
	for _, self := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "config.toml")
		write(t, path, before+server+env+arrays+after)
		if err := delCodex(path, "x", self); err != nil {
			t.Fatal(err)
		}
		want := before + after
		if !self {
			want = before + server + after
		}
		if got := read(t, path); got != want {
			t.Fatalf("self=%v, got:\n%s\nwant:\n%s", self, got, want)
		}
		var document map[string]any
		if err := toml.Unmarshal([]byte(read(t, path)), &document); err != nil {
			t.Fatal(err)
		}
		if self && document["mcp_servers"].(map[string]any)["x"] != nil {
			t.Fatal("removed server was implicitly recreated by a child table")
		}
	}
}

func TestDelCodexParseErrorLeavesFileUntouched(t *testing.T) {
	const input = "[mcp_servers.x]\ncommand = \"runner\"\n\n[mcp_servers.x.env]\nTOKEN = \"value\"\n\n[other]\ninvalid = [\n"
	for _, self := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "config.toml")
		write(t, path, input)
		if err := delCodex(path, "x", self); err == nil || !strings.HasPrefix(err.Error(), path+": ") {
			t.Fatalf("expected a parse error naming the file, got %v", err)
		}
		if got := read(t, path); got != input {
			t.Fatalf("changed file after a parse error:\n%s", got)
		}
	}
}

func TestPutCodexPreservesChildArrayValues(t *testing.T) {
	const other = "[mcp_servers.other]\ncommand = \"keep\"\n"
	const input = `[mcp_servers.x]
command = "old"

[[mcp_servers.x.env_vars]]
name = "FIRST"
source = "local"

[[mcp_servers.x.env_vars]]
name = "SECOND"
source = "local"

`
	path := filepath.Join(t.TempDir(), "config.toml")
	write(t, path, input+other)
	f := &mcpFile{Path: path, Format: fmtCodex}
	before, err := f.entries()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.put(&Server{Name: "x", Transport: "stdio", Command: "new"}, before["x"]); err != nil {
		t.Fatal(err)
	}
	after, err := f.entries()
	if err != nil {
		t.Fatal(err)
	}
	if after["x"]["command"] != "new" || !reflect.DeepEqual(after["x"]["env_vars"], before["x"]["env_vars"]) {
		t.Fatalf("lost the user's array values while saving: %v", after["x"])
	}
	if !strings.HasSuffix(read(t, path), other) {
		t.Fatal("changed the other server")
	}
	if err := f.del("x"); err != nil {
		t.Fatal(err)
	}
	after, err = f.entries()
	if err != nil || after["x"] != nil || read(t, path) != other {
		t.Fatalf("server was not completely removed: %v, %v\n%s", after, err, read(t, path))
	}
}

func TestServerRenameAndAgents(t *testing.T) {
	h := sandbox(t)
	ok(t)(SaveServer("", Server{Name: "a", Transport: "stdio", Command: "x", Agents: []string{"claude", "cursor"}}))
	ok(t)(SaveServer("a", Server{Name: "b", Transport: "stdio", Command: "x", Agents: []string{"claude", "cursor"}}))
	c := read(t, filepath.Join(h, ".claude.json"))
	if strings.Contains(c, `"a"`) || !strings.Contains(c, `"b"`) {
		t.Errorf("rename:\n%s", c)
	}
	ok(t)(ServerAgents("b", []string{"claude"}))
	if s := read(t, filepath.Join(h, ".cursor/mcp.json")); strings.Contains(s, `"b"`) {
		t.Errorf("cursor still has it:\n%s", s)
	}
	if _, err := SaveServer("", Server{Name: "b", Transport: "stdio", Command: "y"}); err == nil {
		t.Error("a second server by the same name")
	}
	if _, err := SaveServer("", Server{Name: "../x", Transport: "stdio", Command: "y"}); err == nil {
		t.Error("a name that isn't one")
	}
}

// Codex and Goose can't reach a server over SSE: they're told so, the
// others get it.
func TestServerSSE(t *testing.T) {
	sandbox(t)
	r, err := SaveServer("", Server{Name: "s", Transport: "sse", URL: "http://localhost:9/sse", Agents: []string{"claude", "codex", "goose"}})
	if err != nil {
		t.Fatal(err)
	}
	var who []string
	for _, p := range r.Problems {
		who = append(who, p.Agent)
	}
	slices.Sort(who)
	if !slices.Equal(who, []string{"codex", "goose"}) {
		t.Errorf("problems: %+v", r.Problems)
	}
	got, _ := targetByID("claude").MCP.read()
	if got["s"] == nil || got["s"].Transport != "sse" {
		t.Errorf("claude: %+v", got["s"])
	}
}

func TestImportServer(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".claude.json"), `{"mcpServers": {"gh": {"type": "stdio", "command": "gh-mcp", "args": ["serve"]}}}`)
	write(t, filepath.Join(h, ".cursor/mcp.json"), `{"mcpServers": {"gh": {"command": "gh-mcp", "args": ["serve"]}}}`)
	write(t, filepath.Join(h, ".gemini/settings.json"), `{"mcpServers": {"gh": {"command": "other"}}}`)
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.FoundServers) != 1 || v.FoundServers[0].Server.Name != "gh" {
		t.Fatalf("found: %+v", v.FoundServers)
	}
	f := v.FoundServers[0]
	if !slices.Equal(f.Server.Agents, []string{"claude", "cursor"}) || !slices.Equal(f.Others, []string{"gemini"}) {
		t.Errorf("found: %+v", f)
	}
	ok(t)(ImportServer("gh"))
	v, _ = Read(nil)
	if len(v.FoundServers) != 0 || len(v.Servers) != 1 {
		t.Errorf("after: %+v %+v", v.FoundServers, v.Servers)
	}
	ok(t)(RemoveServer("gh"))
	if s := read(t, filepath.Join(h, ".gemini/settings.json")); !strings.Contains(s, "other") {
		t.Errorf("gemini's own went:\n%s", s)
	}
	if s := read(t, filepath.Join(h, ".cursor/mcp.json")); strings.Contains(s, "gh-mcp") {
		t.Errorf("cursor kept it:\n%s", s)
	}
}

func TestInstructions(t *testing.T) {
	h := sandbox(t)
	cl := filepath.Join(h, ".claude/CLAUDE.md")
	write(t, cl, "# Mine\n\nBe brief.\n")
	shared := "Use tabs."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"claude", "codex"}}))
	s := read(t, cl)
	if !strings.HasPrefix(s, "# Mine\n\nBe brief.\n\n"+blockBegin+"\nUse tabs.\n"+blockEnd) {
		t.Errorf("claude:\n%s", s)
	}
	cx := filepath.Join(h, ".codex/AGENTS.md")
	if s := read(t, cx); s != blockBegin+"\nUse tabs.\n"+blockEnd+"\n" {
		t.Errorf("codex:\n%q", s)
	}
	extra := "Codex only."
	ok(t)(SaveInstructions(InstructionsChange{Extra: map[string]*string{"codex": &extra}}))
	if s := read(t, cx); !strings.Contains(s, "Use tabs.\n\nCodex only.") {
		t.Errorf("codex extra:\n%s", s)
	}

	// edited in the file: left alone until the library changes
	write(t, cl, strings.Replace(read(t, cl), "Use tabs.", "Use spaces.", 1))
	ok(t)(Sync())
	if !strings.Contains(read(t, cl), "Use spaces.") {
		t.Error("an edit in the file was undone")
	}
	iv, _ := ReadInstructions()
	for _, a := range iv.Agents {
		if a.Agent == "claude" && (!a.Edited || a.Own != 3) {
			t.Errorf("claude: %+v", a)
		}
	}
	ok(t)(SaveInstructions(InstructionsChange{Rewrite: []string{"claude"}}))
	if !strings.Contains(read(t, cl), "Use tabs.") {
		t.Error("rewrite didn't")
	}

	// off: magpie's part goes, the user's stays; a file only magpie wrote goes
	ok(t)(SaveInstructions(InstructionsChange{Agents: []string{}}))
	if s := read(t, cl); s != "# Mine\n\nBe brief.\n" {
		t.Errorf("claude after:\n%q", s)
	}
	if _, err := os.Stat(cx); !os.IsNotExist(err) {
		t.Error("codex's AGENTS.md, only magpie's, is still there")
	}
	if es, _ := os.ReadDir(BackupDir()); len(es) == 0 {
		t.Error("nothing was backed up")
	}
}

func TestImportInstructions(t *testing.T) {
	h := sandbox(t)
	gm := filepath.Join(h, ".gemini/GEMINI.md")
	write(t, gm, "Always answer in French.\n")
	ok(t)(ImportInstructions("gemini"))
	iv, _ := ReadInstructions()
	if iv.Shared != "Always answer in French." {
		t.Errorf("shared: %q", iv.Shared)
	}
	if s := read(t, gm); s != blockBegin+"\nAlways answer in French.\n"+blockEnd+"\n" {
		t.Errorf("gemini:\n%q", s)
	}
}

func skill(t *testing.T, dir, name, desc string) {
	write(t, filepath.Join(dir, "SKILL.md"), "---\nname: "+name+"\ndescription: "+desc+"\n---\n\n# "+name+"\n")
	write(t, filepath.Join(dir, "scripts/run.sh"), "echo hi\n")
}

func TestSkillsFromFolder(t *testing.T) {
	h := sandbox(t)
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	skill(t, filepath.Join(src, "nested/xlsx"), "xlsx", "Sheets")
	p, err := ProbeSkills(src)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, c := range p.Candidates {
		paths = append(paths, c.Path)
	}
	slices.Sort(paths)
	if !slices.Equal(paths, []string{"nested/xlsx", "pdf"}) {
		t.Fatalf("candidates: %+v", p.Candidates)
	}
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"claude", "codex", "opencode"}))
	for _, d := range []string{".claude/skills/pdf", ".codex/skills/pdf", ".config/opencode/skills/pdf"} {
		if _, err := os.Stat(filepath.Join(h, d, "SKILL.md")); err != nil {
			t.Errorf("%s: %v", d, err)
		}
	}
	// editing the folder is editing the skill
	write(t, filepath.Join(src, "pdf/SKILL.md"), "---\nname: pdf\ndescription: Changed\n---\n")
	v, _ := Read(nil)
	if len(v.Skills) != 1 || v.Skills[0].Description != "Changed" || v.Skills[0].Kind != "folder" {
		t.Errorf("skills: %+v", v.Skills)
	}
	ok(t)(SkillAgents("pdf", []string{"claude"}))
	if _, err := os.Lstat(filepath.Join(h, ".codex/skills/pdf")); !os.IsNotExist(err) {
		t.Error("codex still has it")
	}
	ok(t)(RemoveSkill("pdf"))
	if _, err := os.Lstat(filepath.Join(h, ".claude/skills/pdf")); !os.IsNotExist(err) {
		t.Error("claude still has it")
	}
	if _, err := os.Stat(filepath.Join(src, "pdf/SKILL.md")); err != nil {
		t.Error("the user's folder went with it")
	}
}

func TestSkillConflictAndImport(t *testing.T) {
	h := sandbox(t)
	skill(t, filepath.Join(h, ".claude/skills/notes"), "notes", "Mine")
	ext := filepath.Join(h, "elsewhere/lint")
	skill(t, ext, "lint", "Linked")
	os.MkdirAll(filepath.Join(h, ".codex/skills"), 0o755)
	os.Symlink(ext, filepath.Join(h, ".codex/skills/lint"))
	os.MkdirAll(filepath.Join(h, ".gemini/skills"), 0o755)
	os.Symlink(ext, filepath.Join(h, ".gemini/skills/lint"))

	v, _ := Read(nil)
	byName := map[string]FoundSkill{}
	for _, f := range v.FoundSkills {
		byName[f.Name] = f
	}
	if f := byName["lint"]; !slices.Equal(f.Agents, []string{"codex", "gemini"}) || f.Link == "" {
		t.Errorf("lint: %+v", f)
	}
	ok(t)(ImportSkill("notes"))
	ok(t)(ImportSkill("lint"))
	if fi, err := os.Lstat(filepath.Join(h, ".claude/skills/notes")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("notes isn't linked from the library now: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ext, "SKILL.md")); err != nil {
		t.Error("the folder a link pointed to went")
	}
	for _, d := range []string{".codex/skills/lint", ".gemini/skills/lint"} {
		if !ours(filepath.Join(h, d), "lint") {
			t.Errorf("%s isn't the library's", d)
		}
	}

	// the user's own by a name the library has: not overwritten
	skill(t, filepath.Join(h, ".cursor/skills/notes"), "notes", "Cursor's")
	r, err := SkillAgents("notes", []string{"claude", "cursor"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Problems) != 1 || r.Problems[0].Agent != "cursor" {
		t.Errorf("problems: %+v", r.Problems)
	}
	if !strings.Contains(read(t, filepath.Join(h, ".cursor/skills/notes/SKILL.md")), "Cursor's") {
		t.Error("cursor's own was overwritten")
	}
}

func tarball(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: "owner-repo-abc123/" + name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.WriteHeader(&tar.Header{Name: "owner-repo-abc123/../evil", Mode: 0o644, Typeflag: tar.TypeReg})
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestSkillsFromGitHub(t *testing.T) {
	h := sandbox(t)
	version := "one"
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		if strings.Contains(r.URL.Path, "missing") {
			http.NotFound(w, r)
			return
		}
		w.Write(tarball(t, map[string]string{
			"README.md":            "hi",
			"skills/pdf/SKILL.md":  "---\nname: pdf\ndescription: PDFs " + version + "\n---\n",
			"skills/pdf/forms.md":  "forms",
			"skills/docx/SKILL.md": "---\nname: docx\ndescription: Word\n---\n",
			"template/SKILL.md":    "---\nname: template\n---\n",
		}))
	}))
	defer srv.Close()
	old := tarballURL
	tarballURL = func(repo, ref string) string { return srv.URL + "/" + repo + "/" + ref }
	defer func() { tarballURL = old }()

	if _, err := ProbeSkills("owner/missing"); err == nil || !strings.Contains(err.Error(), "no repository") {
		t.Errorf("missing: %v", err)
	}
	in := "https://github.com/owner/repo/tree/main/skills"
	p, err := ProbeSkills(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Candidates) != 2 {
		t.Fatalf("candidates: %+v", p.Candidates)
	}
	if asked[len(asked)-1] != "/owner/repo/main" {
		t.Errorf("asked %v", asked)
	}
	ok(t)(InstallSkills(in, []string{"skills/pdf"}, []string{"claude"}))
	if s := read(t, filepath.Join(h, ".claude/skills/pdf/forms.md")); s != "forms" {
		t.Errorf("forms: %q", s)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(p.root), "evil")); err == nil {
		t.Error("the tarball wrote outside its folder")
	}
	version = "two"
	ok(t)(UpdateSkill("pdf"))
	v, _ := Read(nil)
	if v.Skills[0].Description != "PDFs two" || v.Skills[0].Source != in+"/pdf" {
		t.Errorf("after update: %+v", v.Skills[0])
	}
	if !ours(filepath.Join(h, ".claude/skills/pdf"), "pdf") {
		t.Error("claude's link went in the update")
	}
	if _, err := InstallSkills(in, []string{"skills/pdf"}, nil); err == nil {
		t.Error("installed twice")
	}
}

func TestGitHubSource(t *testing.T) {
	for in, want := range map[string]Source{
		"owner/repo":                                           {Kind: "github", Repo: "owner/repo"},
		"https://github.com/owner/repo":                        {Kind: "github", Repo: "owner/repo"},
		"github.com/owner/repo.git":                            {Kind: "github", Repo: "owner/repo"},
		"https://github.com/o/r/tree/v1/skills/pdf":            {Kind: "github", Repo: "o/r", Ref: "v1", Path: "skills/pdf"},
		"https://github.com/o/r/blob/main/skills/pdf/SKILL.md": {Kind: "github", Repo: "o/r", Ref: "main", Path: "skills/pdf"},
	} {
		got, ok := githubSource(in)
		if !ok || got != want {
			t.Errorf("%s: %+v %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "not a repo", "https://gitlab.com/o/r", "/abs/path"} {
		if _, ok := githubSource(in); ok {
			t.Errorf("%q taken for GitHub", in)
		}
	}
}

// A server or skill on no agent is listed with none, not null: the page
// looks in the list, and a null blanked the whole Library.
func TestReadNoAgents(t *testing.T) {
	sandbox(t)
	ok(t)(SaveServer("", Server{Name: "lone", Transport: "stdio", Command: "lone-mcp"}))
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v.Servers)
	if !strings.Contains(string(b), `"agents":[]`) {
		t.Errorf("servers: %s", b)
	}
}

// Pi's mcp.json is written so both its MCP extensions read the transport,
// and a server written there as the extensions' READMEs have it is read.
func TestPiMCP(t *testing.T) {
	h := sandbox(t)
	p := filepath.Join(h, ".pi/agent/mcp.json")
	write(t, p, `{"mcpServers": {"supabase": {"transport": "streamable-http", "url": "https://mcp.supabase.com/mcp", "lifecycle": "eager"}}}`)
	tg := targetByID("pi")
	if tg == nil || tg.MCP == nil || tg.MCP.Path != p {
		t.Fatalf("pi target: %+v", tg)
	}
	got, err := tg.MCP.read()
	if err != nil {
		t.Fatal(err)
	}
	if s := got["supabase"]; s == nil || s.Transport != "http" || s.URL != "https://mcp.supabase.com/mcp" {
		t.Fatalf("supabase read as %+v", s)
	}
	ok(t)(SaveServer("", Server{Name: "ev", Transport: "sse", URL: "https://example.com/sse", Agents: []string{"pi"}}))
	ok(t)(SaveServer("", Server{Name: "supabase", Transport: "http", URL: "https://mcp.supabase.com/mcp", Agents: []string{"pi"}}))
	var doc struct{ MCPServers map[string]map[string]any }
	if err := json.Unmarshal([]byte(read(t, p)), &doc); err != nil {
		t.Fatal(err)
	}
	ev := doc.MCPServers["ev"]
	if ev["transport"] != "sse" || ev["httpTransport"] != "sse" || ev["url"] != "https://example.com/sse" {
		t.Errorf("ev written as %v", ev)
	}
	if sb := doc.MCPServers["supabase"]; sb["lifecycle"] != "eager" || sb["transport"] != "streamable-http" {
		t.Errorf("supabase written as %v", sb)
	}
}

// Claude Desktop is given only the servers it runs itself: a remote one is
// its Connectors', and the page says so.
func TestClaudeDesktopMCP(t *testing.T) {
	h := sandbox(t)
	if targetByID("claude-desktop") != nil {
		t.Fatal("Claude Desktop found without its folder")
	}
	d, _ := os.UserConfigDir()
	p := filepath.Join(d, "Claude", "claude_desktop_config.json")
	if !strings.HasPrefix(p, h) {
		t.Skip("config dir outside the sandbox: " + p)
	}
	write(t, p, `{"globalShortcut": "Alt+Space", "mcpServers": {"mine": {"command": "x"}}}`)
	tg := targetByID("claude-desktop")
	if tg == nil || tg.MCP == nil || tg.MCP.Path != p || tg.Instructions != "" || tg.Skills != "" {
		t.Fatalf("claude-desktop target: %+v", tg)
	}
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "fs"}, Agents: []string{"claude-desktop"}}))
	if r, err := SaveServer("", Server{Name: "web", Transport: "http", URL: "https://example.com/mcp", Agents: []string{"claude-desktop"}}); err != nil || len(r.Problems) != 1 || r.Problems[0].Error != "no-remote" {
		t.Fatalf("remote server on Claude Desktop: %+v %v", r, err)
	}
	var doc struct {
		GlobalShortcut string
		MCPServers     map[string]map[string]any
	}
	if err := json.Unmarshal([]byte(read(t, p)), &doc); err != nil {
		t.Fatal(err)
	}
	if fs := doc.MCPServers["fs"]; fs["command"] != "npx" || fs["type"] != nil {
		t.Errorf("fs written as %v", fs)
	}
	if doc.MCPServers["web"] != nil || doc.MCPServers["mine"] == nil || doc.GlobalShortcut != "Alt+Space" {
		t.Errorf("file: %s", read(t, p))
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range v.Agents {
		if a.ID == "claude-desktop" && !a.NoRemote {
			t.Error("claude-desktop not said to take no remote server")
		}
	}
	for _, s := range v.Servers {
		if s.Name == "web" && s.Problems["claude-desktop"] != "no-remote" {
			t.Errorf("web problems: %v", s.Problems)
		}
	}
}
