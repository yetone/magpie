package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var projectAgentFiles = map[string]string{
	"claude":   ".claude/agents/reviewer.md",
	"codex":    ".codex/agents/reviewer.toml",
	"opencode": ".opencode/agents/reviewer.md",
	"gemini":   ".gemini/agents/reviewer.md",
	"qwen":     ".qwen/agents/reviewer.md",
	"droid":    ".factory/droids/reviewer.md",
	"cursor":   ".cursor/agents/reviewer.md",
	"copilot":  ".github/agents/reviewer.agent.md",
}

func subagentProjects(t *testing.T) (h, a, b string) {
	h = sandbox(t)
	a, b = filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	for _, d := range []string{a, b} {
		gitInit(t, d)
		ok(t)(AddProject(d))
	}
	s := reviewer
	s.Agents = []string{}
	ok(t)(SaveSubagent("", s))
	return
}

// A project's subagents go into the folder each agent reads a project's
// from, in the shape of its user-wide ones (jinpwng on X), and into
// nothing else: not the agent's own folder, not another project, not
// the project's git.
func TestProjectSubagentsEachAgentsFolder(t *testing.T) {
	h, a, b := subagentProjects(t)
	var ids []string
	for id := range projectAgentFiles {
		ids = append(ids, id)
	}
	res := ok(t)(ProjectSubagents(a, []string{"reviewer"}, ids))
	if len(res.Problems) > 0 {
		t.Fatalf("problems: %+v", res.Problems)
	}
	for id, rel := range projectAgentFiles {
		got := read(t, filepath.Join(a, rel))
		if !strings.Contains(got, "You review code.") {
			t.Errorf("%s: %s = %q", id, rel, got)
		}
	}
	if !strings.Contains(read(t, filepath.Join(a, ".codex/agents/reviewer.toml")), "developer_instructions") {
		t.Error("Codex's role file isn't a role file")
	}
	// global and other projects untouched
	for _, rel := range projectAgentFiles {
		if _, err := os.Stat(filepath.Join(b, rel)); err == nil {
			t.Errorf("another project got %s", rel)
		}
	}
	for _, d := range []string{".claude/agents", ".codex/agents", ".gemini/agents", ".cursor/agents", ".copilot/agents", ".factory/droids", ".config/opencode/agents", ".qwen/agents"} {
		if _, err := os.Stat(filepath.Join(h, d)); err == nil {
			t.Errorf("the user-wide %s was made", d)
		}
	}
	// kept out of git, the project's own files untouched
	g, _ := gitOf(a)
	ex := read(t, filepath.Join(g.Common, "info", "exclude"))
	for _, rel := range projectAgentFiles {
		if !strings.Contains(ex, "/"+rel+"\n") {
			t.Errorf("%s isn't in info/exclude:\n%s", rel, ex)
		}
	}
	// a library edit reaches the project
	s := reviewer
	s.Prompt = "You review harder."
	s.Agents = []string{}
	ok(t)(SaveSubagent("reviewer", s))
	if !strings.Contains(read(t, filepath.Join(a, ".claude/agents/reviewer.md")), "harder") {
		t.Error("an edit of the subagent didn't reach the project")
	}
	// taking it out of one agent takes only that file
	ok(t)(ProjectSubagents(a, []string{"reviewer"}, []string{"claude"}))
	for id, rel := range projectAgentFiles {
		_, err := os.Stat(filepath.Join(a, rel))
		if (id == "claude") != (err == nil) {
			t.Errorf("%s: %s present=%v", id, rel, err == nil)
		}
	}
	// none: everything magpie made goes, folders and exclude lines too
	ok(t)(ProjectSubagents(a, []string{"reviewer"}, nil))
	for _, d := range []string{".claude", ".github", ".codex"} {
		if _, err := os.Stat(filepath.Join(a, d)); err == nil {
			t.Errorf("%s was left", d)
		}
	}
	if ex := read(t, filepath.Join(g.Common, "info", "exclude")); strings.Contains(ex, "reviewer") {
		t.Errorf("exclude keeps lines:\n%s", ex)
	}
}

// A subagent file of the user's by the same name is never written over,
// and one the user edited is theirs from then on.
func TestProjectSubagentsLeaveTheUsersFiles(t *testing.T) {
	_, a, _ := subagentProjects(t)
	own := filepath.Join(a, ".claude/agents/reviewer.md")
	write(t, own, "mine\n")
	res, err := ProjectSubagents(a, []string{"reviewer"}, []string{"claude", "gemini"})
	if err != nil {
		t.Fatal(err)
	}
	if read(t, own) != "mine\n" {
		t.Fatal("the user's file was written over")
	}
	found := false
	for _, p := range res.Problems {
		found = found || p.What == "project:subagent:reviewer"
	}
	if !found {
		t.Errorf("no problem for the user's file: %+v", res.Problems)
	}
	gem := filepath.Join(a, ".gemini/agents/reviewer.md")
	write(t, gem, read(t, gem)+"\nmy edit\n")
	ok(t)(ProjectSubagents(a, []string{"reviewer"}, nil))
	if !strings.Contains(read(t, gem), "my edit") {
		t.Error("an edited file was taken away")
	}
	if read(t, own) != "mine\n" {
		t.Error("the user's file was taken away")
	}
}

// Taking the library's subagent out, or renaming it, follows in projects;
// removing the project takes its files; an agent magpie knows no folder
// of is refused.
func TestProjectSubagentsFollowTheLibrary(t *testing.T) {
	_, a, _ := subagentProjects(t)
	if _, err := ProjectSubagents(a, []string{"reviewer"}, []string{"grok"}); err == nil {
		t.Error("an agent with no known project folder was taken")
	}
	ok(t)(ProjectSubagents(a, []string{"reviewer"}, []string{"claude"}))
	s := reviewer
	s.Name, s.Agents = "checker", []string{}
	ok(t)(SaveSubagent("reviewer", s))
	if _, err := os.Stat(filepath.Join(a, ".claude/agents/reviewer.md")); err == nil {
		t.Error("the old name stayed")
	}
	if !strings.Contains(read(t, filepath.Join(a, ".claude/agents/checker.md")), `name: "checker"`) {
		t.Error("the renamed subagent didn't follow")
	}
	ok(t)(RemoveSubagents([]string{"checker"}))
	if _, err := os.Stat(filepath.Join(a, ".claude")); err == nil {
		t.Error("removing the subagent left its file or folder")
	}
	s.Name = "again"
	ok(t)(SaveSubagent("", s))
	ok(t)(ProjectSubagents(a, []string{"again"}, []string{"claude"}))
	ok(t)(RemoveProject(a, false))
	if _, err := os.Stat(filepath.Join(a, ".claude")); err == nil {
		t.Error("removing the project left its files")
	}
}

// A project's subagents follow it into the repository's other git
// worktrees, as its skills and servers do, and come out with it.
func TestProjectSubagentsFollowWorktrees(t *testing.T) {
	_, a, _ := subagentProjects(t)
	ok(t)(ProjectSubagents(a, []string{"reviewer"}, []string{"claude"}))

	// git worktree add ../a-wt
	wt := filepath.Join(filepath.Dir(a), "a-wt")
	own := filepath.Join(a, ".git", "worktrees", "a-wt")
	write(t, filepath.Join(wt, ".git"), "gitdir: "+own+"\n")
	write(t, filepath.Join(own, "gitdir"), filepath.Join(wt, ".git")+"\n")
	write(t, filepath.Join(own, "commondir"), "../..\n")
	keepProjectTrees()
	f := filepath.Join(wt, ".claude", "agents", "reviewer.md")
	if !strings.Contains(read(t, f), "You review code.") {
		t.Fatal("the worktree hasn't the project's subagent")
	}

	ok(t)(ProjectSubagents(a, []string{"reviewer"}, nil))
	if _, err := os.Stat(f); err == nil {
		t.Error("the worktree keeps a subagent the project no longer has")
	}
}
