package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/testenv"
)

// projectSandbox is a sandbox HOME with Claude Code and Codex installed,
// a library with the skills pdf, docx and xlsx given to no agent, and a
// project folder app, the folder the test runs in.
func projectSandbox(t *testing.T) (proj string) {
	t.Helper()
	h := t.TempDir()
	for k, v := range map[string]string{"HOME": h, "USERPROFILE": h, "XDG_CONFIG_HOME": filepath.Join(h, ".config"), "XDG_STATE_HOME": "", "APPDATA": "", "LOCALAPPDATA": "", "PATH": filepath.Join(h, "bin")} {
		t.Setenv(k, v)
	}
	for _, k := range agentenv.Vars {
		t.Setenv(k, "")
	}
	put := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"claude", "codex"} {
		testenv.Program(t, filepath.Join(h, "bin", n), "#!/bin/sh\n")
		put(filepath.Join(h, "bin", n+".exe"), "")
	}
	put(filepath.Join(h, ".claude/settings.json"), "")
	put(filepath.Join(h, ".codex/config.toml"), "")
	src := filepath.Join(h, "src")
	for _, n := range []string{"pdf", "docx", "xlsx"} {
		put(filepath.Join(src, n, "SKILL.md"), "---\nname: "+n+"\ndescription: "+n+"\n---\n")
	}
	if _, err := library.InstallSkills(src, []string{"pdf", "docx", "xlsx"}, []string{}); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	proj = filepath.Join(work, "app")
	put(filepath.Join(proj, ".gitignore"), "node_modules/\n")
	if p, err := filepath.EvalSymlinks(proj); err == nil {
		proj = p
	}
	t.Chdir(proj)
	return proj
}

func projectOut(t *testing.T, args ...string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	err = libraryCmd(append([]string{"library", "project"}, args...))
	w.Close()
	os.Stdout = old
	out := <-done
	r.Close()
	return out, err
}

func linkTo(t *testing.T, p, name string) bool {
	t.Helper()
	if _, err := os.Stat(filepath.Join(p, "SKILL.md")); err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return true // a junction, or a copy where none can be made
	}
	target, err := os.Readlink(p)
	return err == nil && filepath.Base(target) == name
}

// magpie library project … (#5193, Sephiro_Lan on X): a folder added, given
// skills, a skill's agents changed and the skills taken away from a
// terminal, written as the window's Library › Projects page writes them:
// links in the folders the agents read, the project's .gitignore left as
// it is.
func TestLibraryProjectCmd(t *testing.T) {
	proj := projectSandbox(t)
	must := func(args ...string) string {
		t.Helper()
		out, err := projectOut(t, args...)
		if err != nil {
			t.Fatalf("library project %v: %v\n%s", args, err, out)
		}
		return out
	}
	refused := func(args ...string) {
		t.Helper()
		if out, err := projectOut(t, args...); err == nil {
			t.Errorf("library project %v was taken:\n%s", args, out)
		}
	}

	refused("skill", "add", ".", "pdf") // not a project yet
	must("add", ".")
	refused("add", ".")             // a project already
	refused("add", proj, "--force") // an unknown flag
	refused("add", "--force")       // not a folder
	refused("nope")                 // an unknown command
	refused("skill", "nope", ".", "pdf")
	if out := must(); !strings.Contains(out, "app") || !strings.Contains(out, "no skills yet") {
		t.Errorf("list:\n%s", out)
	}

	// with no agents=, every agent here that reads a project's skills
	must("skill", "add", ".", "pdf", "docx")
	for _, e := range []string{".claude/skills/pdf", ".agents/skills/pdf", ".claude/skills/docx", ".agents/skills/docx"} {
		if !linkTo(t, filepath.Join(proj, e), filepath.Base(e)) {
			t.Errorf("%s isn't a link to the library's", e)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(proj, ".gitignore")); string(b) != "node_modules/\n" {
		t.Errorf(".gitignore:\n%s", b)
	}
	refused("skill", "add", ".", "nope")              // no such skill
	refused("skill", "add", ".", "xlsx", "agents=zz") // no such agent
	refused("skill", "add", ".", "xlsx", "--yes")     // an unknown flag

	// a folder there that isn't magpie's is left as it is, and said
	own := filepath.Join(proj, ".claude/skills/xlsx")
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := projectOut(t, "skill", "add", ".", "xlsx", "agents=claude")
	if err == nil || !strings.Contains(out, "xlsx") || !strings.Contains(out, "isn't magpie's") {
		t.Errorf("a folder of the user's in the way: %v\n%s", err, out)
	}
	if fi, err := os.Lstat(own); err != nil || !fi.IsDir() {
		t.Errorf("the user's folder was touched: %v", err)
	}

	// the same as the page's chips: codex alone
	must("skill", "agents", ".", "pdf", "codex")
	if _, err := os.Lstat(filepath.Join(proj, ".claude/skills/pdf")); !os.IsNotExist(err) {
		t.Errorf(".claude/skills/pdf is still there: %v", err)
	}
	var v library.ProjectView
	if err := json.Unmarshal([]byte(must("skills", ".", "--json")), &v); err != nil {
		t.Fatal(err)
	}
	if strings.Join(v.Skills["pdf"], ",") != "codex" || strings.Join(v.Skills["docx"], ",") != "claude,codex" {
		t.Errorf("skills: %+v", v.Skills)
	}

	refused("skill", "rm", ".", "nope") // not the project's
	must("skill", "rm", ".", "pdf", "docx", "xlsx")
	for _, e := range []string{".agents", ".claude/skills/pdf", ".claude/skills/docx"} {
		if _, err := os.Lstat(filepath.Join(proj, e)); !os.IsNotExist(err) {
			t.Errorf("%s is still there: %v", e, err)
		}
	}
	if fi, err := os.Lstat(own); err != nil || !fi.IsDir() {
		t.Errorf("the user's folder was taken: %v", err)
	}

	// copies in place of links
	must("copy", ".", "on")
	must("skill", "add", ".", "pdf", "agents=codex")
	if fi, err := os.Lstat(filepath.Join(proj, ".agents/skills/pdf")); err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("not a copy: %v", err)
	}
	refused("copy", ".", "maybe")

	must("rm", ".")
	if _, err := os.Lstat(filepath.Join(proj, ".agents")); !os.IsNotExist(err) {
		t.Errorf(".agents is still there: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(proj, ".gitignore")); string(b) != "node_modules/\n" {
		t.Errorf(".gitignore after: %q", b)
	}
	refused("rm", ".")
}

// --keep-files takes the project off the list and leaves what magpie
// placed there, as the page's "keep its skills" box does.
func TestLibraryProjectRmKeep(t *testing.T) {
	proj := projectSandbox(t)
	for _, args := range [][]string{{"add", proj}, {"skill", "add", proj, "pdf", "agents=claude"}, {"rm", proj, "--keep-files"}} {
		if out, err := projectOut(t, args...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	if !linkTo(t, filepath.Join(proj, ".claude/skills/pdf"), "pdf") {
		t.Error("--keep-files took the skill out")
	}
	if dir, _ := library.ProjectAt(proj); dir != "" {
		t.Error("still a project")
	}
}

func TestLibraryProjectHelp(t *testing.T) {
	for _, args := range [][]string{{"library", "project", "--help"}, {"library", "projects", "-h"}} {
		if !helpAsked(args) || !strings.Contains(helpFor(args), "project skill add <dir>") {
			t.Errorf("%v: %q", args, helpFor(args))
		}
	}
	if !strings.Contains(helpFor([]string{"library", "--help"}), "magpie library project") {
		t.Error("library's usage doesn't name project")
	}
}

// magpie library project instructions … (VictorHong1022 on X): a
// project's own instructions set from a terminal go, between magpie's two
// lines, into the file each agent reads in the project, the user's own
// text there untouched, first to every agent here that reads one (the
// shared instructions are on for none); an agent with no such file is
// refused, and none takes them out.
func TestLibraryProjectInstructionsCmd(t *testing.T) {
	proj := projectSandbox(t)
	must := func(args ...string) string {
		t.Helper()
		out, err := projectOut(t, args...)
		if err != nil {
			t.Fatalf("library project %v: %v\n%s", args, err, out)
		}
		return out
	}
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(proj, rel))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		return string(b)
	}
	if err := os.WriteFile(filepath.Join(proj, "CLAUDE.md"), []byte("Mine.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	must("add", ".")
	if out := must("instructions", "."); !strings.Contains(out, "No instructions") {
		t.Errorf("show:\n%s", out)
	}
	must("instructions", ".", "text", "Use", "pnpm", "here.")
	part := "<!-- magpie:begin · written by magpie from its Library -->\nUse pnpm here.\n<!-- magpie:end -->\n"
	if got := read("CLAUDE.md"); got != "Mine.\n\n"+part {
		t.Errorf("CLAUDE.md = %q", got)
	}
	if got := read("AGENTS.md"); got != part {
		t.Errorf("AGENTS.md = %q", got)
	}
	var v struct {
		Agents []string `json:"agents"`
		Files  []string `json:"files"`
	}
	if err := json.Unmarshal([]byte(must("instructions", ".", "--json")), &v); err != nil {
		t.Fatal(err)
	}
	if strings.Join(v.Agents, ",") != "claude,codex" || strings.Join(v.Files, ",") != "AGENTS.md,CLAUDE.md" {
		t.Errorf("--json: %+v", v)
	}
	if out, err := projectOut(t, "instructions", ".", "agents", "zed"); err == nil {
		t.Errorf("zed was taken:\n%s", out)
	}
	must("instructions", ".", "agents", "none")
	if got := read("CLAUDE.md"); got != "Mine.\n" {
		t.Errorf("CLAUDE.md after none = %q", got)
	}
	if _, err := os.Stat(filepath.Join(proj, "AGENTS.md")); !os.IsNotExist(err) {
		t.Errorf("AGENTS.md is still there: %v", err)
	}
}

// magpie library project server|subagent … (#7248, jinpwng on X): a
// project's MCP servers and subagents added, pointed at other agents and
// taken out from a terminal, through the calls the page makes. Claude
// Code's project file keeps the user's own server.
func TestLibraryProjectServerAndSubagentCmd(t *testing.T) {
	proj := projectSandbox(t)
	if _, err := library.SaveServer("", library.Server{Name: "docs", Transport: "stdio", Command: "npx", Args: []string{"-y", "docs-mcp"}, Agents: []string{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := library.SaveSubagent("", library.Subagent{Name: "reviewer", Description: "reviews", Prompt: "You review code.", Agents: []string{}}); err != nil {
		t.Fatal(err)
	}
	own := `{"mcpServers":{"mine":{"command":"mine"}}}`
	if err := os.WriteFile(filepath.Join(proj, ".mcp.json"), []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := projectOut(t, args...)
		if err != nil {
			t.Fatalf("library project %v: %v\n%s", args, err, out)
		}
		return out
	}
	refused := func(args ...string) {
		t.Helper()
		if out, err := projectOut(t, args...); err == nil {
			t.Errorf("library project %v was taken:\n%s", args, out)
		}
	}
	read := func(rel string) string {
		b, _ := os.ReadFile(filepath.Join(proj, rel))
		return string(b)
	}
	must("add", ".")
	refused("server", "add", ".", "nope")
	refused("server", "add", ".", "docs", "agents=zz")
	refused("server", "add", ".", "docs", "--yes")
	refused("server", "rm", ".", "docs") // not in it
	refused("subagent", "add", ".", "docs")

	must("server", "add", ".", "docs", "agents=claude")
	if got := read(".mcp.json"); !strings.Contains(got, "docs-mcp") || !strings.Contains(got, `"mine"`) {
		t.Errorf(".mcp.json:\n%s", got)
	}
	if out := must("server", "."); !strings.Contains(out, "docs") || !strings.Contains(out, "claude") {
		t.Errorf("list:\n%s", out)
	}
	out := must("server", "list", ".", "--json")
	var js struct {
		Servers map[string][]string `json:"servers"`
	}
	if err := json.Unmarshal([]byte(out), &js); err != nil || len(js.Servers["docs"]) != 1 {
		t.Errorf("--json %v:\n%s", err, out)
	}
	must("server", "agents", ".", "docs", "claude,codex")
	if got := read(".codex/config.toml"); !strings.Contains(got, "docs-mcp") {
		t.Errorf(".codex/config.toml:\n%s", got)
	}
	must("server", "rm", ".", "docs")
	if got := read(".mcp.json"); strings.Contains(got, "docs-mcp") || !strings.Contains(got, `"mine"`) {
		t.Errorf(".mcp.json after rm:\n%s", got)
	}

	// with no agents=, the agents here that read a project's subagents
	must("subagent", "add", ".", "reviewer")
	if got := read(".claude/agents/reviewer.md"); !strings.Contains(got, "You review code.") {
		t.Errorf("claude's subagent:\n%s", got)
	}
	if got := read(".codex/agents/reviewer.toml"); !strings.Contains(got, "developer_instructions") {
		t.Errorf("codex's subagent:\n%s", got)
	}
	must("subagent", "agents", ".", "reviewer", "codex")
	if _, err := os.Stat(filepath.Join(proj, ".claude/agents/reviewer.md")); err == nil {
		t.Error("claude's subagent stayed")
	}
	must("subagent", "rm", ".", "reviewer")
	if _, err := os.Stat(filepath.Join(proj, ".codex/agents/reviewer.toml")); err == nil {
		t.Error("codex's subagent stayed")
	}
	refused("subagent", "rm", ".", "reviewer")
}

// #7253: one hook added to a project and taken out again through the CLI,
// beside the user's own entry, for the agents named.
func TestLibraryProjectHookCmd(t *testing.T) {
	proj := projectSandbox(t)
	if _, err := library.SaveHook("", library.Hook{Name: "bark", Event: "turn-end", Command: "say bark", Agents: []string{}}); err != nil {
		t.Fatal(err)
	}
	own := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo mine"}]}]}}`
	if err := os.MkdirAll(filepath.Join(proj, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".claude", "settings.local.json"), []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := projectOut(t, args...)
		if err != nil {
			t.Fatalf("library project %v: %v\n%s", args, err, out)
		}
		return out
	}
	read := func(rel string) string {
		b, _ := os.ReadFile(filepath.Join(proj, rel))
		return string(b)
	}
	must("add", ".")
	for _, bad := range [][]string{{"hook", "add", ".", "nope"}, {"hook", "add", ".", "bark", "agents=zz"}, {"hook", "add", ".", "bark", "--yes"}, {"hook", "rm", ".", "bark"}} {
		if out, err := projectOut(t, bad...); err == nil {
			t.Errorf("library project %v was taken:\n%s", bad, out)
		}
	}
	must("hook", "add", ".", "bark", "agents=claude,codex")
	if got := read(".claude/settings.local.json"); !strings.Contains(got, "say bark") || !strings.Contains(got, "echo mine") {
		t.Errorf("claude:\n%s", got)
	}
	if got := read(".codex/hooks.json"); !strings.Contains(got, "say bark") {
		t.Errorf("codex:\n%s", got)
	}
	if out := must("hook", "."); !strings.Contains(out, "bark") || !strings.Contains(out, "codex") {
		t.Errorf("list:\n%s", out)
	}
	// each row says where it comes from (#7247): magpie's bark, the project's echo mine
	var listed struct {
		Items []struct{ Name, Source, Path string }
	}
	if err := json.Unmarshal([]byte(must("hook", ".", "--json")), &listed); err != nil {
		t.Fatal(err)
	}
	src := map[string]string{}
	for _, it := range listed.Items {
		src[it.Name] = it.Source
	}
	if src["bark"] != "magpie" || src["echo mine"] != "project" || len(src) != 2 {
		t.Errorf("sources: %+v", listed.Items)
	}
	must("hook", "agents", ".", "bark", "codex")
	if got := read(".claude/settings.local.json"); strings.Contains(got, "say bark") || !strings.Contains(got, "echo mine") {
		t.Errorf("claude after agents:\n%s", got)
	}
	must("hook", "rm", ".", "bark")
	if _, err := os.Stat(filepath.Join(proj, ".codex", "hooks.json")); err == nil {
		t.Error("the file magpie made stayed")
	}
}

// magpie library project pi (#7249): add, off, on, rm, adopt and list, and
// the project's own package left as it is.
func TestLibraryProjectPiCmd(t *testing.T) {
	proj := projectSandbox(t)
	if err := os.MkdirAll(filepath.Join(proj, ".pi"), 0o755); err != nil {
		t.Fatal(err)
	}
	own := `{"packages":["npm:pi-own"]}`
	if err := os.WriteFile(filepath.Join(proj, ".pi", "settings.json"), []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := projectOut(t, args...)
		if err != nil {
			t.Fatalf("library project %v: %v\n%s", args, err, out)
		}
		return out
	}
	read := func() string {
		b, _ := os.ReadFile(filepath.Join(proj, ".pi", "settings.json"))
		return strings.Join(strings.Fields(string(b)), "")
	}
	must("add", ".")
	for _, bad := range [][]string{{"pi", "add", ".", "--evil"}, {"pi", "add", ".", "-x"}, {"pi", "add", ".", "./local dir"}, {"pi", "add", ".", "npm:pi-own"}} {
		if out, err := projectOut(t, bad...); err == nil {
			t.Errorf("library project %v was taken:\n%s", bad, out)
		}
	}
	must("pi", "add", ".", "npm:pi-demo")
	if got := read(); !strings.Contains(got, `"npm:pi-own"`) || !strings.Contains(got, `"npm:pi-demo"`) {
		t.Errorf("add: %s", got)
	}
	must("pi", "off", ".", "npm:pi-demo")
	if got := read(); !strings.Contains(got, `"extensions":[]`) {
		t.Errorf("off: %s", got)
	}
	out := must("pi", ".", "--json")
	if !strings.Contains(out, `"by": "project"`) || !strings.Contains(out, `"state": "off"`) {
		t.Errorf("list:\n%s", out)
	}
	must("pi", "on", ".", "npm:pi-demo")
	must("pi", "adopt", ".", "npm:pi-own")
	must("pi", "rm", ".", "npm:pi-demo", "npm:pi-own")
	if got := read(); got != own {
		t.Errorf("rm: %s", got)
	}
}
