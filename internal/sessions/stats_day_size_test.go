package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Each day's usage and active rows are held at their own length: every
// range the overview keeps holds them, and rows grown by append kept the
// slack of each doubling (#2278). Five folders on one day grow to eight.
func TestStatsDayRowsHoldNoSlack(t *testing.T) {
	inZone(t, 0)
	claude, _ := setup(t)
	for i, dir := range []string{"a", "b", "c", "d", "e"} {
		id := "4444444" + itoa(i) + "-2222-3333-4444-555555555555"
		path := filepath.Join(claude, "projects", "-work-"+dir, id+".jsonl")
		os.MkdirAll(filepath.Dir(path), 0o755)
		line := ccMsg("m"+dir, "2026-09-27T10:00:00.000Z", 100, 10) + ccMsg("n"+dir, "2026-09-27T10:01:00.000Z", 100, 10)
		line = strings.ReplaceAll(line, "/work/night", "/work/"+dir)
		line = strings.ReplaceAll(line, "33333333-2222-3333-4444-555555555555", id)
		os.WriteFile(path, []byte(line), 0o644)
	}
	s := statsAt(0, statsNow)
	found := false
	for _, d := range s.Days {
		if d.Date != "2026-09-27" {
			continue
		}
		found = true
		if len(d.Usage) < 5 || len(d.Active) < 5 {
			t.Fatalf("the 27th has %d usage and %d active rows, want 5 of each", len(d.Usage), len(d.Active))
		}
		if cap(d.Usage) != len(d.Usage) || cap(d.Active) != len(d.Active) {
			t.Fatalf("usage %d of %d, active %d of %d", len(d.Usage), cap(d.Usage), len(d.Active), cap(d.Active))
		}
	}
	if !found {
		t.Fatal("no 2026-09-27 in the stats")
	}
}
