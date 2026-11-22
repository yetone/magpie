package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidwall/jsonc"
)

// traeHere makes TRAE CN's userData folder in the sandbox, as the app
// makes it on its first start (os.UserConfigDir()/Trae CN, named for its
// nameShort), and returns that folder.
func traeHere(t *testing.T, h string) string {
	t.Helper()
	t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming")) // Windows' UserConfigDir
	d, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(d, "Trae CN")
	write(t, filepath.Join(dir, "User", "settings.json"), "{}\n")
	return dir
}

// TRAE CN keeps its skills in ~/.trae-cn/skills and its user-wide MCP
// servers in the mcp.json of its userData's User folder (TRAE CN
// 2.3.105403's workbench), but the library gave it neither: it is no
// agent magpie wires a model into, and apps held no IDE but Kiro CLI
// (#5889, ifelseboyxx).
func TestTraeCNTarget(t *testing.T) {
	h := sandbox(t)
	dir := traeHere(t, h)
	tg := targetByID("trae-cn")
	if tg == nil {
		t.Fatalf("trae-cn is not a target: %v", ids(Targets()))
	}
	if tg.Skills != filepath.Join(h, ".trae-cn", "skills") {
		t.Errorf("skills: %q", tg.Skills)
	}
	if tg.MCP == nil || tg.MCP.Path != filepath.Join(dir, "User", "mcp.json") || tg.MCP.Format != fmtTrae {
		t.Errorf("mcp: %+v", tg.MCP)
	}
	if tg.Instructions != "" {
		t.Errorf("instructions: %q, which TRAE CN keeps in its own settings", tg.Instructions)
	}
	for _, kind := range []string{"mcp", "skills"} {
		if id, err := Takes("trae-cn", kind); id != "trae-cn" || err != nil {
			t.Errorf("Takes(trae-cn, %s): %q %v", kind, id, err)
		}
	}
	if _, err := Takes("trae-cn", "instructions"); err == nil {
		t.Error("TRAE CN takes instructions")
	}

	// a skill is a real folder there, not a link its agent may skip
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	if _, err := ProbeSkills(src); err != nil {
		t.Fatal(err)
	}
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"trae-cn"}))
	p := filepath.Join(h, ".trae-cn", "skills", "pdf")
	if _, err := os.Stat(filepath.Join(p, "SKILL.md")); err != nil {
		t.Fatal("trae-cn has no pdf skill")
	}
	if isLink(t, p) {
		t.Error("trae-cn's pdf is a link, not a copy")
	}
}

// ~/.trae-cn alone isn't TRAE CN being here: its userData folder is.
// Nor is the international Trae's folder (Trae, ~/.trae) this one.
func TestTraeCNNotHere(t *testing.T) {
	h := sandbox(t)
	t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
	d, _ := os.UserConfigDir()
	write(t, filepath.Join(d, "Trae", "User", "settings.json"), "{}\n")
	write(t, filepath.Join(h, ".trae", "skills", "x", "SKILL.md"), "---\nname: x\n---\n")
	if tg := targetByID("trae-cn"); tg != nil {
		t.Fatalf("trae-cn is a target without its folder: %+v", tg)
	}
	if id, err := Takes("trae-cn", "mcp"); err == nil || !strings.Contains(err.Error(), "isn't installed") {
		t.Errorf("Takes(trae-cn, mcp) not here: %q %v", id, err)
	}
}

// TRAE CN's mcp.json has mcpServers of command, args, env, url and
// headers, no type (a url is HTTP, and SSE can't be named), read with a
// JSONC parser; its own fields (cwd, disabled, fromGalleryId) and the
// user's comments stay, and a ${NAME} isn't written there as it would go
// to the server as written.
func TestTraeCNMCP(t *testing.T) {
	h := sandbox(t)
	dir := traeHere(t, h)
	p := filepath.Join(dir, "User", "mcp.json")
	write(t, p, `{
  // added from the gallery
  "mcpServers": {
    "mine": {
      "command": "uvx",
      "args": ["mine-mcp"],
      "cwd": "/work",
      "disabled": true,
      "fromGalleryId": "g-1",
    },
  },
}`)
	tg := targetByID("trae-cn")
	if tg == nil || tg.MCP == nil {
		t.Fatalf("trae-cn has no MCP target: %+v", tg)
	}

	agents := []string{"trae-cn"}
	stdio := Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}, Env: map[string]string{"K": "V"}, Agents: agents}
	web := Server{Name: "web", Transport: "http", URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer x"}, Agents: agents}
	for _, s := range []Server{stdio, web} {
		ok(t)(SaveServer("", s))
	}

	got := read(t, p)
	if !strings.Contains(got, "// added from the gallery") {
		t.Errorf("the user's comment is gone:\n%s", got)
	}
	var f struct {
		Servers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(jsonc.ToJSON([]byte(got)), &f); err != nil {
		t.Fatalf("%v:\n%s", err, got)
	}
	for name, s := range f.Servers {
		for _, k := range []string{"type", "transport"} {
			if _, ok := s[k]; ok {
				t.Errorf("%s has %q, which TRAE CN's entries never name:\n%s", name, k, got)
			}
		}
	}
	if fs := f.Servers["fs"]; fs["command"] != "npx" {
		t.Errorf("fs: %v", fs)
	} else if e, _ := fs["env"].(map[string]any); e["K"] != "V" {
		t.Errorf("fs env: %v", fs)
	}
	if w := f.Servers["web"]; w["url"] != "https://example.com/mcp" {
		t.Errorf("web: %v", w)
	} else if hd, _ := w["headers"].(map[string]any); hd["Authorization"] != "Bearer x" {
		t.Errorf("web headers: %v", w)
	}
	if m := f.Servers["mine"]; m["command"] != "uvx" || m["cwd"] != "/work" || m["disabled"] != true || m["fromGalleryId"] != "g-1" {
		t.Errorf("the user's fields kept: %v", m)
	}

	// read back, and left as they are by a sync that changes nothing
	read1, err := tg.MCP.read()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []Server{stdio, web} {
		if s := read1[want.Name]; s == nil || !s.same(&want) {
			t.Errorf("%s read back as %+v", want.Name, s)
		}
	}
	before := read(t, p)
	ok(t)(Sync())
	if read(t, p) != before {
		t.Errorf("a sync rewrote servers that are as the library has them:\n%s", read(t, p))
	}

	// a server changed in the library is written as it is now, over
	// what magpie wrote before
	edited := stdio
	edited.Command, edited.Args = "bunx", []string{"@mcp/fs@2"}
	ok(t)(SaveServer("fs", edited))
	var g struct {
		Servers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(jsonc.ToJSON([]byte(read(t, p))), &g); err != nil {
		t.Fatal(err)
	}
	if fs := g.Servers["fs"]; fs["command"] != "bunx" || fmt.Sprint(fs["args"]) != "[@mcp/fs@2]" {
		t.Errorf("fs after an edit: %v", fs)
	}

	// no SSE: a url is streamable HTTP
	if err := tg.MCP.supports(&Server{Transport: "sse"}); !errors.Is(err, errNoSSE) {
		t.Errorf("sse: %v", err)
	}
	u, _ := tg.MCP.decode("by-url", map[string]any{"url": "https://example.com/sse"})
	if u == nil || u.Transport != "http" {
		t.Errorf("a url reads as streamable HTTP, got %+v", u)
	}
	// a ${NAME} would reach the server as written
	if err := tg.MCP.supports(&Server{Transport: "http", URL: "https://x", Headers: map[string]string{"Authorization": "Bearer ${TOKEN}"}}); !errors.Is(err, errNoEnvRef) {
		t.Errorf("${TOKEN} header: %v", err)
	}

	for _, s := range []string{"fs", "web"} {
		ok(t)(RemoveServer(s))
	}
	after, _ := tg.MCP.read()
	if len(after) != 1 || after["mine"] == nil || after["mine"].Command != "uvx" {
		t.Errorf("after: %+v\n%s", after, read(t, p))
	}
	got = read(t, p)
	for _, want := range []string{`"fromGalleryId"`, `"cwd"`, "// added from the gallery"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost the user's %s:\n%s", want, got)
		}
	}
}

// A project's skills and servers go where TRAE CN reads them:
// .trae/skills and .trae/mcp.json (its workbench, 2.3.105403), its
// skills as copies.
func TestTraeCNProject(t *testing.T) {
	if d := ProjectSkillsDir("trae-cn"); d != ".trae/skills" {
		t.Errorf("project skills: %q", d)
	}
	if f, ok := projectMCPFiles["trae-cn"]; !ok || f.rel != ".trae/mcp.json" || f.format != fmtTrae {
		t.Errorf("project mcp: %+v", f)
	}
	_, proj := projectLib(t)
	ok(t)(ProjectSkill(proj, "pdf", []string{"trae-cn"}))
	p := filepath.Join(proj, ".trae", "skills", "pdf")
	if _, err := os.Stat(filepath.Join(p, "SKILL.md")); err != nil {
		t.Fatal("the project has no .trae/skills/pdf")
	}
	if isLink(t, p) {
		t.Error(".trae/skills/pdf is a link, not a copy")
	}
}
