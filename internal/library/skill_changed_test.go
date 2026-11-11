package library

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeSkillTarball serves a repository whose skills/pdf says version.
func fakeSkillTarball(t *testing.T, version *string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(tarball(t, map[string]string{
			"skills/pdf/SKILL.md":  "---\nname: pdf\ndescription: PDFs " + *version + "\n---\n",
			"skills/pdf/forms.md":  "forms " + *version,
			"skills/docx/SKILL.md": "---\nname: docx\ndescription: Word " + *version + "\n---\n",
		}))
	}))
	t.Cleanup(srv.Close)
	old := tarballURL
	tarballURL = func(repo, ref string) string { return srv.URL + "/" + repo + "/" + ref }
	t.Cleanup(func() { tarballURL = old })
}

func skillView(t *testing.T, name string) SkillView {
	t.Helper()
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range v.Skills {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no %s on the page", name)
	return SkillView{}
}

// keptBackups is every copy of a skill the backups keep as the library's.
func keptBackups(t *testing.T, name string) []string {
	t.Helper()
	got, _ := filepath.Glob(filepath.Join(BackupDir(), "*", "library", "skills", name, "forms.md"))
	return got
}

// #1449: a skill from GitHub the user changed in the library (by hand, or
// through an agent it is linked into) is said to be changed, and an update
// asks before replacing it: one of it alone is refused unless told to
// replace, an update of every skill leaves it as it is, and once replaced
// the changed version is in the backups. An unchanged one updates as
// before, with no backup of GitHub's old files.
func TestUpdateKeepsASkillChangedHere(t *testing.T) {
	h := sandbox(t)
	version := "one"
	fakeSkillTarball(t, &version)
	ok(t)(InstallSkills("owner/repo", []string{"skills/pdf", "skills/docx"}, []string{"claude"}))
	if s := skillView(t, "pdf"); s.Edited {
		t.Fatal("pdf is said to be changed as installed")
	}
	// Finder showing the folder is no change
	write(t, filepath.Join(skillDir("pdf"), ".DS_Store"), "finder")
	if s := skillView(t, "pdf"); s.Edited {
		t.Fatal("a .DS_Store made pdf changed")
	}

	// edited through Claude Code's link, which is the library's folder
	write(t, filepath.Join(h, ".claude/skills/pdf/forms.md"), "my forms")
	if s := skillView(t, "pdf"); !s.Edited {
		t.Fatal("pdf edited here isn't said to be")
	}
	if s := skillView(t, "docx"); s.Edited {
		t.Fatal("docx is said to be changed")
	}

	version = "two"
	_, err := UpdateSkill("pdf", false)
	var edited *EditedError
	if !errors.As(err, &edited) || edited.Name != "pdf" {
		t.Fatalf("updating the edited pdf: %v", err)
	}
	if s := read(t, filepath.Join(skillDir("pdf"), "forms.md")); s != "my forms" {
		t.Fatalf("the refused update wrote over the edit: %q", s)
	}

	res, err := UpdateSkills()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Updated, []string{"docx"}) {
		t.Errorf("updated %v, want docx only", res.Updated)
	}
	if len(res.Unupdated) != 1 || res.Unupdated[0].What != "skill:pdf" || !strings.Contains(res.Unupdated[0].Error, "changed here") {
		t.Errorf("unupdated %+v", res.Unupdated)
	}
	if s := read(t, filepath.Join(skillDir("pdf"), "forms.md")); s != "my forms" {
		t.Fatalf("updating every skill wrote over the edit: %q", s)
	}
	if got := keptBackups(t, "docx"); len(got) != 0 {
		t.Errorf("docx, unchanged, was kept with the backups: %v", got)
	}

	ok(t)(UpdateSkill("pdf", true))
	if s := read(t, filepath.Join(skillDir("pdf"), "forms.md")); s != "forms two" {
		t.Fatalf("pdf replaced: %q", s)
	}
	got := keptBackups(t, "pdf")
	if len(got) != 1 || read(t, got[0]) != "my forms" {
		t.Fatalf("the edit replaced isn't in the backups: %v", got)
	}
	if s := skillView(t, "pdf"); s.Edited || s.Description != "PDFs two" {
		t.Fatalf("pdf after the update: %+v", s)
	}
	if !ours(filepath.Join(h, ".claude/skills/pdf"), "pdf") {
		t.Error("claude's link went in the update")
	}
}

// An agent given its skills as copies edits one: until a sync takes the
// edit in, the library's folder is GitHub's, and the copy is what was
// changed. An update asks all the same.
func TestUpdateKeepsACopyEditedInAnAgent(t *testing.T) {
	h := sandbox(t)
	version := "one"
	fakeSkillTarball(t, &version)
	ok(t)(InstallSkills("owner/repo", []string{"skills/pdf"}, []string{"claude"}))
	ok(t)(SetSkillHow("", HowCopy))
	p := filepath.Join(h, ".claude/skills/pdf")
	if linked(p) {
		t.Fatal("claude's pdf is still a link")
	}
	if s := skillView(t, "pdf"); s.Edited {
		t.Fatal("the copy is said to be changed as made")
	}
	later := time.Now().Add(2 * time.Second)
	write(t, filepath.Join(p, "forms.md"), "claude's forms")
	os.Chtimes(filepath.Join(p, "forms.md"), later, later)
	if s := skillView(t, "pdf"); !s.Edited {
		t.Fatal("the copy edited in Claude Code isn't said to be")
	}
	version = "two"
	var edited *EditedError
	if _, err := UpdateSkill("pdf", false); !errors.As(err, &edited) {
		t.Fatalf("updating pdf edited in Claude Code's copy: %v", err)
	}
	if s := read(t, filepath.Join(p, "forms.md")); s != "claude's forms" {
		t.Fatalf("the refused update wrote over the copy: %q", s)
	}
}

// A skill installed before magpie kept the hash of what it fetched can't
// be told changed or not: it isn't said to be, and an update keeps the
// version it replaces, in case it was.
func TestUpdateKeepsWhatItCantTell(t *testing.T) {
	sandbox(t)
	version := "one"
	fakeSkillTarball(t, &version)
	ok(t)(InstallSkills("owner/repo", []string{"skills/pdf"}, nil))
	mu.Lock()
	l, err := load()
	if err == nil {
		l.skill("pdf").Hash = ""
		err = l.save()
	}
	mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(skillDir("pdf"), "forms.md"), "maybe mine")
	if s := skillView(t, "pdf"); s.Edited {
		t.Fatal("pdf with no hash is said to be changed")
	}
	version = "two"
	ok(t)(UpdateSkill("pdf", false))
	got := keptBackups(t, "pdf")
	if len(got) != 1 || read(t, got[0]) != "maybe mine" {
		t.Fatalf("what the update replaced isn't in the backups: %v", got)
	}
}

// The page checks the skills from GitHub by itself when they weren't
// checked since magpie started, or not for checkEvery; a library with none
// from there has nothing to check.
func TestCheckDue(t *testing.T) {
	sandbox(t)
	checkedAt.Store(0)
	t.Cleanup(func() { checkedAt.Store(0) })
	version := "one"
	fakeSkillTarball(t, &version)
	if v, _ := Read(nil); v.CheckDue {
		t.Fatal("due with no skills")
	}
	ok(t)(InstallSkills("owner/repo", []string{"skills/pdf"}, nil))
	if v, _ := Read(nil); !v.CheckDue {
		t.Fatal("not due before any check")
	}
	checkedAt.Store(time.Now().Add(-time.Hour).UnixNano())
	if v, _ := Read(nil); v.CheckDue {
		t.Fatal("due an hour after a check")
	}
	checkedAt.Store(time.Now().Add(-checkEvery - time.Minute).UnixNano())
	if v, _ := Read(nil); !v.CheckDue {
		t.Fatal("not due a day after a check")
	}
}

// A check says it was made: the page doesn't check again as it opens.
func TestCheckSkillsMarksItDone(t *testing.T) {
	sandbox(t)
	checkedAt.Store(0)
	t.Cleanup(func() { checkedAt.Store(0) })
	fakeSkillRepo(t, map[string]string{"skills/pdf/SKILL.md": "---\nname: pdf\ndescription: PDFs\n---\n"})
	ok(t)(InstallSkills("owner/marks", []string{"skills/pdf"}, nil))
	if _, err := CheckSkills(); err != nil {
		t.Fatal(err)
	}
	if v, _ := Read(nil); v.CheckDue {
		t.Fatal("due right after a check")
	}
}

// A copy with a folder that can't be read can't be told unchanged (#1102):
// an edit may be in it. The skill's edit state is then unknown, so an
// update keeps the library's version aside, and the sync keeps the copy.
func TestEditStateUnknownForAnUnreadableCopy(t *testing.T) {
	h := sandbox(t)
	version := "one"
	fakeSkillTarball(t, &version)
	ok(t)(InstallSkills("owner/repo", []string{"skills/pdf"}, []string{"claude"}))
	ok(t)(SetSkillHow("", HowCopy))
	p := filepath.Join(h, ".claude/skills/pdf")
	if linked(p) {
		t.Fatal("claude's pdf is still a link")
	}
	edit := filepath.Join(p, "scripts/run.sh")
	write(t, edit, "echo mine\n")
	later := time.Now().Add(2 * time.Second)
	os.Chtimes(edit, later, later)
	blockSkillSubtree(t, h, filepath.Join(p, "scripts"))
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	s := l.skill("pdf")
	if s == nil {
		t.Fatal("no pdf in the library")
	}
	if edited, known := l.editState(s, Targets()); edited || known {
		t.Fatalf("an unreadable copy's state: edited %v, known %v; want unknown", edited, known)
	}
}

// An update of one skill says whether it brought anything: the GUI tells
// "updated" from "already up to date" by Current (zhexuanliuoutl1 on X).
func TestUpdateSkillSaysWhenNothingChanged(t *testing.T) {
	sandbox(t)
	version := "one"
	fakeSkillTarball(t, &version)
	ok(t)(InstallSkills("owner/current", []string{"skills/pdf"}, []string{"claude"}))
	if res := ok(t)(UpdateSkill("pdf", false)); !slices.Equal(res.Current, []string{"pdf"}) {
		t.Errorf("nothing new on GitHub: current %v", res.Current)
	}
	version = "two"
	if res := ok(t)(UpdateSkill("pdf", false)); len(res.Current) != 0 {
		t.Errorf("a changed skill is said to be current: %v", res.Current)
	}
	if res := ok(t)(UpdateSkill("pdf", false)); !slices.Equal(res.Current, []string{"pdf"}) {
		t.Errorf("updated again: current %v", res.Current)
	}
}
