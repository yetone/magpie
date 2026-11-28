package library

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// gandli on X: with many skills, every tick of one skill's agent read each
// agent's skills folder once for each skill in it, looking for the old
// copies a swap left (dropOld). A change reads each folder once, however
// many skills it holds, and still clears what was left.
func TestSkillsFolderReadOnceForOldCopies(t *testing.T) {
	h := sandbox(t)
	if err := os.MkdirAll(filepath.Join(h, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	const n = 30
	for i := range n {
		name := fmt.Sprintf("skill-%02d", i)
		if _, err := createSkill(name, "one of many", []string{"claude"}, "body\n", nil); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(h, ".claude", "skills")
	if es, _ := os.ReadDir(dir); len(es) != n {
		t.Fatalf("claude has %d skills, want %d", len(es), n)
	}
	// what a swap couldn't remove, of two skills, and one not magpie's
	left := []string{oldPrefix("skill-03"), oldPrefix("skill-03") + "-2", oldPrefix("skill-17") + "-1"}
	for _, o := range append(left, ".mine.magpie-older") {
		skill(t, filepath.Join(dir, o), "old", "Old")
	}

	real := readOlds
	t.Cleanup(func() { readOlds = real })
	var rmu sync.Mutex
	reads := map[string]int{}
	readOlds = func(d string) ([]os.DirEntry, error) {
		rmu.Lock()
		reads[d]++
		rmu.Unlock()
		return real(d)
	}
	ok(t)(Sync())
	if reads[dir] > 1 {
		t.Errorf("a sync read claude's folder %d times for old copies, once a skill; want once", reads[dir])
	}
	for _, o := range left {
		if _, err := os.Lstat(filepath.Join(dir, o)); !os.IsNotExist(err) {
			t.Errorf("%s is still there after a sync: %v", o, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, ".mine.magpie-older")); err != nil {
		t.Errorf("an entry that is no old copy was removed: %v", err)
	}

	// one skill's agents ticked again: one read of the folder still
	clear(reads)
	ok(t)(SkillAgents("skill-05", nil))
	if reads[dir] > 1 {
		t.Errorf("a tick read claude's folder %d times for old copies; want once", reads[dir])
	}
	if _, err := os.Lstat(filepath.Join(dir, "skill-05")); !os.IsNotExist(err) {
		t.Errorf("skill-05 is still claude's: %v", err)
	}
}

func TestOldOf(t *testing.T) {
	for n, want := range map[string]string{
		".pdf.magpie-old":                "pdf",
		".pdf.magpie-old-12":             "pdf",
		".a.magpie-old.magpie-old":       "a.magpie-old",
		".a.magpie-old.magpie-old-3":     "a.magpie-old",
		".pdf.magpie-old-":               "",
		".pdf.magpie-old-x":              "",
		".pdf.magpie-older":              "",
		"pdf.magpie-old":                 "",
		"..magpie-old":                   "",
		".pdf.magpie-next":               "",
		".中文.magpie-old-1":               "中文",
		".skill-03.magpie-old-2":         "skill-03",
		".skill-03.magpie-old-2.magpie-": "",
	} {
		got, ok := oldOf(n)
		if got != want || ok != (want != "") {
			t.Errorf("oldOf(%q) = %q, %v; want %q", n, got, ok, want)
		}
	}
}
