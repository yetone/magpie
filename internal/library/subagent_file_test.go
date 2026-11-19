package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func subagentViewOf(t *testing.T, name string) SubagentView {
	t.Helper()
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range v.Subagents {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no %s in the view", name)
	return SubagentView{}
}

// Each subagent is kept in a file of its own on this computer: an edit
// made there, by hand or by an agent, reaches the library and every agent
// ticked for it; one made on the page is written there.
func TestSubagentFileFollowed(t *testing.T) {
	h := sandbox(t)
	claude := filepath.Join(h, ".claude/agents/reviewer.md")
	s := Subagent{Name: "reviewer", Description: "Reviews a diff.", Prompt: "You review code.", Agents: []string{"claude"}}
	ok(t)(SaveSubagent("", s))
	f := filepath.Join(Dir(), "subagents/reviewer.md")
	if got := read(t, f); got != "---\nname: \"reviewer\"\ndescription: \"Reviews a diff.\"\n---\n\nYou review code.\n" {
		t.Fatalf("its file: %q", got)
	}
	if v := subagentViewOf(t, "reviewer"); v.Path != f || v.FileError != "" || v.FileHash != "" {
		t.Errorf("view: path %q error %q hash %q", v.Path, v.FileError, v.FileHash)
	}

	// edited there, as an agent would: the library and Claude Code follow
	write(t, f, "---\nname: reviewer\ndescription: Reviews a diff for bugs.\nmodel: gpt-5.5\nskills: tdd, lint\n---\n\nYou review code.\n\nReport bugs only.\n")
	ok(t)(Sync())
	l, _ := load()
	got := l.subagent("reviewer")
	if got.Description != "Reviews a diff for bugs." || got.Prompt != "You review code.\n\nReport bugs only." || got.Model != "gpt-5.5" || strings.Join(got.Skills, ",") != "lint,tdd" {
		t.Errorf("the library after the edit: %+v", got)
	}
	if c := read(t, claude); !strings.Contains(c, "Report bugs only.") || !strings.Contains(c, "Reviews a diff for bugs.") {
		t.Errorf("claude's after the edit: %q", c)
	}
	// the user's own words stay as they wrote them
	if !strings.Contains(read(t, f), "skills: tdd, lint") {
		t.Error("the edited file was written over")
	}

	// a change on the page is written there
	s.Prompt = "Be brief."
	ok(t)(SaveSubagent("reviewer", s))
	if got := read(t, f); !strings.HasSuffix(got, "---\n\nBe brief.\n") {
		t.Errorf("its file after a save: %q", got)
	}

	// renamed: the old file goes with the old name
	s.Name = "checker"
	ok(t)(SaveSubagent("reviewer", s))
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Errorf("reviewer.md after the rename: %v", err)
	}
	g := filepath.Join(Dir(), "subagents/checker.md")
	if !strings.Contains(read(t, g), "name: \"checker\"") {
		t.Errorf("checker.md: %q", read(t, g))
	}
	ok(t)(RemoveSubagents([]string{"checker"}))
	if _, err := os.Stat(g); !os.IsNotExist(err) {
		t.Errorf("checker.md after remove: %v", err)
	}
}

// An edit that can't be read as a subagent is left as it is, the agents
// keep what it said before, and the page says why; once it reads again,
// it is taken in.
func TestSubagentFileUnreadable(t *testing.T) {
	h := sandbox(t)
	claude := filepath.Join(h, ".claude/agents/reviewer.md")
	ok(t)(SaveSubagent("", Subagent{Name: "reviewer", Description: "Reviews.", Prompt: "Review.", Agents: []string{"claude"}}))
	f := filepath.Join(Dir(), "subagents/reviewer.md")
	before := read(t, claude)
	for _, bad := range []string{
		"Just a prompt, no front matter.\n",
		"---\nname: other\ndescription: x\n---\n\nhi\n",
		"---\ndescription: Reviews.\n---\n\n   \n",
	} {
		write(t, f, bad)
		ok(t)(Sync())
		if read(t, f) != bad {
			t.Errorf("%q was written over", bad)
		}
		if read(t, claude) != before {
			t.Errorf("claude's changed on %q", bad)
		}
		if v := subagentViewOf(t, "reviewer"); !strings.Contains(v.FileError, f) || v.Prompt != "Review." {
			t.Errorf("view on %q: %q %q", bad, v.FileError, v.Prompt)
		}
		// a change on the page meanwhile leaves the user's text too
		ok(t)(SubagentAgentsSet("reviewer", []string{"claude"}))
		if read(t, f) != bad {
			t.Errorf("%q was written over by a change", bad)
		}
	}
	write(t, f, "---\ndescription: Reviews.\n---\n\nReview twice.\n")
	ok(t)(Sync())
	if !strings.Contains(read(t, claude), "Review twice.") || subagentViewOf(t, "reviewer").FileError != "" {
		t.Errorf("not taken once fixed: %q", read(t, claude))
	}
	// a file the user deleted comes back
	os.Remove(f)
	ok(t)(Sync())
	if !strings.Contains(read(t, f), "Review twice.") {
		t.Errorf("not written again: %q", read(t, f))
	}
	// one edited and not taken in stays when the subagent goes
	write(t, f, "mine")
	ok(t)(RemoveSubagents([]string{"reviewer"}))
	if read(t, f) != "mine" {
		t.Error("an edited file went with the subagent")
	}
}

// Kept in a file of the user's own, in a repository say: one there
// already is what the subagent says; one not there yet is written. It
// stays when the subagent goes.
func TestSubagentOwnFile(t *testing.T) {
	h := sandbox(t)
	claude := filepath.Join(h, ".claude/agents/reviewer.md")
	mine := filepath.Join(h, "repo/agents/reviewer.md")
	write(t, mine, "---\nname: reviewer\ndescription: From the repo.\n---\n\nThe repo's prompt.\n")
	s := Subagent{Name: "reviewer", Description: "Typed.", Prompt: "Typed.", Agents: []string{"claude"}, File: "~/repo/agents/reviewer.md"}
	ok(t)(SaveSubagent("", s))
	if c := read(t, claude); !strings.Contains(c, "The repo's prompt.") || !strings.Contains(c, "From the repo.") {
		t.Errorf("claude's: %q", c)
	}
	if _, err := os.Stat(filepath.Join(Dir(), "subagents/reviewer.md")); !os.IsNotExist(err) {
		t.Errorf("the library's own file too: %v", err)
	}
	if v := subagentViewOf(t, "reviewer"); v.Path != mine || v.File != mine {
		t.Errorf("view: %q %q", v.Path, v.File)
	}
	// a git pull, as keepSubagentFiles sees it while magpie runs
	write(t, mine, "---\nname: reviewer\ndescription: From the repo.\n---\n\nPulled.\n")
	keepSubagentFiles()
	if !strings.Contains(read(t, claude), "Pulled.") {
		t.Errorf("claude's after a pull: %q", read(t, claude))
	}
	// one not there yet is written
	other := filepath.Join(h, "repo/agents/new.md")
	ok(t)(SaveSubagent("", Subagent{Name: "new", Description: "New.", Prompt: "New.", Agents: []string{}, File: other}))
	if !strings.Contains(read(t, other), "New.") {
		t.Errorf("new.md: %q", read(t, other))
	}
	ok(t)(RemoveSubagents([]string{"reviewer", "new"}))
	if !strings.Contains(read(t, mine), "Pulled.") || read(t, other) == "" {
		t.Error("removing took the user's own files")
	}

	for _, bad := range []string{"repo/x.md", filepath.Join(h, "repo/x.txt"), filepath.Join(Dir(), "x.md"), filepath.Join(h, "repo")} {
		if _, err := SaveSubagent("", Subagent{Name: "bad", Description: "x", Prompt: "x", File: bad}); err == nil {
			t.Errorf("kept in %s", bad)
		}
	}
	ok(t)(SaveSubagent("", Subagent{Name: "given", Description: "x", Prompt: "x", Agents: []string{"claude"}}))
	if _, err := SaveSubagent("", Subagent{Name: "bad", Description: "x", Prompt: "x", File: filepath.Join(h, ".claude/agents/given.md")}); err == nil {
		t.Error("kept in a file magpie writes for Claude Code")
	}
	write(t, filepath.Join(h, "repo/broken.md"), "no front matter")
	if _, err := SaveSubagent("", Subagent{Name: "broken", Description: "x", Prompt: "x", File: filepath.Join(h, "repo/broken.md")}); err == nil {
		t.Error("kept in a file that isn't a subagent")
	}
}

// The file is this computer's: a bundle carries none, and Put keeps the
// file each subagent here is kept in.
func TestSubagentFileNotCarried(t *testing.T) {
	h := sandbox(t)
	mine := filepath.Join(h, "repo/reviewer.md")
	ok(t)(SaveSubagent("", Subagent{Name: "reviewer", Description: "Reviews.", Prompt: "Review.", Agents: []string{}, File: mine}))
	b, err := Collect()
	if err != nil {
		t.Fatal(err)
	}
	if c := (*b.Subagents)[0]; c.File != "" || c.FileHash != "" {
		t.Fatalf("carried: %q %q", c.File, c.FileHash)
	}
	(*b.Subagents)[0].Prompt = "From the other computer."
	if _, err := Put(b); err != nil {
		t.Fatal(err)
	}
	l, _ := load()
	if s := l.subagent("reviewer"); s.File != mine || !strings.Contains(read(t, mine), "From the other computer.") {
		t.Errorf("after put: %+v, file %q", s, read(t, mine))
	}
}
