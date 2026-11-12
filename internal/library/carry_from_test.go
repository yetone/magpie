package library

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// ConatusMoon2222 on X: skills grouped by their library on one computer
// land in "this computer" on another after a sync, and Group again doesn't
// bring them back. What traced them (the skills CLI's lock, a checkout) is
// the first computer's own files, and the bundle carried neither it nor
// the From a trace made, while Put also wrote every skill new and lost
// what Group again had found here.

// onA makes a library on one computer whose skills are traced by the
// skills CLI's lock, and gives its bundle.
func onA(t *testing.T) *Bundle {
	t.Helper()
	h := sandbox(t)
	skill(t, filepath.Join(h, ".agents/skills/locked"), "locked", "Locked")
	write(t, filepath.Join(h, ".agents/.skill-lock.json"), `{"version":3,"skills":{"locked":{"source":"other/locked","sourceType":"github"}}}`)
	dirLink(filepath.Join(h, ".agents/skills/locked"), filepath.Join(h, ".claude/skills/locked"))
	skill(t, filepath.Join(h, ".claude/skills/mine"), "mine", "Mine")
	ok(t)(ImportSkills([]string{"locked", "mine"}))
	if r := repoIn(t)["locked"]; r != "other/locked" {
		t.Fatalf("A doesn't group locked: %q", r)
	}
	b, err := Collect()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSyncedSkillKeepsItsRepo(t *testing.T) {
	b := onA(t)
	sandbox(t) // the other computer: no lock, no checkout
	ok(t)(Put(b))
	got := repoIn(t)
	if got["locked"] != "other/locked" || got["mine"] != "" {
		t.Errorf("on B: %v", got)
	}
	// and it goes on to a third computer from B, whose files trace nothing
	b2, err := Collect()
	if err != nil {
		t.Fatal(err)
	}
	sandbox(t)
	ok(t)(Put(b2))
	if r := repoIn(t)["locked"]; r != "other/locked" {
		t.Errorf("on C: %q", r)
	}
}

// A bundle from before From was carried says nothing of it: what this
// computer found for the skill (Group again) stays through the sync.
func TestSyncKeepsRepoFoundHere(t *testing.T) {
	b := onA(t)
	// as a magpie before this made it: the same JSON without the field
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.ReplaceAll(string(raw), `"from":"other/locked",`, ""))
	b = &Bundle{}
	if err := json.Unmarshal(raw, b); err != nil {
		t.Fatal(err)
	}
	sandbox(t)
	ok(t)(Put(b))
	l := mustLoad(t)
	l.skill("locked").From = "other/locked" // Group again, here
	if err := l.save(); err != nil {
		t.Fatal(err)
	}
	ok(t)(Put(b))
	if r := repoIn(t)["locked"]; r != "other/locked" {
		t.Errorf("after the next sync: %q", r)
	}
}

// Synced before From was carried: a skill of the pack that came with its
// source still names the repository, and Group again groups the rest.
func TestRegroupAfterOldSync(t *testing.T) {
	h := sandbox(t)
	pack := map[string]string{
		"skills/a/SKILL.md": "---\nname: a\ndescription: A\n---\nA\n",
		"skills/b/SKILL.md": "---\nname: b\ndescription: B\n---\nB\n",
	}
	tar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.URL.Path, "/tar/") != "someone/pack" {
			http.NotFound(w, r)
			return
		}
		w.Write(tarball(t, pack))
	}))
	t.Cleanup(tar.Close)
	oldT := tarballURL
	tarballURL = func(repo, ref string) string { return tar.URL + "/tar/" + repo }
	t.Cleanup(func() { tarballURL = oldT })

	write(t, filepath.Join(h, "x/a/SKILL.md"), pack["skills/a/SKILL.md"])
	write(t, filepath.Join(h, "x/b/SKILL.md"), pack["skills/b/SKILL.md"])
	b := &Bundle{Skills: []*CarriedSkill{
		{Name: "a", Source: &Source{Kind: "github", Repo: "someone/pack", Path: "skills/a"}, Agents: []string{}, Files: map[string][]byte{"SKILL.md": []byte(pack["skills/a/SKILL.md"])}},
		{Name: "b", Agents: []string{}, Files: map[string][]byte{"SKILL.md": []byte(pack["skills/b/SKILL.md"]), "mine.txt": []byte("x")}}, // a file of its own: grouped, not bound
	}}
	ok(t)(Put(b))
	if r := repoIn(t)["b"]; r != "" {
		t.Fatalf("b before: %q", r)
	}
	if n, err := RegroupSkills(); err != nil || n != 1 {
		t.Fatalf("regroup: %d %v", n, err)
	}
	if r := repoIn(t)["b"]; r != "someone/pack" {
		t.Errorf("b after: %q", r)
	}
	// and the next sync of the same old bundle doesn't undo it
	ok(t)(Put(b))
	if r := repoIn(t)["b"]; r != "someone/pack" {
		t.Errorf("b after a sync: %q", r)
	}
}
