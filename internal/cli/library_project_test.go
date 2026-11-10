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
