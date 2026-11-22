package library

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// 0xYaCo on X: "skill 能自动化升级吗？". With the switch on, a skill its
// repository changed is fetched again by itself; off, it waits for the
// user; one changed here is left as it is; a look isn't made again until
// checkEvery has gone by.
func TestAutoUpdateSkills(t *testing.T) {
	sandbox(t)
	autoUpdate.Lock()
	autoUpdate.at = time.Time{}
	autoUpdate.Unlock()
	addr, commit := gitRepo(t, map[string]string{
		"skills/pdf/SKILL.md": "---\nname: pdf\ndescription: PDFs\n---\n",
		"skills/pdf/notes.md": "one",
	})
	if _, err := InstallSkills(addr, []string{"skills/pdf"}, []string{"claude"}); err != nil {
		t.Fatal(err)
	}
	notes := func() string {
		b, _ := os.ReadFile(filepath.Join(skillDir("pdf"), "notes.md"))
		return string(b)
	}
	commit(map[string]string{"skills/pdf/notes.md": "two"})

	// off, the default: nothing is fetched
	if res, err := AutoUpdateTick(time.Now()); err != nil || res != nil || notes() != "one" {
		t.Fatalf("off: %+v %v, notes %q", res, err, notes())
	}

	// switched on, it looks at once (CHENGJIAHEGOXIS on X), not at the
	// loop's next hour
	res, err := SetAutoUpdateSkills(true)
	if err != nil || res == nil || !slices.Equal(res.Updated, []string{"pdf"}) || notes() != "two" {
		t.Fatalf("switched on: %+v %v, notes %q", res, err, notes())
	}
	if v, err := Read(nil); err != nil || !v.AutoUpdateSkills {
		t.Fatalf("the view doesn't say it is on: %v", err)
	}
	now := time.Now()

	// not due again within checkEvery of that look
	commit(map[string]string{"skills/pdf/notes.md": "three"})
	if res, err := AutoUpdateTick(now.Add(time.Hour)); err != nil || res != nil || notes() != "two" {
		t.Fatalf("an hour later: %+v %v, notes %q", res, err, notes())
	}
	if res, err := AutoUpdateTick(now.Add(checkEvery)); err != nil || res == nil || notes() != "three" {
		t.Fatalf("after checkEvery: %+v %v, notes %q", res, err, notes())
	}

	// changed here: left as it is
	write(t, filepath.Join(skillDir("pdf"), "notes.md"), "mine")
	commit(map[string]string{"skills/pdf/notes.md": "four"})
	res, err = AutoUpdateTick(now.Add(2 * checkEvery))
	if err != nil || res == nil || len(res.Updated) != 0 || len(res.Unupdated) != 1 || notes() != "mine" {
		t.Fatalf("changed here: %+v %v, notes %q", res, err, notes())
	}
}

// CHENGJIAHEGOXIS on X: with the switch on, the skills kept their old
// content. A check that finds a skill changed on GitHub between two looks
// (the page's, as it opens the skills) updates it then, not checkEvery
// later, and every agent gets the new files: one linked to the library,
// and one given copies. Off, the check only says so.
func TestCheckWithAutoUpdateOnUpdatesEveryAgent(t *testing.T) {
	h := sandbox(t)
	autoUpdate.Lock()
	autoUpdate.at = time.Time{}
	autoUpdate.Unlock()
	var fmu sync.Mutex
	files := map[string]string{
		"skills/pdf/SKILL.md": "---\nname: pdf\ndescription: PDFs\n---\n",
		"skills/pdf/notes.md": "one",
	}
	sha := "p1"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmu.Lock()
		defer fmu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/tar/") {
			w.Write(tarball(t, files))
			return
		}
		if strings.Contains(r.URL.Path, "/git/trees/") {
			w.WriteHeader(404) // new skills beside it are TestCheckSkillsFindsNewOnes's
			return
		}
		fmt.Fprintf(w, `[{"sha":%q,"html_url":"https://github.com/owner/autoupdate/commit/%s","commit":{"message":"m","committer":{"date":"2026-11-22T00:00:00Z"}}}]`, sha, sha)
	}))
	t.Cleanup(srv.Close)
	oldT, oldA := tarballURL, githubAPI
	tarballURL = func(repo, ref string) string { return srv.URL + "/tar/" + repo }
	githubAPI = srv.URL
	t.Cleanup(func() { tarballURL, githubAPI = oldT, oldA })
	upstream := func(notes, commit string) {
		fmu.Lock()
		files["skills/pdf/notes.md"], sha = notes, commit
		fmu.Unlock()
	}
	agents := func() (claude, codex string) {
		return read(t, filepath.Join(h, ".claude/skills/pdf/notes.md")), read(t, filepath.Join(h, ".codex/skills/pdf/notes.md"))
	}

	// a repository of its own: probes and the API's answers are kept by it
	t.Cleanup(func() { checkedAt.Store(0) })
	ok(t)(InstallSkills("owner/autoupdate", []string{"skills/pdf"}, []string{"claude", "codex"}))
	ok(t)(SetSkillHow("codex", HowCopy))
	if linked(filepath.Join(h, ".codex/skills/pdf")) {
		t.Fatal("codex's pdf isn't a copy")
	}

	// off: the check says pdf changed, and leaves it
	upstream("two", "p2")
	if _, up, err := CheckSkillsUpdating(); err != nil || up != nil {
		t.Fatalf("off: %+v %v", up, err)
	}
	if c := lastCheck("pdf"); c == nil || c.Status != "update" {
		t.Fatalf("off, the check found %+v", c)
	}
	if claude, codex := agents(); claude != "one" || codex != "one" {
		t.Fatalf("off, the agents have %q and %q", claude, codex)
	}

	// on: the switch looks at once
	res, err := SetAutoUpdateSkills(true)
	if err != nil || res == nil || !slices.Equal(res.Updated, []string{"pdf"}) {
		t.Fatalf("switched on: %+v %v", res, err)
	}
	if claude, codex := agents(); claude != "two" || codex != "two" {
		t.Fatalf("switched on, the agents have %q and %q", claude, codex)
	}

	// changed there an hour later: the loop's look isn't due, the page's
	// check updates it
	upstream("three", "p3")
	if up, err := AutoUpdateTick(time.Now().Add(time.Hour)); err != nil || up != nil {
		t.Fatalf("a look within checkEvery: %+v %v", up, err)
	}
	found, up, err := CheckSkillsUpdating()
	if err != nil || up == nil || !slices.Equal(up.Updated, []string{"pdf"}) {
		t.Fatalf("the page's check: %+v %+v %v", found, up, err)
	}
	if claude, codex := agents(); claude != "three" || codex != "three" {
		t.Fatalf("after the page's check, the agents have %q and %q", claude, codex)
	}
	if v := skillView(t, "pdf"); v.Check != nil {
		t.Fatalf("pdf still shows an update: %+v", v.Check)
	}
}
