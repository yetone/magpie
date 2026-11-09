package library

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// biubiuRepo serves biubiu/skills with a pdf-pro skill in skills/, as
// GitHub has it now.
func biubiuRepo(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/biubiu/skills/") {
			http.NotFound(w, r)
			return
		}
		w.Write(tarball(t, map[string]string{
			"skills/pdf-pro/SKILL.md": "---\nname: pdf-pro\ndescription: Fill and read PDFs\n---\nv2\n",
			"skills/pdf-pro/forms.md": "forms v2",
		}))
	}))
	t.Cleanup(srv.Close)
	old := tarballURL
	tarballURL = func(repo, ref string) string { return srv.URL + "/" + repo + "/" + ref }
	t.Cleanup(func() { tarballURL = old })
}

// linkedToGitHub checks that the library's pdf-pro is the one from
// biubiu/skills now, with GitHub's files in its own folder, and that
// it's the only pdf-pro: none went in beside it.
func linkedToGitHub(t *testing.T, h string) {
	t.Helper()
	l, _ := load()
	s := l.skill("pdf-pro")
	if s == nil || s.Source == nil || s.Source.Kind != "github" || s.Source.Repo != "biubiu/skills" || s.Source.Path != "skills/pdf-pro" {
		t.Fatalf("the library's pdf-pro is %+v", s)
	}
	if len(l.Skills) != 1 {
		t.Errorf("the library has %d skills", len(l.Skills))
	}
	if fi, err := os.Lstat(skillDir("pdf-pro")); err != nil || linkEntry(fi) {
		t.Errorf("the library's pdf-pro is a link still: %v", err)
	}
	if got := read(t, filepath.Join(skillDir("pdf-pro"), "forms.md")); got != "forms v2" {
		t.Errorf("the library's forms.md: %q", got)
	}
	// the agent's link goes on pointing at it
	if got := read(t, filepath.Join(h, ".claude/skills/pdf-pro/SKILL.md")); !strings.Contains(got, "v2") {
		t.Errorf("claude's pdf-pro: %q", got)
	}
	// and it updates from GitHub like one installed from there
	ok(t)(UpdateSkill("pdf-pro", false))
}

// biubiu8802 on X: a skill downloaded from GitHub and added to the
// library from its folder couldn't follow GitHub. Installing it from
// GitHub links the library's to it: GitHub's files take its place and it
// updates from there, and the user's folder is left as it is.
func TestFolderSkillFollowsGitHub(t *testing.T) {
	h := sandbox(t)
	biubiuRepo(t)
	dl := filepath.Join(h, "Downloads", "pdf-pro")
	write(t, filepath.Join(dl, "SKILL.md"), "---\nname: pdf-pro\ndescription: Fill and read PDFs\n---\nv1\n")
	ok(t)(InstallSkills(dl, []string{""}, []string{"claude"}))

	p, err := ProbeSkills("biubiu/skills")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Candidates) != 1 || !p.Candidates[0].Links || p.Candidates[0].Have || p.Candidates[0].As != "" {
		t.Fatalf("probed as %+v", p.Candidates)
	}
	r := ok(t)(InstallSkills("biubiu/skills", []string{"skills/pdf-pro"}, []string{"claude"}))
	if !slices.Equal(r.Linked, []string{"pdf-pro"}) || len(r.LinkedKept) != 0 || len(r.Installed) != 0 || len(r.Skipped) != 0 {
		t.Fatalf("linked %v kept %v installed %v skipped %+v", r.Linked, r.LinkedKept, r.Installed, r.Skipped)
	}
	linkedToGitHub(t, h)
	if got := read(t, filepath.Join(dl, "SKILL.md")); !strings.Contains(got, "v1") {
		t.Errorf("the user's folder changed: %q", got)
	}
}

// One taken into the library from an agent's folder is the library's own
// copy: linked to GitHub, its files that aren't GitHub's go to the
// backups, and the result says so.
func TestOwnSkillFollowsGitHubKeepsItsFiles(t *testing.T) {
	h := sandbox(t)
	biubiuRepo(t)
	write(t, filepath.Join(h, ".claude/skills/pdf-pro/SKILL.md"), "---\nname: pdf-pro\ndescription: Fill and read PDFs\n---\nmy edit\n")
	ok(t)(ImportSkill("pdf-pro"))

	r := ok(t)(InstallSkills("biubiu/skills", []string{"skills/pdf-pro"}, nil))
	if !slices.Equal(r.Linked, []string{"pdf-pro"}) || !slices.Equal(r.LinkedKept, []string{"pdf-pro"}) {
		t.Fatalf("linked %v kept %v", r.Linked, r.LinkedKept)
	}
	linkedToGitHub(t, h)
	kept, _ := filepath.Glob(filepath.Join(BackupDir(), "*", "library", "skills", "pdf-pro", "SKILL.md"))
	if len(kept) != 1 || !strings.Contains(read(t, kept[0]), "my edit") {
		t.Errorf("the user's own version wasn't kept: %v", kept)
	}
}

// One known to come from another repository is another skill by that
// name, even when it says it does the same: it is left as it is, and
// GitHub's goes in beside it. One traced nowhere that describes something
// else is TestInstallSetPutsAnotherByTheSameNameBeside's.
func TestSkillFromElsewhereIsNotLinked(t *testing.T) {
	h := sandbox(t)
	biubiuRepo(t)
	dl := filepath.Join(h, "Downloads", "pdf-pro")
	write(t, filepath.Join(dl, "SKILL.md"), "---\nname: pdf-pro\ndescription: Fill and read PDFs\n---\nsomeone else's\n")
	ok(t)(InstallSkills(dl, []string{""}, nil))
	mu.Lock()
	l, _ := load()
	l.skill("pdf-pro").From = "acme/skills"
	l.save()
	mu.Unlock()

	r := ok(t)(InstallSkills("biubiu/skills", []string{"skills/pdf-pro"}, nil))
	if len(r.Linked) != 0 || !slices.Equal(r.Installed, []string{"pdf-pro-biubiu"}) {
		t.Fatalf("linked %v installed %v", r.Linked, r.Installed)
	}
	l, _ = load()
	if s := l.skill("pdf-pro"); s.Source == nil || s.Source.Kind != "folder" {
		t.Errorf("the folder's pdf-pro is %+v", s.Source)
	}
}
