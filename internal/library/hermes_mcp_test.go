package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/appdir"
	"gopkg.in/yaml.v3"
)

// hermesConfig is a Hermes config.yaml as the user keeps it: their model,
// a comment, and a server of their own with settings magpie doesn't write.
const hermesConfig = `# my Hermes settings
model:
  default: anthropic/claude-sonnet-4.6 # the one I like
  provider: openrouter
mcp_servers:
  mine:
    command: uvx
    args: [mine-mcp]
    timeout: 30
toolsets:
  - hermes-cli
`

// EUPH on Discord: the Library's MCP servers couldn't be given to Hermes.
// Hermes reads mcp_servers from $HERMES_HOME/config.yaml (tools/mcp_tool.py):
// command/args/env, url/headers, transport: sse for SSE; each is written
// there beside the user's settings, comments and servers, read back, and
// only magpie's taken out again.
func TestHermesMCP(t *testing.T) {
	h := sandbox(t)
	dir := filepath.Join(h, "hermes-home")
	t.Setenv("HERMES_HOME", dir)
	p := filepath.Join(dir, "config.yaml")
	write(t, p, hermesConfig)
	holdCLI(t, h, "hermes") // the config alone is gone (#2173, #2177)

	tg := targetByID("hermes")
	if tg == nil || tg.MCP == nil || tg.MCP.Path != p {
		t.Fatalf("hermes has no MCP target: %+v", tg)
	}
	if id, err := Takes("hermes-agent", "mcp"); id != "hermes" || err != nil {
		t.Errorf("Takes: %q %v", id, err)
	}
	if tg.Skills != filepath.Join(dir, "skills") {
		t.Errorf("skills: %q", tg.Skills)
	}

	agents := []string{"hermes"}
	stdio := Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}, Env: map[string]string{"K": "V"}, Agents: agents}
	web := Server{Name: "web", Transport: "http", URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer x"}, Agents: agents}
	sse := Server{Name: "old", Transport: "sse", URL: "https://example.com/sse", Agents: agents}
	for _, s := range []Server{stdio, web, sse} {
		ok(t)(SaveServer("", s))
	}

	got := read(t, p)
	for _, want := range []string{"# my Hermes settings", "# the one I like", "provider: openrouter", "- hermes-cli"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q:\n%s", want, got)
		}
	}
	var f struct {
		Servers map[string]map[string]any `yaml:"mcp_servers"`
	}
	if err := yaml.Unmarshal([]byte(got), &f); err != nil {
		t.Fatalf("%v:\n%s", err, got)
	}
	if fs := f.Servers["fs"]; fs["command"] != "npx" || fs["url"] != nil || fs["transport"] != nil {
		t.Errorf("fs: %v", fs)
	} else if e, _ := fs["env"].(map[string]any); e["K"] != "V" {
		t.Errorf("fs env: %v", fs)
	}
	if w := f.Servers["web"]; w["url"] != "https://example.com/mcp" || w["transport"] != nil || w["command"] != nil {
		t.Errorf("web: %v", w)
	} else if hd, _ := w["headers"].(map[string]any); hd["Authorization"] != "Bearer x" {
		t.Errorf("web headers: %v", w)
	}
	if o := f.Servers["old"]; o["url"] != "https://example.com/sse" || o["transport"] != "sse" {
		t.Errorf("sse: %v", o)
	}
	if m := f.Servers["mine"]; m["command"] != "uvx" || m["timeout"] != 30 {
		t.Errorf("the user's server: %v", m)
	}
	// written in Hermes' own order, command before args
	if i, j := strings.Index(got, "command: npx"), strings.Index(got, "- '@mcp/fs'"); i < 0 || j < i {
		t.Errorf("fs isn't written as Hermes writes it:\n%s", got)
	}

	read1, err := tg.MCP.read()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []Server{stdio, web, sse} {
		if s := read1[want.Name]; s == nil || !s.same(&want) {
			t.Errorf("%s read back as %+v", want.Name, s)
		}
	}
	before := read(t, p)
	ok(t)(Sync())
	if read(t, p) != before {
		t.Errorf("a sync rewrote servers that are as the library has them:\n%s", read(t, p))
	}

	// a timeout or enabled the user sets on magpie's server stays when it
	// is written again
	got = strings.Replace(read(t, p), "    url: https://example.com/mcp\n", "    url: https://example.com/mcp\n    enabled: false\n    timeout: 90\n", 1)
	write(t, p, got)
	ok(t)(SaveServer("web", Server{Name: "web", Transport: "http", URL: "https://example.com/v2", Agents: agents}))
	yaml.Unmarshal([]byte(read(t, p)), &f)
	if w := f.Servers["web"]; w["url"] != "https://example.com/v2" || w["enabled"] != false || w["timeout"] != 90 || w["headers"] != nil {
		t.Errorf("web after edit: %v\n%s", w, read(t, p))
	}

	for _, s := range []string{"fs", "web", "old"} {
		ok(t)(RemoveServer(s))
	}
	after, _ := tg.MCP.read()
	if len(after) != 1 || after["mine"] == nil || after["mine"].Command != "uvx" {
		t.Errorf("after: %+v\n%s", after, read(t, p))
	}
	got = read(t, p)
	for _, want := range []string{"# my Hermes settings", "# the one I like", "timeout: 30"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q:\n%s", want, got)
		}
	}
}

// A server Hermes has that the library doesn't is found, to bring in.
func TestHermesMCPFound(t *testing.T) {
	h := sandbox(t)
	dir := appdir.HermesHome()
	write(t, filepath.Join(dir, "config.yaml"), strings.Replace(hermesConfig, "toolsets:", "  remote:\n    url: https://r.example/sse\n    transport: sse\ntoolsets:", 1))
	holdCLI(t, h, "hermes") // the config alone is gone (#2173, #2177)
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]*Server{}
	for _, f := range foundServers(l) {
		found[f.Server.Name] = f.Server
	}
	if s := found["mine"]; s == nil || s.Command != "uvx" || !strings.Contains(strings.Join(s.Agents, ","), "hermes") {
		t.Errorf("mine: %+v", s)
	}
	if s := found["remote"]; s == nil || s.Transport != "sse" || s.URL != "https://r.example/sse" {
		t.Errorf("remote: %+v", s)
	}
}

// Norethcy on X: Hermes's profiles got a model each but no skills or MCP
// servers, which reached the connected profile alone. Each other profile is
// an agent of its own (hermes#<name>) with its own config.yaml and skills
// folder (hermes_cli/profiles.py): a server or skill given to one lands in
// that profile and in no other.
func TestHermesProfilesGetTheirOwnLibrary(t *testing.T) {
	h := sandbox(t)
	t.Setenv("HERMES_HOME", "")
	root := appdir.HermesHomeIn(h)
	main := filepath.Join(root, "config.yaml")
	coder := filepath.Join(root, "profiles", "coder", "config.yaml")
	write(t, main, hermesConfig)
	write(t, coder, "model:\n  default: kimi-k3\n  provider: moonshot\n")
	holdCLI(t, h, "hermes")

	tg := targetByID("hermes#coder")
	if tg == nil || tg.MCP == nil || tg.MCP.Path != coder || tg.Skills != filepath.Join(root, "profiles", "coder", "skills") {
		t.Fatalf("coder's target: %+v", tg)
	}
	if m := targetByID("hermes"); m == nil || m.MCP.Path != main || m.Skills != filepath.Join(root, "skills") {
		t.Fatalf("the default profile's target: %+v", m)
	}

	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}, Agents: []string{"hermes#coder"}}))
	if !strings.Contains(read(t, coder), "fs:") || !strings.Contains(read(t, coder), "provider: moonshot") {
		t.Errorf("coder has no fs, or lost its own settings:\n%s", read(t, coder))
	}
	if strings.Contains(read(t, main), "fs:") {
		t.Errorf("the server reached the default profile:\n%s", read(t, main))
	}

	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"hermes#coder"}))
	if !ours(filepath.Join(root, "profiles", "coder", "skills", "pdf"), "pdf") {
		t.Error("coder's skills folder has no pdf")
	}
	if _, err := os.Lstat(filepath.Join(root, "skills", "pdf")); !os.IsNotExist(err) {
		t.Error("the skill reached the default profile")
	}

	ok(t)(RemoveServer("fs"))
	if strings.Contains(read(t, coder), "fs:") {
		t.Errorf("fs stays in coder:\n%s", read(t, coder))
	}
}
