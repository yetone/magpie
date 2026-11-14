package maskrec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func resetChecked() {
	checked.mu.Lock()
	defer checked.mu.Unlock()
	if checked.timer != nil {
		checked.timer.Stop()
	}
	checked.days, checked.dirty, checked.timer, checked.pruned = nil, nil, nil, time.Time{}
}

// An agent's turns are counted while masking is on, nothing masked or
// not: the summary says each agent's turns looked over, those masking
// changed and those sent as written, over the days asked for, after a
// restart too (the day's file), and Clear takes them with the records.
// The file holds counts alone.
func TestCheckedCountsEachAgent(t *testing.T) {
	now := time.Date(2026, 11, 14, 15, 0, 0, 0, time.Local)
	dir := sandbox(t, now)
	resetChecked()
	t.Cleanup(resetChecked)
	for range 3 {
		Checked("pi", true, false)
	}
	Checked("pi", false, false)
	Checked("dsh", true, true)
	Checked("", true, false)
	want := map[string]Looked{"pi": {Name: "pi", Looked: 3, Whole: 1}, "dsh": {Name: "dsh", Looked: 1, Masked: 1}, "other": {Name: "other", Looked: 1}}
	check := func(when string, sum Summary) {
		t.Helper()
		if len(sum.Checked) != 3 || sum.Checked[0].Name != "pi" {
			t.Fatalf("%s: %+v", when, sum.Checked)
		}
		for _, c := range sum.Checked {
			if c != want[c.Name] {
				t.Fatalf("%s: %+v, want %+v", when, c, want[c.Name])
			}
		}
	}
	check("in memory", Sum(7))

	flushChecked()
	path := filepath.Join(dir, "checked-2026-11-14.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(string(b), "\n") || !strings.Contains(string(b), `"pi"`) {
		t.Fatalf("file: %s", b)
	}
	// a restart reads the day again, and counts on from it
	resetChecked()
	check("read back", Sum(7))
	Checked("pi", true, false)
	if c := Sum(7).Checked[0]; c.Name != "pi" || c.Looked != 4 {
		t.Fatalf("counted on: %+v", c)
	}
	// a day out of the days asked for isn't counted
	clock = func() time.Time { return now.AddDate(0, 0, 3) }
	if got := Sum(1).Checked; len(got) != 0 {
		t.Fatalf("today's, with nothing counted: %+v", got)
	}
	if got := Sum(7).Checked; len(got) != 3 {
		t.Fatalf("over seven days: %+v", got)
	}
	// a file that doesn't parse counts from nothing
	resetChecked()
	os.WriteFile(path, []byte("{half"), 0o600)
	if got := Sum(7).Checked; len(got) != 0 {
		t.Fatalf("unparsed file: %+v", got)
	}

	Checked("pi", true, false)
	flushChecked()
	if err := Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Clear left %s: %v", path, err)
	}
	if got := Sum(7).Checked; len(got) != 0 {
		t.Fatalf("after Clear: %+v", got)
	}
}

// A day's counts older than the records are kept goes with them.
func TestCheckedPruned(t *testing.T) {
	now := time.Date(2026, 11, 14, 15, 0, 0, 0, time.Local)
	dir := sandbox(t, now)
	resetChecked()
	t.Cleanup(resetChecked)
	old := filepath.Join(dir, "checked-2026-10-01.json")
	os.WriteFile(old, []byte(`{"pi":{"looked":2}}`), 0o600)
	kept := filepath.Join(dir, "checked-2026-11-01.json")
	os.WriteFile(kept, []byte(`{"pi":{"looked":2}}`), 0o600)
	Checked("pi", true, false)
	flushChecked()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("%s kept: %v", old, err)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatal(err)
	}
}
