package agent

import (
	"testing"
	"time"
)

// An idle magpie builds dsh's models again at most every dshModelsFor,
// not every few minutes, and once more when the day changes, for the
// models retired by date.
func TestDshModelsStale(t *testing.T) {
	at := time.Date(2026, 11, 11, 12, 0, 0, 0, time.Local)
	for _, c := range []struct {
		now   time.Time
		stale bool
	}{
		{at.Add(10 * time.Minute), false},
		{at.Add(29 * time.Minute), false},
		{at.Add(dshModelsFor), true},
		{time.Date(2026, 11, 12, 0, 1, 0, 0, time.Local), true},
	} {
		if got := dshModelsStale(at, c.now); got != c.stale {
			t.Errorf("built at %v, now %v: stale %v, want %v", at, c.now, got, c.stale)
		}
	}
	late := time.Date(2026, 11, 11, 23, 58, 0, 0, time.Local)
	if !dshModelsStale(late, late.Add(5*time.Minute)) {
		t.Error("models built before midnight are kept into the next day")
	}
}
