package library

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// carrySource serves owner/repo, whose skills/pdf says version, and counts
// the downloads; down makes it refuse them.
func carrySource(t *testing.T, version *string) (hits *atomic.Int32, down *atomic.Bool) {
	t.Helper()
	// another test's download of owner/repo, kept for an install, isn't
	// this server's
	forgetProbes()
	t.Cleanup(forgetProbes)
	hits, down = &atomic.Int32{}, &atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "no", http.StatusServiceUnavailable)
			return
		}
		hits.Add(1)
		w.Write(tarball(t, map[string]string{
			"skills/pdf/SKILL.md": "---\nname: pdf\ndescription: PDFs " + *version + "\n---\n",
			"skills/pdf/forms.md": "forms " + *version,
		}))
	}))
	t.Cleanup(srv.Close)
	old := tarballURL
	tarballURL = func(repo, ref string) string { return srv.URL + "/" + repo + "/" + ref }
	t.Cleanup(func() { tarballURL = old })
	return hits, down
}

func carriedSkill(t *testing.T, b *Bundle, name string) *CarriedSkill {
	t.Helper()
	for _, s := range b.Skills {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no skill %s carried", name)
	return nil
}

func librarySkill(t *testing.T, name string) *Skill {
	t.Helper()
	mu.Lock()
	l, err := load()
	mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	s := l.skill(name)
	if s == nil {
		t.Fatalf("no skill %s in the library", name)
	}
	return s
}

// leaveOut is j with the named file of a skill left out of what it
// carries, as the size limit leaves a big one out.
func leaveOut(t *testing.T, j, name, file string) string {
	t.Helper()
	var b Bundle
	if err := json.Unmarshal([]byte(j), &b); err != nil {
		t.Fatal(err)
	}
	c := carriedSkill(t, &b, name)
	delete(c.Files, file)
	c.Left = append(c.Left, file)
	return mustJSON(t, &b)
}

// #5715 (JJasonsun on Discord): a skill fetched from GitHub goes to the
// other computers as its source, which each fetches itself; what of its
// files the size limit left out is fetched, never missing silently. One
// of the user's own goes as its files, as before. The second computer
// carries back the same library.
func TestSkillCarriedBySource(t *testing.T) {
	sandbox(t)
	version := "one"
	hits, _ := carrySource(t, &version)
	ok(t)(InstallSkills("owner/repo", []string{"skills/pdf"}, []string{"claude"}))
	ok(t)(CreateSkill("mine", "my own", []string{"claude"}))
	b := must(Collect())
	pdf, mine := carriedSkill(t, b, "pdf"), carriedSkill(t, b, "mine")
	if !pdf.Fetch || pdf.Hash == "" || pdf.Hash != librarySkill(t, "pdf").Hash {
		t.Fatalf("pdf isn't carried as its source: %s", mustJSON(t, pdf))
	}
	// for a magpie before this, its files as they fit
	if len(pdf.Files) != 2 {
		t.Errorf("pdf's files aren't carried for an older magpie: %v", pdf.Files)
	}
	if mine.Fetch || mine.Hash != "" || len(mine.Files) == 0 {
		t.Errorf("a skill of the user's own isn't carried as its files: %s", mustJSON(t, mine))
	}
	carried := mustJSON(t, b)

	// the whole of it carried: nothing to fetch
	sandbox(t)
	before := hits.Load()
	r := putJSON(t, carried)
	if len(r.Problems) != 0 || hits.Load() != before {
		t.Errorf("a skill carried whole was fetched (%d) or has problems: %+v", hits.Load()-before, r.Problems)
	}
	if s := librarySkill(t, "pdf"); s.Pending || s.Hash != pdf.Hash {
		t.Errorf("pdf here: %+v", s)
	}
	if back := mustJSON(t, must(Collect())); back != carried {
		t.Errorf("the second computer carries back another library:\n%s\nwas\n%s", back, carried)
	}

	// forms.md left out by the size limit: fetched from GitHub here
	sandbox(t)
	before = hits.Load()
	r = putJSON(t, leaveOut(t, carried, "pdf", "forms.md"))
	if len(r.Problems) != 0 || hits.Load() == before {
		t.Fatalf("pdf wasn't fetched (%d): %+v", hits.Load()-before, r.Problems)
	}
	if got := read(t, filepath.Join(skillDir("pdf"), "forms.md")); got != "forms one" {
		t.Errorf("forms.md here: %q", got)
	}
	if s := librarySkill(t, "pdf"); s.Pending || s.Hash != pdf.Hash {
		t.Errorf("pdf here: %+v", s)
	}
	if back := mustJSON(t, must(Collect())); back != carried {
		t.Errorf("the fetched skill carries back another library:\n%s\nwas\n%s", back, carried)
	}
}

// A computer that can't reach the source keeps the skill Pending, with
// what came standing in, says so, and carries it on as its source at the
// other computer's version; Update fetches it once it can.
func TestSkillCarriedBySourceNotFetched(t *testing.T) {
	sandbox(t)
	version := "one"
	_, down := carrySource(t, &version)
	ok(t)(InstallSkills("owner/repo", []string{"skills/pdf"}, []string{"claude"}))
	carried := mustJSON(t, must(Collect()))
	hash := librarySkill(t, "pdf").Hash

	sandbox(t)
	down.Store(true)
	r := putJSON(t, leaveOut(t, carried, "pdf", "forms.md"))
	if len(r.Problems) != 1 || r.Problems[0].What != "skill:pdf" || !strings.Contains(r.Problems[0].Error, "Update tries again") {
		t.Fatalf("problems: %+v", r.Problems)
	}
	if v := skillView(t, "pdf"); !v.Pending || v.Edited {
		t.Errorf("not shown pending: %+v", v)
	}
	if s := librarySkill(t, "pdf"); !s.Pending || s.Hash != hash {
		t.Errorf("pdf here: %+v", s)
	}
	if c := carriedSkill(t, must(Collect()), "pdf"); !c.Fetch || c.Hash != hash {
		t.Errorf("a pending skill isn't carried on as its source: %s", mustJSON(t, c))
	}
	// the next sync tries again, and so does Update
	down.Store(false)
	ok(t)(UpdateSkill("pdf", false))
	if s := librarySkill(t, "pdf"); s.Pending || s.Hash != hash {
		t.Errorf("pdf after Update: %+v", s)
	}
	if got := read(t, filepath.Join(skillDir("pdf"), "forms.md")); got != "forms one" {
		t.Errorf("forms.md after Update: %q", got)
	}
}

// A skill changed on the receiving computer isn't written over by the
// other computer's version from its source: kept, and said, while the two
// versions differ. At the same version there is nothing to say.
func TestSkillCarriedBySourceKeepsChangesHere(t *testing.T) {
	sandbox(t)
	version := "one"
	hits, _ := carrySource(t, &version)
	ok(t)(InstallSkills("owner/repo", []string{"skills/pdf"}, []string{"claude"}))
	carried := mustJSON(t, must(Collect()))

	write(t, filepath.Join(skillDir("pdf"), "forms.md"), "my forms")
	if c := carriedSkill(t, must(Collect()), "pdf"); c.Fetch || string(c.Files["forms.md"]) != "my forms" {
		t.Errorf("a skill changed here isn't carried as its files: %s", mustJSON(t, c))
	}
	r := putJSON(t, carried)
	if len(r.Problems) != 0 || read(t, filepath.Join(skillDir("pdf"), "forms.md")) != "my forms" {
		t.Errorf("the same version: %+v, forms.md %q", r.Problems, read(t, filepath.Join(skillDir("pdf"), "forms.md")))
	}

	// the other computer has another version
	var b Bundle
	if err := json.Unmarshal([]byte(carried), &b); err != nil {
		t.Fatal(err)
	}
	c := carriedSkill(t, &b, "pdf")
	c.Hash, c.Files["forms.md"] = "another", []byte("forms two")
	before := hits.Load()
	r = putJSON(t, mustJSON(t, &b))
	if len(r.Problems) != 1 || r.Problems[0].What != "skill:pdf" || !strings.Contains(r.Problems[0].Error, "changed on this computer") {
		t.Errorf("problems: %+v", r.Problems)
	}
	if got := read(t, filepath.Join(skillDir("pdf"), "forms.md")); got != "my forms" || hits.Load() != before {
		t.Errorf("the change here was written over (%q), or fetched (%d)", got, hits.Load()-before)
	}
	if v := skillView(t, "pdf"); !v.Edited || v.Pending {
		t.Errorf("pdf is no longer said to be changed here: %+v", v)
	}
}

// One at another version here, unchanged, is fetched: what was here is
// kept with the backups, as an update keeps it.
func TestSkillCarriedBySourceNewerThere(t *testing.T) {
	sandbox(t)
	version := "one"
	carrySource(t, &version)
	ok(t)(InstallSkills("owner/repo", []string{"skills/pdf"}, []string{"claude"}))
	version = "two"
	var b Bundle
	if err := json.Unmarshal([]byte(mustJSON(t, must(Collect()))), &b); err != nil {
		t.Fatal(err)
	}
	carriedSkill(t, &b, "pdf").Hash = "two's"
	r := putJSON(t, mustJSON(t, &b))
	if len(r.Problems) != 0 {
		t.Errorf("problems: %+v", r.Problems)
	}
	if got := read(t, filepath.Join(skillDir("pdf"), "forms.md")); got != "forms two" {
		t.Errorf("forms.md here: %q", got)
	}
	if s := librarySkill(t, "pdf"); s.Pending || s.Hash == "two's" || s.Hash == "" {
		t.Errorf("pdf here: %+v", s)
	}
}

// A bundle from a magpie before this carries no Fetch: the same files are
// kept, and so is the hash of what was fetched, so the skill still goes as
// its source from here.
func TestSkillOldBundleKeepsHash(t *testing.T) {
	sandbox(t)
	version := "one"
	carrySource(t, &version)
	ok(t)(InstallSkills("owner/repo", []string{"skills/pdf"}, []string{"claude"}))
	hash := librarySkill(t, "pdf").Hash
	b := must(Collect())
	c := carriedSkill(t, b, "pdf")
	c.Fetch, c.Hash, c.Commit = false, "", ""
	if j := mustJSON(t, c); strings.Contains(j, "fetch") || strings.Contains(j, "hash") {
		t.Fatalf("not as an older magpie carries it: %s", j)
	}
	putJSON(t, mustJSON(t, b))
	if s := librarySkill(t, "pdf"); s.Hash != hash {
		t.Errorf("the hash of what was fetched is lost: %+v", s)
	}
	if c := carriedSkill(t, must(Collect()), "pdf"); !c.Fetch {
		t.Errorf("no longer carried as its source")
	}
}

// A link holding a token is never synced: such a skill goes as its files,
// and a bundle naming one is put from its files, not fetched.
func TestSkillSignedInLinkGoesAsFiles(t *testing.T) {
	l := &Library{}
	for _, repo := range []string{"https://me:ghp_secret@example.com/team/skills.git", "https://ghp_secret@example.com/team/skills.git"} {
		// Pending: carried by its source whatever its folder holds, but for the link
		s := &Skill{Name: "x", Source: &Source{Kind: "git", Repo: repo}, Hash: "h", Pending: true}
		if l.bySource(s) {
			t.Errorf("%s is carried as its source", repo)
		}
		if l.fromSourceHere(&CarriedSkill{Name: "x", Source: s.Source, Fetch: true, Hash: "h"}) != nil {
			t.Errorf("%s is fetched", repo)
		}
	}
	if l.fromSourceHere(&CarriedSkill{Name: "x", Source: &Source{Kind: "github", Repo: "o/r"}, Fetch: true, Hash: "h"}) == nil {
		t.Errorf("a public GitHub source isn't fetched")
	}
}

// A file another computer left out as too big (Left) that this one
// doesn't have goes on as left out from here, until it is here: this
// computer's sync never takes it from the one that has it.
func TestSkillMissingFilesCarriedOnAsLeft(t *testing.T) {
	sandbox(t)
	ok(t)(CreateSkill("mine", "my own", []string{"claude"}))
	j := leaveOut(t, mustJSON(t, must(Collect())), "mine", "big.bin")
	sandbox(t)
	if r := putJSON(t, j); len(r.Problems) != 0 {
		t.Fatalf("problems: %+v", r.Problems)
	}
	if c := carriedSkill(t, must(Collect()), "mine"); !slices.Equal(c.Left, []string{"big.bin"}) {
		t.Fatalf("not carried on as left out: %s", mustJSON(t, c))
	}
	if back := mustJSON(t, must(Collect())); back != j {
		t.Errorf("carries back another library:\n%s\nwas\n%s", back, j)
	}
	// here now: carried as any file
	write(t, filepath.Join(skillDir("mine"), "big.bin"), "big")
	if c := carriedSkill(t, must(Collect()), "mine"); len(c.Left) != 0 || string(c.Files["big.bin"]) != "big" {
		t.Errorf("here now, still left out: %s", mustJSON(t, c))
	}
}
