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

// cavemanRepo serves a repository of three skills, as GitHub's tarball.
func cavemanRepo(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(tarball(t, map[string]string{
			"skills/caveman/SKILL.md":        "---\nname: caveman\ndescription: Talk like caveman\n---\n",
			"skills/caveman-commit/SKILL.md": "---\nname: caveman-commit\ndescription: Commit like caveman\n---\n",
			"skills/caveman-review/SKILL.md": "---\nname: caveman-review\ndescription: Review like caveman\n---\n",
		}))
	}))
	t.Cleanup(srv.Close)
	old := tarballURL
	tarballURL = func(repo, ref string) string { return srv.URL + "/" + repo + "/" + ref }
	t.Cleanup(func() { tarballURL = old })
}

func skipped(r *Result) []string {
	var out []string
	for _, p := range r.Skipped {
		out = append(out, strings.TrimPrefix(p.What, "skill:"))
	}
	return out
}

// lc on Discord (安装skills的时候，没有跳过已经安装的): installing a set
// of skills stopped at the first one the library's folder already had
// ("the library's folder already has a caveman-commit in it"), and none
// of the set was installed. The rest of the set is installed; the one in
// the way is skipped and said, its folder as the user left it.
func TestInstallSetSkipsOneInTheWay(t *testing.T) {
	sandbox(t)
	cavemanRepo(t)
	// caveman installed before, then edited by the user
	ok(t)(InstallSkills("owner/caveman", []string{"skills/caveman"}, []string{"claude"}))
	edited := "---\nname: caveman\ndescription: mine now\n---\n"
	write(t, filepath.Join(skillDir("caveman"), "SKILL.md"), edited)
	// a caveman-commit of the user's own in the library's folder, not listed
	mine := "---\nname: caveman-commit\ndescription: my own\n---\n"
	write(t, filepath.Join(skillDir("caveman-commit"), "SKILL.md"), mine)
	write(t, filepath.Join(skillDir("caveman-commit"), "notes.md"), "keep me")

	r := ok(t)(InstallSkills("owner/caveman", []string{"skills/caveman", "skills/caveman-commit", "skills/caveman-review"}, []string{"claude"}))
	if !slices.Equal(r.Installed, []string{"caveman-review"}) {
		t.Errorf("installed %v", r.Installed)
	}
	if !slices.Equal(r.Had, []string{"caveman"}) {
		t.Errorf("had %v", r.Had)
	}
	if !slices.Equal(skipped(r), []string{"caveman-commit"}) || !strings.Contains(r.Skipped[0].Error, "left as it is") {
		t.Errorf("skipped %+v", r.Skipped)
	}
	if got := read(t, filepath.Join(skillDir("caveman"), "SKILL.md")); got != edited {
		t.Errorf("the user's edit to caveman was written over: %q", got)
	}
	if got := read(t, filepath.Join(skillDir("caveman-commit"), "SKILL.md")); got != mine {
		t.Errorf("the user's caveman-commit was written over: %q", got)
	}
	if got := read(t, filepath.Join(skillDir("caveman-commit"), "notes.md")); got != "keep me" {
		t.Errorf("the user's caveman-commit lost a file: %q", got)
	}
	l, _ := load()
	if l.skill("caveman-review") == nil {
		t.Error("caveman-review isn't in the library")
	}
	if l.skill("caveman-commit") != nil {
		t.Error("the user's own caveman-commit was listed as the repository's")
	}

	// a set whose every skill is had already is no error
	r = ok(t)(InstallSkills("owner/caveman", []string{"skills/caveman", "skills/caveman-review"}, []string{"claude"}))
	if len(r.Installed) != 0 || len(r.Had) != 2 {
		t.Errorf("again: installed %v had %v", r.Installed, r.Had)
	}
	// one skill alone that can't be installed still fails, saying why
	if _, err := InstallSkills("owner/caveman", []string{"skills/caveman-commit"}, nil); err == nil || !strings.Contains(err.Error(), "left as it is") {
		t.Errorf("alone: %v", err)
	}
	// the market's install of one the library has is had, not an error
	r = ok(t)(InstallMarketSkill("owner/caveman", "caveman", []string{"claude"}))
	if !slices.Equal(r.Had, []string{"caveman"}) {
		t.Errorf("market: had %v", r.Had)
	}
}

// An install that copied a skill and then stopped before the library was
// saved left its folder unlisted, in the way of every later try. A folder
// holding exactly the skill's files is listed where it is; nothing in it
// is written.
func TestInstallListsItsOwnLeftoverFolder(t *testing.T) {
	sandbox(t)
	cavemanRepo(t)
	p, err := ProbeSkills("owner/caveman")
	if err != nil {
		t.Fatal(err)
	}
	if err := copyDir(filepath.Join(p.root, "skills", "caveman-commit"), skillDir("caveman-commit")); err != nil {
		t.Fatal(err)
	}
	r := ok(t)(InstallSkills("owner/caveman", []string{"skills/caveman-commit", "skills/caveman-review"}, []string{"claude"}))
	if !slices.Equal(r.Installed, []string{"caveman-review"}) || !slices.Equal(r.Had, []string{"caveman-commit"}) || len(r.Skipped) != 0 {
		t.Errorf("installed %v had %v skipped %+v", r.Installed, r.Had, r.Skipped)
	}
	l, _ := load()
	s := l.skill("caveman-commit")
	if s == nil || s.Source == nil || s.Source.Repo != "owner/caveman" || s.Source.Path != "skills/caveman-commit" || s.Hash == "" {
		t.Fatalf("caveman-commit: %+v", s)
	}
	if !ours(filepath.Join(os.Getenv("HOME"), ".claude/skills/caveman-commit"), "caveman-commit") {
		t.Error("claude wasn't given caveman-commit")
	}
}

// A skill by that name the library lists from somewhere else is the
// user's: left as it is and said, the rest installed.
func TestInstallSetSkipsAnotherByTheSameName(t *testing.T) {
	h := sandbox(t)
	cavemanRepo(t)
	src := filepath.Join(h, "src")
	skill(t, filepath.Join(src, "caveman"), "caveman", "Another caveman")
	ok(t)(InstallSkills(src, []string{"caveman"}, []string{"claude"}))

	r := ok(t)(InstallSkills("owner/caveman", []string{"skills/caveman", "skills/caveman-review"}, []string{"claude"}))
	if !slices.Equal(r.Installed, []string{"caveman-review"}) || !slices.Equal(skipped(r), []string{"caveman"}) {
		t.Errorf("installed %v skipped %+v", r.Installed, r.Skipped)
	}
	l, _ := load()
	if s := l.skill("caveman"); s == nil || s.Source == nil || s.Source.Kind != "folder" {
		t.Errorf("caveman is now %+v", s)
	}
	if got := read(t, filepath.Join(skillDir("caveman"), "SKILL.md")); !strings.Contains(got, "Another caveman") {
		t.Errorf("caveman's SKILL.md is now %q", got)
	}
}
