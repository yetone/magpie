package library

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Refeccd on X: skills in a git repository kept in groups
// (writing/blog, coding/review) went into the library with the
// repository's owner added to their names, though the library's skills by
// those names were those very skills.

// groupedRepo is a git repository with its skills in groups.
func groupedRepo(t *testing.T) (string, func(map[string]string)) {
	return gitRepo(t, map[string]string{
		"writing/blog/SKILL.md":  "---\nname: blog\ndescription: Write blog posts\n---\nv2\n",
		"writing/poem/SKILL.md":  "---\nname: poem\ndescription: Write poems\n---\nv2\n",
		"coding/review/SKILL.md": "---\nname: review\ndescription: Review code\n---\nv2\n",
	})
}

// noSuffix fails when a probe or an install gave any skill a name of its
// own, or the library has more skills than want.
func noSuffix(t *testing.T, p *Probe, want int) {
	t.Helper()
	for _, c := range p.Candidates {
		if c.As != "" {
			t.Errorf("%s would go in as %s", c.Path, c.As)
		}
	}
	l, _ := load()
	var names []string
	for _, s := range l.Skills {
		names = append(names, s.Name)
		if s.Called != "" {
			t.Errorf("%s is kept as %s", s.Called, s.Name)
		}
	}
	if len(names) != want {
		t.Errorf("the library has %v", names)
	}
}

// The library's own blog and review, grouped with the repository (its
// address in From, as the skills CLI's lock or a checkout gives it), then
// the repository added by that address: each links to it, as a GitHub
// repository's skills do, rather than going in beside itself.
func TestGitRepoLinksTracedSkills(t *testing.T) {
	sandbox(t)
	addr, _ := groupedRepo(t)
	write(t, filepath.Join(skillDir("blog"), "SKILL.md"), "---\nname: blog\ndescription: Blog posts\n---\nv1\n")
	write(t, filepath.Join(skillDir("review"), "SKILL.md"), "---\nname: review\ndescription: Code review\n---\nv1\n")
	ok(t)(change(func(l *Library) error {
		l.Skills = append(l.Skills, &Skill{Name: "blog", From: addr, Agents: []string{"claude"}}, &Skill{Name: "review", From: addr, Agents: []string{"claude"}})
		return nil
	}))

	p, err := ProbeSkills(addr)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range p.Candidates {
		if want := c.Name != "poem"; c.Links != want {
			t.Errorf("%s links: %v", c.Path, c.Links)
		}
	}
	noSuffix(t, p, 2)
	r := ok(t)(InstallSkills(addr, []string{"writing/blog", "writing/poem", "coding/review"}, []string{"claude"}))
	if !slices.Equal(r.Linked, []string{"blog", "review"}) || !slices.Equal(r.Installed, []string{"poem"}) {
		t.Fatalf("linked %v installed %v skipped %+v", r.Linked, r.Installed, r.Skipped)
	}
	noSuffix(t, p, 3)
	l, _ := load()
	for _, n := range []string{"blog", "review"} {
		s := l.skill(n)
		if s.Source == nil || s.Source.Kind != "git" || s.Source.Repo != addr || len(s.Commit) != 40 {
			t.Errorf("%s: %+v %+v", n, s, s.Source)
		}
		if got := read(t, filepath.Join(skillDir(n), "SKILL.md")); !strings.Contains(got, "v2") {
			t.Errorf("%s's SKILL.md: %q", n, got)
		}
	}
	// and they update from there, as one installed from it does
	ok(t)(UpdateSkills())
}

// A repository that put its skills in groups since: the skill the library
// has from its old folder is that skill, and follows it to its group.
func TestGitRepoSkillMovedIntoGroup(t *testing.T) {
	sandbox(t)
	addr, commit := gitRepo(t, map[string]string{
		"skills/blog/SKILL.md": "---\nname: blog\ndescription: Write blog posts\n---\nv1\n",
	})
	ok(t)(InstallSkills(addr, []string{"skills/blog"}, []string{"claude"}))
	commit(map[string]string{
		"skills/blog/SKILL.md":         gitRemoved,
		"skills/writing/blog/SKILL.md": "---\nname: blog\ndescription: Write blog posts\n---\nv2\n",
		"skills/writing/poem/SKILL.md": "---\nname: poem\ndescription: Write poems\n---\nv2\n",
	})
	p, err := ProbeSkills(addr)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(p.Candidates, func(c Candidate) bool { return c.Name == "blog" })
	if i < 0 || !p.Candidates[i].Have {
		t.Fatalf("probed as %+v", p.Candidates)
	}
	noSuffix(t, p, 1)
	ok(t)(InstallSkills(addr, []string{"skills/writing/blog", "skills/writing/poem"}, []string{"claude"}))
	noSuffix(t, p, 2)
	l, _ := load()
	if s := l.skill("blog"); s.Source.Path != "skills/writing/blog" {
		t.Errorf("blog updates from %+v", s.Source)
	}
	ok(t)(UpdateSkill("blog", false))
	if got := read(t, filepath.Join(skillDir("blog"), "SKILL.md")); !strings.Contains(got, "v2") {
		t.Errorf("blog's SKILL.md: %q", got)
	}
}

// Two skills by one name in two groups of a repository are two skills:
// the second goes in beside the first by its group's name, which tells
// them apart where the owner both have would not.
func TestSameNameInAnotherGroupTakesTheGroupsName(t *testing.T) {
	sandbox(t)
	addr, _ := gitRepo(t, map[string]string{
		"writing/notes/SKILL.md": "---\nname: notes\ndescription: Writing notes\n---\n",
		"coding/notes/SKILL.md":  "---\nname: notes\ndescription: Code notes\n---\n",
	})
	w, c := addr+"/-/tree/main/writing", addr+"/-/tree/main/coding"
	pw, err := ProbeSkills(w)
	if err != nil || len(pw.Candidates) != 1 {
		t.Fatalf("probed %s as %+v, %v", w, pw, err)
	}
	ok(t)(InstallSkills(w, []string{pw.Candidates[0].Path}, nil))
	p, err := ProbeSkills(c)
	if err != nil || len(p.Candidates) != 1 || p.Candidates[0].As != "notes-coding" {
		t.Fatalf("probed %s as %+v, %v", c, p, err)
	}
	r := ok(t)(InstallSkills(c, []string{p.Candidates[0].Path}, nil))
	if !slices.Equal(r.Installed, []string{"notes-coding"}) {
		t.Fatalf("installed %v", r.Installed)
	}
}

// What an earlier install left: review-<owner> beside the library's own
// review traced to that repository. The next change of the library puts
// them back together under review, from the repository, with the copy's
// agents; both old folders are kept with the backups.
func TestSkillBesideItselfFoldsBack(t *testing.T) {
	h := sandbox(t)
	addr, _ := groupedRepo(t)
	write(t, filepath.Join(skillDir("review"), "SKILL.md"), "---\nname: review\ndescription: Code review\n---\nv1\n")
	ok(t)(change(func(l *Library) error {
		l.Skills = append(l.Skills, &Skill{Name: "review", From: addr, Agents: []string{"claude"}})
		return nil
	}))
	// as the old install made it
	write(t, filepath.Join(skillDir("review-team"), "SKILL.md"), "---\nname: review-team\ndescription: Review code\n---\nv2\n")
	ok(t)(change(func(l *Library) error {
		src := Source{Kind: "git", Repo: addr, Path: "coding/review"}
		dir := skillDir("review-team")
		l.Skills = append(l.Skills, &Skill{Name: "review-team", Called: "review", Source: &src, Agents: []string{"codex"}, Hash: hashDir(dir), Own: ownHash(dir)})
		return nil
	}))

	l, _ := load()
	if len(l.Skills) != 1 {
		t.Fatalf("the library has %d skills", len(l.Skills))
	}
	s := l.skill("review")
	if s == nil || s.Called != "" || s.Source == nil || s.Source.Kind != "git" || s.Source.Path != "coding/review" {
		t.Fatalf("review is %+v", s)
	}
	if !slices.Equal(s.Agents, []string{"claude", "codex"}) {
		t.Errorf("review is on %v", s.Agents)
	}
	got := read(t, filepath.Join(skillDir("review"), "SKILL.md"))
	if !strings.Contains(got, "name: review\n") || !strings.Contains(got, "v2") {
		t.Errorf("review's SKILL.md: %q", got)
	}
	if _, err := os.Lstat(skillDir("review-team")); err == nil {
		t.Error("review-team is still in the library's folder")
	}
	if got := read(t, filepath.Join(h, ".codex/skills/review/SKILL.md")); !strings.Contains(got, "v2") {
		t.Errorf("codex's review: %q", got)
	}
	backups, _ := filepath.Glob(filepath.Join(BackupDir(), "*", "skills", "*"))
	replaced, _ := filepath.Glob(filepath.Join(BackupDir(), "*", "library", "skills", "*"))
	backups = append(backups, replaced...)
	var kept []string
	for _, b := range backups {
		kept = append(kept, filepath.Base(b))
	}
	slices.Sort(kept)
	if !slices.Equal(kept, []string{"review", "review-team"}) {
		t.Errorf("backups: %v", kept)
	}
}

// One beside a skill from elsewhere is another skill, and stays beside it.
func TestSkillBesideAnotherStays(t *testing.T) {
	sandbox(t)
	addr, _ := groupedRepo(t)
	write(t, filepath.Join(skillDir("review"), "SKILL.md"), "---\nname: review\ndescription: Code review\n---\nv1\n")
	write(t, filepath.Join(skillDir("review-team"), "SKILL.md"), "---\nname: review-team\ndescription: Review code\n---\nv2\n")
	ok(t)(change(func(l *Library) error {
		src := Source{Kind: "git", Repo: addr, Path: "coding/review"}
		l.Skills = append(l.Skills, &Skill{Name: "review", From: "elsewhere/skills"},
			&Skill{Name: "review-team", Called: "review", Source: &src})
		return nil
	}))
	ok(t)(change(func(l *Library) error { return nil }))
	l, _ := load()
	if len(l.Skills) != 2 || l.skill("review-team") == nil {
		t.Fatalf("the library has %d skills", len(l.Skills))
	}
}
