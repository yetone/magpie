package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func instrProject(t *testing.T) (h, dir string) {
	h = sandbox(t)
	dir = filepath.Join(t.TempDir(), "app")
	gitInit(t, dir)
	ok(t)(AddProject(dir))
	return
}

func setInstr(t *testing.T, dir, text string, agents ...string) *Result {
	t.Helper()
	c := ProjectInstr{Text: &text}
	if agents != nil {
		c.Agents = &agents
	}
	return ok(t)(ProjectInstructions(dir, c))
}

func block(text string) string { return blockBegin + "\n" + text + "\n" + blockEnd }

// A project's own instructions (VictorHong1022 on X) go into the file each
// picked agent reads in the project, between magpie's two lines, the
// user's own text in it untouched; turned off, the file is as it was, and
// one magpie made is gone. The agents' user-wide files are not touched.
func TestProjectInstructionsEachAgentsFile(t *testing.T) {
	h, dir := instrProject(t)
	mine := "# Mine\n\nRun make test.\n"
	write(t, filepath.Join(dir, "CLAUDE.md"), mine)
	setInstr(t, dir, "Use pnpm here.", "claude", "codex", "gemini", "cursor")
	if got, want := read(t, filepath.Join(dir, "CLAUDE.md")), "# Mine\n\nRun make test.\n\n"+block("Use pnpm here.")+"\n"; got != want {
		t.Errorf("CLAUDE.md = %q, want %q", got, want)
	}
	for _, f := range []string{"AGENTS.md", "GEMINI.md"} {
		if got, want := read(t, filepath.Join(dir, f)), block("Use pnpm here.")+"\n"; got != want {
			t.Errorf("%s = %q, want %q", f, got, want)
		}
	}
	for _, f := range []string{".claude/CLAUDE.md", ".codex/AGENTS.md", ".gemini/GEMINI.md"} {
		gone(t, filepath.Join(h, f))
	}
	// what magpie made is kept out of git; the user's CLAUDE.md is git's
	g, _ := gitOf(dir)
	ex := read(t, filepath.Join(g.Common, "info", "exclude"))
	for _, f := range []string{"/AGENTS.md\n", "/GEMINI.md\n"} {
		if !strings.Contains(ex, f) {
			t.Errorf("%s isn't in info/exclude:\n%s", f, ex)
		}
	}
	if strings.Contains(ex, "/CLAUDE.md") {
		t.Errorf("the user's CLAUDE.md is in info/exclude:\n%s", ex)
	}
	view := instrView(t)
	var pv *ProjectView
	for i := range view.Projects {
		if view.Projects[i].Dir == dir {
			pv = &view.Projects[i]
		}
	}
	if pv == nil || pv.Instr != "Use pnpm here." || strings.Join(pv.InstrFiles, ",") != "AGENTS.md,CLAUDE.md,GEMINI.md" {
		t.Errorf("view: %+v", pv)
	}

	// a text changed reaches every file
	setInstr(t, dir, "Use bun here.")
	for _, f := range []string{"CLAUDE.md", "AGENTS.md", "GEMINI.md"} {
		if got := read(t, filepath.Join(dir, f)); !strings.Contains(got, block("Use bun here.")) || strings.Contains(got, "pnpm") {
			t.Errorf("%s = %q", f, got)
		}
	}
	// an agent dropped takes its file's part out, the others' stay
	setInstr(t, dir, "Use bun here.", "claude", "codex", "cursor")
	gone(t, filepath.Join(dir, "GEMINI.md"))
	// off: the user's file as it was, the made one gone
	ok(t)(ProjectInstructions(dir, ProjectInstr{Agents: &[]string{}}))
	if got := read(t, filepath.Join(dir, "CLAUDE.md")); got != mine {
		t.Errorf("CLAUDE.md after off = %q, want %q", got, mine)
	}
	gone(t, filepath.Join(dir, "AGENTS.md"))
	if ex := read(t, filepath.Join(g.Common, "info", "exclude")); strings.Contains(ex, "/AGENTS.md") {
		t.Errorf("info/exclude still lists AGENTS.md:\n%s", ex)
	}
}

func instrView(t *testing.T) *View {
	t.Helper()
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// magpie's part edited in the file is the user's until the project's text
// changes; a part, or a file, the user took out is written again; text
// the user added to a file magpie made keeps the file when turned off.
func TestProjectInstructionsLeaveTheUsersEdits(t *testing.T) {
	_, dir := instrProject(t)
	setInstr(t, dir, "One.", "codex")
	f := filepath.Join(dir, "AGENTS.md")
	edited := "# Theirs\n\n" + block("One, edited.") + "\n"
	write(t, f, edited)
	ok(t)(Sync())
	if got := read(t, f); got != edited {
		t.Errorf("an edited part was written over: %q", got)
	}
	setInstr(t, dir, "Two.")
	if got, want := read(t, f), "# Theirs\n\n"+block("Two.")+"\n"; got != want {
		t.Errorf("AGENTS.md = %q, want %q", got, want)
	}
	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	ok(t)(Sync())
	if got, want := read(t, f), block("Two.")+"\n"; got != want {
		t.Errorf("a deleted file came back as %q, want %q", got, want)
	}
	write(t, f, "Mine too.\n\n"+block("Two.")+"\n")
	ok(t)(ProjectInstructions(dir, ProjectInstr{Agents: &[]string{}}))
	if got := read(t, f); got != "Mine too.\n" {
		t.Errorf("AGENTS.md after off = %q", got)
	}
}

// Codex and Pi read AGENTS.override.md in place of AGENTS.md when it is
// there, Claude Code .claude/CLAUDE.md when it has no CLAUDE.md at the
// root, and Gemini CLI the name its settings give: the file each agent
// really reads is the one written.
func TestProjectInstructionsTheFileTheAgentReads(t *testing.T) {
	h, dir := instrProject(t)
	write(t, filepath.Join(dir, "AGENTS.override.md"), "Override.\n")
	write(t, filepath.Join(dir, ".claude", "CLAUDE.md"), "Hidden.\n")
	write(t, filepath.Join(h, ".gemini", "settings.json"), `{"context": {"fileName": ["CONTEXT.md", "GEMINI.md"]}}`)
	setInstr(t, dir, "Here.", "codex", "claude", "gemini")
	if got := read(t, filepath.Join(dir, "AGENTS.override.md")); got != "Override.\n\n"+block("Here.")+"\n" {
		t.Errorf("AGENTS.override.md = %q", got)
	}
	if got := read(t, filepath.Join(dir, ".claude", "CLAUDE.md")); got != "Hidden.\n\n"+block("Here.")+"\n" {
		t.Errorf(".claude/CLAUDE.md = %q", got)
	}
	if got := read(t, filepath.Join(dir, "CONTEXT.md")); got != block("Here.")+"\n" {
		t.Errorf("CONTEXT.md = %q", got)
	}
	for _, f := range []string{"AGENTS.md", "CLAUDE.md", "GEMINI.md"} {
		gone(t, filepath.Join(dir, f))
	}
}

// A file of the user's written with CRLF keeps its line ends.
func TestProjectInstructionsKeepCRLF(t *testing.T) {
	_, dir := instrProject(t)
	f := filepath.Join(dir, "AGENTS.md")
	write(t, f, "Mine.\r\nMore.\r\n")
	setInstr(t, dir, "Here.", "codex")
	if got, want := read(t, f), "Mine.\r\nMore.\r\n\r\n"+strings.ReplaceAll(block("Here."), "\n", "\r\n")+"\r\n"; got != want {
		t.Errorf("AGENTS.md = %q, want %q", got, want)
	}
	ok(t)(ProjectInstructions(dir, ProjectInstr{Agents: &[]string{}}))
	if got := read(t, f); got != "Mine.\r\nMore.\r\n" {
		t.Errorf("AGENTS.md after off = %q", got)
	}
}

// A project file that is, links followed, an agent's user-wide
// instructions file is left alone and said so: the shared part and the
// project's would fight over the same lines.
func TestProjectInstructionsNeverAnAgentsOwnFile(t *testing.T) {
	h, dir := instrProject(t)
	global := filepath.Join(h, ".claude", "CLAUDE.md")
	write(t, global, "Global.\n")
	if err := os.Symlink(global, filepath.Join(dir, "CLAUDE.md")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	text := "Here."
	res, err := ProjectInstructions(dir, ProjectInstr{Text: &text, Agents: &[]string{"claude"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Problems) != 1 || res.Problems[0].What != "project:instructions" {
		t.Errorf("problems: %+v", res.Problems)
	}
	if got := read(t, global); got != "Global.\n" {
		t.Errorf("the user-wide CLAUDE.md = %q", got)
	}
}

// A project removed takes its instructions out of its files.
func TestRemoveProjectTakesItsInstructionsOut(t *testing.T) {
	_, dir := instrProject(t)
	write(t, filepath.Join(dir, "CLAUDE.md"), "Mine.\n")
	setInstr(t, dir, "Here.", "claude", "codex")
	ok(t)(RemoveProject(dir, false))
	if got := read(t, filepath.Join(dir, "CLAUDE.md")); got != "Mine.\n" {
		t.Errorf("CLAUDE.md = %q", got)
	}
	gone(t, filepath.Join(dir, "AGENTS.md"))
	for _, p := range instrView(t).Projects {
		if p.Dir == dir {
			t.Errorf("still a project: %+v", p)
		}
	}
}

// An agent magpie knows no project file of is refused.
func TestProjectInstructionsUnknownAgent(t *testing.T) {
	_, dir := instrProject(t)
	if _, err := ProjectInstructions(dir, ProjectInstr{Agents: &[]string{"dsh"}}); err == nil {
		t.Error("dsh was taken")
	}
}

// A project's instructions follow it into the repository's other git
// worktrees, as its skills and subagents do, and come out with it.
func TestProjectInstructionsFollowWorktrees(t *testing.T) {
	_, dir := instrProject(t)
	setInstr(t, dir, "Here.", "claude")

	// git worktree add ../app-wt
	wt := filepath.Join(filepath.Dir(dir), "app-wt")
	own := filepath.Join(dir, ".git", "worktrees", "app-wt")
	write(t, filepath.Join(wt, ".git"), "gitdir: "+own+"\n")
	write(t, filepath.Join(own, "gitdir"), filepath.Join(wt, ".git")+"\n")
	write(t, filepath.Join(own, "commondir"), "../..\n")
	write(t, filepath.Join(wt, "CLAUDE.md"), "The branch's own.\n")
	keepProjectTrees()
	f := filepath.Join(wt, "CLAUDE.md")
	if got := read(t, f); got != "The branch's own.\n\n"+block("Here.")+"\n" {
		t.Fatalf("the worktree's CLAUDE.md = %q", got)
	}

	ok(t)(ProjectInstructions(dir, ProjectInstr{Agents: &[]string{}}))
	if got := read(t, f); got != "The branch's own.\n" {
		t.Errorf("the worktree's CLAUDE.md after off = %q", got)
	}
}
