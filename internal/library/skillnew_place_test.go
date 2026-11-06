package library

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// refeccd (Discord): skills added from a git repository or a folder were
// only ever updated in place; ones the repository or folder added since
// never reached the library. A check now offers them, as it does GitHub's.

func TestCheckSkillsFindsNewInGitRepo(t *testing.T) {
	sandbox(t)
	addr, commit := gitRepo(t, map[string]string{
		"skills/pdf/SKILL.md":    "---\nname: pdf\ndescription: PDFs\n---\n",
		"skills/review/SKILL.md": "---\nname: review\ndescription: Reviews\n---\n",
		"README.md":              "hi",
	})
	// pdf picked, review shown and left
	ok(t)(InstallSkills(addr, []string{"skills/pdf"}, []string{"claude", "codex"}))
	check(t)
	if got := newNames(t); len(got) != 0 {
		t.Fatalf("offered what the picker showed already: %v", got)
	}

	commit(map[string]string{
		"skills/slides/SKILL.md": "---\nname: slides\ndescription: Slides\n---\n",
		"skills/notes/SKILL.md":  "---\nname: notes\ndescription: Notes\n---\n",
	})
	check(t)
	if got := newNames(t); !slices.Equal(got, []string{"notes", "slides"}) {
		t.Fatalf("new: %v", got)
	}
	v, _ := Read(nil)
	n := v.NewSkills[1]
	if n.Kind != "git" || n.Repo != addr || n.Path != "skills/slides" || n.Description != "Slides" ||
		!slices.Equal(n.Agents, []string{"claude", "codex"}) {
		t.Errorf("new skill: %+v", n)
	}

	// added from the same repository, by the same address and key
	ok(t)(AddNewSkills([]string{n.ID}))
	l, _ := load()
	s := l.skill("slides")
	if s == nil || s.Source.Kind != "git" || s.Source.Repo != addr || s.Source.Path != "skills/slides" || len(s.Commit) != 40 {
		t.Fatalf("added: %+v", s)
	}
	if !slices.Equal(s.Agents, []string{"claude", "codex"}) {
		t.Errorf("added for %v", s.Agents)
	}
	if got := newNames(t); !slices.Equal(got, []string{"notes"}) {
		t.Errorf("after adding one: %v", got)
	}

	// the other set aside, and one removed: neither offered again
	if err := IgnoreNewSkills([]string{v.NewSkills[0].ID}); err != nil {
		t.Fatal(err)
	}
	ok(t)(RemoveSkills([]string{"slides"}))
	commit(map[string]string{"README.md": "changed"})
	check(t)
	if got := newNames(t); len(got) != 0 {
		t.Errorf("a set-aside or removed skill offered again: %v", got)
	}
	l, _ = load()
	if l.skill("slides") != nil || l.skill("notes") != nil {
		t.Error("a check added a skill by itself")
	}
}

func TestCheckSkillsFindsNewInFolder(t *testing.T) {
	h := sandbox(t)
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	skill(t, filepath.Join(src, "xlsx"), "xlsx", "Sheets")
	checkedAt.Store(0) // no check since magpie started
	t.Cleanup(func() { checkedAt.Store(0) })
	if v, _ := Read(nil); v.CheckFolders || checkDue(mustLoad(t)) {
		t.Fatal("a check is offered with nothing to look at")
	}
	// pdf picked, xlsx shown and left
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"claude"}))
	v, _ := Read(nil)
	if !v.CheckFolders || !checkDue(mustLoad(t)) {
		t.Fatalf("no check offered for the folder: %v", v.CheckFolders)
	}
	check(t)
	if got := newNames(t); len(got) != 0 {
		t.Fatalf("offered what the picker showed already: %v", got)
	}

	// the user puts two more there (a git pull, say), and edits pdf
	skill(t, filepath.Join(src, "slides"), "slides", "Slides")
	skill(t, filepath.Join(src, "team/notes"), "notes", "Notes")
	write(t, filepath.Join(src, "pdf/SKILL.md"), "---\nname: pdf\ndescription: Mine\n---\n")
	check(t)
	if got := newNames(t); !slices.Equal(got, []string{"notes", "slides"}) {
		t.Fatalf("new: %v", got)
	}
	v, _ = Read(nil)
	n := v.NewSkills[1]
	if n.Kind != "folder" || n.Repo != src || n.Path != "slides" || n.ID != filepath.Join(src, "slides") ||
		!slices.Equal(n.Agents, []string{"claude"}) {
		t.Errorf("new skill: %+v", n)
	}
	ok(t)(AddNewSkills([]string{n.ID}))
	l, _ := load()
	s := l.skill("slides")
	if s == nil || s.Source.Kind != "folder" || realDir(s.Source.Dir) != realDir(filepath.Join(src, "slides")) {
		t.Fatalf("added: %+v", s)
	}
	if b, _ := os.ReadFile(filepath.Join(src, "pdf/SKILL.md")); string(b) != "---\nname: pdf\ndescription: Mine\n---\n" {
		t.Errorf("the user's edit was written over: %q", b)
	}

	// set aside, removed, or a folder by a name the library has: none
	// offered again
	if err := IgnoreNewSkills([]string{v.NewSkills[0].ID}); err != nil {
		t.Fatal(err)
	}
	ok(t)(RemoveSkills([]string{"slides"}))
	skill(t, filepath.Join(src, "other/pdf"), "pdf", "Another pdf")
	check(t)
	if got := newNames(t); len(got) != 0 {
		t.Errorf("offered again: %v", got)
	}

	// one put there later is offered; with no skill linked from the
	// folder any more, nothing is, and no check is offered for it
	skill(t, filepath.Join(src, "late"), "late", "Late")
	check(t)
	if got := newNames(t); !slices.Equal(got, []string{"late"}) {
		t.Fatalf("new: %v", got)
	}
	ok(t)(RemoveSkills([]string{"pdf"}))
	if got := newNames(t); len(got) != 0 {
		t.Errorf("offered from a folder the library has no skill from: %v", got)
	}
	if v, _ := Read(nil); v.CheckFolders {
		t.Error("a check offered with no linked skill left")
	}
}
