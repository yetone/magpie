package library

import (
	"os"
	"path/filepath"
	"slices"
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
	now := time.Now()

	// off, the default: nothing is fetched
	if res, err := AutoUpdateTick(now); err != nil || res != nil || notes() != "one" {
		t.Fatalf("off: %+v %v, notes %q", res, err, notes())
	}

	if _, err := SetAutoUpdateSkills(true); err != nil {
		t.Fatal(err)
	}
	if v, err := Read(nil); err != nil || !v.AutoUpdateSkills {
		t.Fatalf("the view doesn't say it is on: %v", err)
	}
	res, err := AutoUpdateTick(now)
	if err != nil || res == nil || !slices.Equal(res.Updated, []string{"pdf"}) || notes() != "two" {
		t.Fatalf("on: %+v %v, notes %q", res, err, notes())
	}

	// not due again within checkEvery
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
