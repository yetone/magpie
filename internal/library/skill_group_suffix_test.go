package library

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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

// A copy an earlier magpie left beside itself (review-team) is folded
// back on the next read of the library, with no change made: upgrading
// alone had left it there (Refeccd on X, "并没有修好").
func TestSkillBesideItselfFoldsOnRead(t *testing.T) {
	h := sandbox(t)
	addr, _ := groupedRepo(t)
	write(t, filepath.Join(skillDir("review"), "SKILL.md"), "---\nname: review\ndescription: Code review\n---\nv1\n")
	write(t, filepath.Join(skillDir("review-team"), "SKILL.md"), "---\nname: review-team\ndescription: Review code\n---\nv2\n")
	// saved as the old install left it, not through a change (which folds)
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	src := Source{Kind: "git", Repo: addr, Path: "coding/review"}
	dir := skillDir("review-team")
	l.Skills = append(l.Skills, &Skill{Name: "review", From: addr, Agents: []string{"claude"}},
		&Skill{Name: "review-team", Called: "review", Source: &src, Agents: []string{"codex"}, Hash: hashDir(dir), Own: ownHash(dir)})
	if err := l.save(); err != nil {
		t.Fatal(err)
	}

	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range v.Skills {
		names = append(names, s.Name)
	}
	if !slices.Equal(names, []string{"review"}) {
		t.Fatalf("the page shows %v", names)
	}
	l, _ = load()
	if s := l.skill("review"); s == nil || s.Source == nil || s.Source.Path != "coding/review" || !slices.Equal(s.Agents, []string{"claude", "codex"}) {
		t.Fatalf("review is %+v", s)
	}
	if _, err := os.Lstat(skillDir("review-team")); err == nil {
		t.Error("review-team is still in the library's folder")
	}
	// the agents follow in the same read
	if got := read(t, filepath.Join(h, ".codex/skills/review/SKILL.md")); !strings.Contains(got, "v2") {
		t.Errorf("codex's review: %q", got)
	}
	if _, err := os.Lstat(filepath.Join(h, ".codex/skills/review-team")); err == nil {
		t.Error("codex still has review-team")
	}
}

// gitHubBothWays serves files as refeccd/grouped-skills' tarball, and
// maps git@github.com:refeccd/grouped-skills.git to a git repository of
// the same files. The repository is these tests' own: tooBig remembers a
// repository for the whole run, and other tests mark owner/repo too big.
func gitHubBothWays(t *testing.T, files map[string]string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(tarball(t, files))
	}))
	t.Cleanup(srv.Close)
	old := tarballURL
	tarballURL = func(repo, ref string) string { return srv.URL + "/" + repo + "/" + ref }
	t.Cleanup(func() { tarballURL = old })
	addr, _ := gitRepo(t, files)
	ssh := "git@github.com:refeccd/grouped-skills.git"
	write(t, os.Getenv("GIT_CONFIG_GLOBAL"), "[url \""+addr+"\"]\n\tinsteadOf = "+ssh+"\n")
	return ssh
}

var groupedFiles = map[string]string{
	"skills/writing/blog/SKILL.md":  "---\nname: blog\ndescription: Write blog posts\n---\nv1\n",
	"skills/coding/review/SKILL.md": "---\nname: review\ndescription: Review code\n---\nv1\n",
}

// A GitHub repository added as owner/repo, then by its git address: the
// same repository, whose skills the library has, not others by their
// names (they went in as blog-refeccd and review-refeccd).
func TestGitHubRepoByGitAddressIsHad(t *testing.T) {
	sandbox(t)
	ssh := gitHubBothWays(t, groupedFiles)
	paths := []string{"skills/writing/blog", "skills/coding/review"}
	ok(t)(InstallSkills("refeccd/grouped-skills", paths, []string{"claude"}))
	p, err := ProbeSkills(ssh)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range p.Candidates {
		if !c.Have {
			t.Errorf("%s isn't had", c.Path)
		}
	}
	noSuffix(t, p, 2)
	r := ok(t)(InstallSkills(ssh, paths, []string{"claude"}))
	if len(r.Installed) != 0 || len(r.Had) != 2 {
		t.Errorf("installed %v had %v", r.Installed, r.Had)
	}
	noSuffix(t, p, 2)
}

// What that left before: blog-refeccd, from the git address, beside blog
// from refeccd/grouped-skills. They are one skill, and fold back on the next read.
func TestGitHubCopyByGitAddressFoldsBack(t *testing.T) {
	sandbox(t)
	ssh := gitHubBothWays(t, groupedFiles)
	ok(t)(InstallSkills("refeccd/grouped-skills", []string{"skills/writing/blog"}, []string{"claude"}))
	write(t, filepath.Join(skillDir("blog-refeccd"), "SKILL.md"), "---\nname: blog-refeccd\ndescription: Write blog posts\n---\nv1\n")
	l, _ := load()
	src := Source{Kind: "git", Repo: ssh, Path: "skills/writing/blog"}
	dir := skillDir("blog-refeccd")
	l.Skills = append(l.Skills, &Skill{Name: "blog-refeccd", Called: "blog", Source: &src, Agents: []string{"codex"}, Hash: hashDir(dir), Own: ownHash(dir)})
	if err := l.save(); err != nil {
		t.Fatal(err)
	}

	if _, err := Read(nil); err != nil {
		t.Fatal(err)
	}
	l, _ = load()
	if len(l.Skills) != 1 {
		t.Fatalf("the library has %d skills", len(l.Skills))
	}
	if s := l.skill("blog"); s == nil || s.Called != "" || !slices.Equal(s.Agents, []string{"claude", "codex"}) {
		t.Fatalf("blog is %+v", s)
	}
}

// A checkout whose skills are four folders into it (skills/team/writing/
// poem), linked into an agent and taken into the library, then its
// repository added: each skill is traced to the checkout's origin and
// links, the one the user edited too, rather than going in beside itself.
func TestCheckoutSkillFourDeepIsTraced(t *testing.T) {
	h := sandbox(t)
	const depth = "skills/team/writing"
	addr, _ := gitRepo(t, map[string]string{
		depth + "/blog/SKILL.md": "---\nname: blog\ndescription: Write blog posts\n---\nv1\n",
		depth + "/poem/SKILL.md": "---\nname: poem\ndescription: Write poems\n---\nv1\n",
	})
	pretty := "https://git.example.com/team/sub/skills.git"
	write(t, os.Getenv("GIT_CONFIG_GLOBAL"), "[url \""+addr+"\"]\n\tinsteadOf = "+pretty+"\n")
	co := filepath.Join(h, "code", "skills")
	if out, err := exec.Command(realGit, "clone", "-q", addr, co).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	if out, err := exec.Command(realGit, "-C", co, "remote", "set-url", "origin", pretty).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	// edited, so its description is no longer the repository's
	write(t, filepath.Join(co, depth, "poem", "SKILL.md"), "---\nname: poem\ndescription: my poems\n---\nmine\n")
	for _, n := range []string{"blog", "poem"} {
		if err := os.MkdirAll(filepath.Join(h, ".claude", "skills"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(co, depth, n), filepath.Join(h, ".claude", "skills", n)); err != nil {
			t.Skip("no symlinks here:", err)
		}
	}
	ok(t)(ImportSkills([]string{"blog", "poem"}))
	p, err := ProbeSkills(pretty)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range p.Candidates {
		if !c.Links {
			t.Errorf("%s doesn't link", c.Path)
		}
	}
	noSuffix(t, p, 2)
	r := ok(t)(InstallSkills(pretty, []string{depth + "/blog", depth + "/poem"}, []string{"claude"}))
	if !slices.Equal(r.Linked, []string{"blog", "poem"}) {
		t.Errorf("linked %v installed %v", r.Linked, r.Installed)
	}
	noSuffix(t, p, 2)
}
