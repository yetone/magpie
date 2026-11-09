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

// skillCreators serves anthropics/skills and openai/skills, each with a
// skill-creator of its own, laid out as the two repositories have them:
// anthropics' and openai's in skills/. version is what openai's says it
// is.
func skillCreators(t *testing.T, version *string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/anthropics/skills/"):
			w.Write(tarball(t, map[string]string{
				"skills/skill-creator/SKILL.md":    "---\nname: skill-creator\ndescription: Guide for creating effective skills (Anthropic).\nlicense: Complete terms in LICENSE.txt\n---\n\n# Skill Creator\n",
				"skills/skill-creator/LICENSE.txt": "Apache",
			}))
		case strings.HasPrefix(r.URL.Path, "/openai/skills/"):
			w.Write(tarball(t, map[string]string{
				"skills/skill-creator/SKILL.md": "---\nname: skill-creator\ndescription: Guide for creating effective skills (OpenAI " + *version + ").\nmetadata:\n  short-description: Create or update a skill\n---\n\n# Skill Creator\n",
			}))
		case strings.HasPrefix(r.URL.Path, "/acme/skills/"):
			w.Write(tarball(t, map[string]string{
				"skill-creator/SKILL.md": "---\nname: skill-creator\ndescription: Acme's own\n---\n",
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := tarballURL
	tarballURL = func(repo, ref string) string { return srv.URL + "/" + repo + "/" + ref }
	t.Cleanup(func() { tarballURL = old })
}

// mora1n on Discord: anthropics' skill-creator and openai's are two
// skills by one name, and only the first could be installed; the second
// was "left as it is". Now the second goes in beside the first under a
// name of its own, its source's owner added, and every agent gets both.
// The first keeps its name, folder and links.
func TestSameNameFromAnotherSourceGoesInBeside(t *testing.T) {
	h := sandbox(t)
	version := "one"
	skillCreators(t, &version)

	ok(t)(InstallSkills("anthropics/skills", []string{"skills/skill-creator"}, []string{"claude"}))
	anthropic := read(t, filepath.Join(skillDir("skill-creator"), "SKILL.md"))

	p, err := ProbeSkills("openai/skills")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Candidates) != 1 || p.Candidates[0].Have || p.Candidates[0].As != "skill-creator-openai" {
		t.Fatalf("openai's probed as %+v", p.Candidates)
	}
	r := ok(t)(InstallSkills("openai/skills", []string{"skills/skill-creator"}, []string{"claude"}))
	if !slices.Equal(r.Installed, []string{"skill-creator-openai"}) || len(r.Skipped) != 0 {
		t.Fatalf("installed %v skipped %+v", r.Installed, r.Skipped)
	}

	l, _ := load()
	first, second := l.skill("skill-creator"), l.skill("skill-creator-openai")
	if first == nil || first.Source.Repo != "anthropics/skills" || first.Called != "" {
		t.Errorf("anthropics' is now %+v", first)
	}
	if second == nil || second.Source.Repo != "openai/skills" || second.Source.Path != "skills/skill-creator" || second.Called != "skill-creator" {
		t.Fatalf("openai's is %+v", second)
	}
	if got := read(t, filepath.Join(skillDir("skill-creator"), "SKILL.md")); got != anthropic {
		t.Errorf("anthropics' SKILL.md changed: %q", got)
	}
	// the agent knows it by its folder's name: two skill-creators in
	// one agent would be one hiding the other
	want := "---\nname: skill-creator-openai\ndescription: Guide for creating effective skills (OpenAI one).\nmetadata:\n  short-description: Create or update a skill\n---\n\n# Skill Creator\n"
	if got := read(t, filepath.Join(skillDir("skill-creator-openai"), "SKILL.md")); got != want {
		t.Errorf("openai's SKILL.md is %q", got)
	}
	for _, n := range []string{"skill-creator", "skill-creator-openai"} {
		if !ours(filepath.Join(h, ".claude/skills", n), n) {
			t.Errorf("claude wasn't given %s", n)
		}
	}
	// named so, it is what was fetched: not changed here, and a check
	// finds the same files at its source
	if edited, known := l.editState(second, Targets()); edited || !known {
		t.Errorf("openai's reads as edited=%v known=%v", edited, known)
	}
	if got := second.fetchedHash(filepath.Join(p.root, "skills", "skill-creator")); got == "" || got != second.Hash {
		t.Errorf("fetched hash %q, library's %q", got, second.Hash)
	}
	v, _ := Read(nil)
	for _, s := range v.Skills {
		if s.Name == "skill-creator-openai" && s.Source != "https://github.com/openai/skills/tree/HEAD/skills/skill-creator" {
			t.Errorf("openai's source reads %q", s.Source)
		}
	}

	// installed again, it is had, by the name it has here
	r = ok(t)(InstallSkills("openai/skills", []string{"skills/skill-creator"}, nil))
	if len(r.Installed) != 0 || !slices.Equal(r.Had, []string{"skill-creator-openai"}) {
		t.Errorf("again: installed %v had %v", r.Installed, r.Had)
	}
	if p, _ := ProbeSkills("openai/skills"); !p.Candidates[0].Have || p.Candidates[0].As != "skill-creator-openai" {
		t.Errorf("probed again: %+v", p.Candidates)
	}
	// the market sees which repository's it is
	l, _ = load()
	if got := l.haveSkill("openai/skills", "skill-creator", "skill-creator"); got != "skill-creator-openai" {
		t.Errorf("market has openai's as %q", got)
	}
	if got := l.haveSkill("acme/skills", "skill-creator", "skill-creator"); got != "" {
		t.Errorf("market has acme's as %q", got)
	}
	// a third from elsewhere gets its own owner's name
	if r := ok(t)(InstallMarketSkill("acme/skills", "skill-creator", []string{"claude"})); !slices.Equal(r.Installed, []string{"skill-creator-acme"}) {
		t.Errorf("acme's: installed %v skipped %+v", r.Installed, r.Skipped)
	}

	// an update keeps the name it has here, in its SKILL.md too
	version = "two"
	ok(t)(UpdateSkill("skill-creator-openai", false))
	got := read(t, filepath.Join(skillDir("skill-creator-openai"), "SKILL.md"))
	if !strings.Contains(got, "name: skill-creator-openai\n") || !strings.Contains(got, "(OpenAI two)") {
		t.Errorf("after update: %q", got)
	}
	l, _ = load()
	if edited, _ := l.editState(l.skill("skill-creator-openai"), Targets()); edited {
		t.Error("the update reads as changed here")
	}
}

// A skill taken in from an agent's folder has no source to tell it by:
// the very same files by its name are had, not installed twice.
func TestSameFilesByNameAreHad(t *testing.T) {
	sandbox(t)
	version := "one"
	skillCreators(t, &version)
	p, err := ProbeSkills("acme/skills")
	if err != nil {
		t.Fatal(err)
	}
	if err := copyDir(filepath.Join(p.root, "skill-creator"), skillDir("skill-creator")); err != nil {
		t.Fatal(err)
	}
	if _, err := change(func(l *Library) error {
		l.Skills = append(l.Skills, &Skill{Name: "skill-creator", Agents: []string{}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r := ok(t)(InstallSkills("acme/skills", []string{"skill-creator"}, nil))
	if len(r.Installed) != 0 || !slices.Equal(r.Had, []string{"skill-creator"}) {
		t.Errorf("installed %v had %v", r.Installed, r.Had)
	}
	// the one had is acme's now, and updates from there; another by that
	// name from elsewhere is another skill
	if l, _ := load(); l.skill("skill-creator").Source == nil || l.skill("skill-creator").Source.Repo != "acme/skills" {
		t.Errorf("the one had is %+v", l.skill("skill-creator").Source)
	}
	r = ok(t)(InstallSkills("openai/skills", []string{"skills/skill-creator"}, nil))
	if !slices.Equal(r.Installed, []string{"skill-creator-openai"}) {
		t.Errorf("openai's: installed %v had %v", r.Installed, r.Had)
	}
}

func TestFreeSkillName(t *testing.T) {
	sandbox(t)
	l := &Library{Skills: []*Skill{{Name: "pdf"}, {Name: "pdf-openai"}, {Name: strings.Repeat("a", 64)}}}
	gh := Source{Kind: "github", Repo: "openai/skills"}
	for _, c := range []struct {
		name string
		src  Source
		want string
	}{
		{"docx", gh, "docx"},
		{"pdf", gh, "pdf-openai-skills"},
		{"pdf", Source{Kind: "git", Repo: "git@gitlab.example.com:team/tools.git"}, "pdf-team"},
		{"pdf", Source{Kind: "folder", Dir: "/Users/me/work/kit"}, "pdf-work"},
		{strings.Repeat("a", 64), gh, strings.Repeat("a", 64-len("-openai")) + "-openai"},
	} {
		if got := l.freeSkillName(c.name, c.src); got != c.want || !nameRe.MatchString(got) {
			t.Errorf("%s from %+v: %q, want %q", c.name, c.src, got, c.want)
		}
	}
	// every other name taken: a number
	l.Skills = append(l.Skills, &Skill{Name: "pdf-openai-skills"}, &Skill{Name: "pdf-skills"})
	if got := l.freeSkillName("pdf", gh); got != "pdf-openai-2" {
		t.Errorf("all taken: %q", got)
	}
	// a folder in the library's that it doesn't list is taken too
	write(t, filepath.Join(skillDir("docx"), "SKILL.md"), "mine")
	if got := l.freeSkillName("docx", gh); got != "docx-openai" {
		t.Errorf("unlisted folder: %q", got)
	}
}

func TestRenamedMeta(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"---\nname: a\ndescription: d\n---\nbody name: a\n", "---\nname: b\ndescription: d\n---\nbody name: a\n"},
		{"---\r\nname: \"a\"\r\n---\r\n", "---\r\nname: b\r\n---\r\n"},
		// a nested name isn't the skill's
		{"---\nmetadata:\n  name: a\nname: a\n---\n", "---\nmetadata:\n  name: a\nname: b\n---\n"},
		{"---\ndescription: d\n---\nname: a\n", "---\ndescription: d\n---\nname: a\n"},
		{"# no front matter\nname: a\n", "# no front matter\nname: a\n"},
	} {
		got, _ := renamedMeta(c.in, "b")
		if got != c.want {
			t.Errorf("%q: %q, want %q", c.in, got, c.want)
		}
	}
	// a SKILL.md that is a link isn't written through
	dir := t.TempDir()
	other := filepath.Join(dir, "other.md")
	write(t, other, "---\nname: a\n---\n")
	if err := os.MkdirAll(filepath.Join(dir, "s"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(dir, "s", "SKILL.md")); err != nil {
		t.Skip(err)
	}
	if err := nameSkill(filepath.Join(dir, "s"), "b"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, other); got != "---\nname: a\n---\n" {
		t.Errorf("written through the link: %q", got)
	}
}
