package usage

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func TestSummarize(t *testing.T) {
	now := time.Date(2026, 9, 23, 15, 30, 0, 0, time.UTC)
	recs := []Record{
		{Time: now.Add(-40 * 24 * time.Hour), Agent: "codex", Provider: "p", Model: "m", Input: 10, Output: 1},
		{Time: now.Add(-3 * 24 * time.Hour), Agent: "claude", Provider: "p", Model: "m", Input: 100, Output: 20},
		{Time: now.Add(-2 * time.Hour), Agent: "claude", Provider: "p", Model: "m2", Input: 1000, Output: 200, Status: 200},
		{Time: now.Add(-1 * time.Hour), Agent: "codex", Provider: "p", Model: "m", Status: 502},
	}
	s := summarize(Today, now, recs)
	if s.Calls != 2 || s.Errors != 1 || s.Input != 1000 || s.Bucket != "hour" || len(s.Series) != 24 {
		t.Fatalf("today: %+v", s.Totals)
	}
	if s.Series[13].Input != 1000 || s.Series[14].Calls != 1 {
		t.Fatalf("today buckets: %+v %+v", s.Series[13].Totals, s.Series[14].Totals)
	}
	s = summarize(Week, now, recs)
	if s.Calls != 3 || len(s.Series) != 7 || s.Series[3].Input != 100 || s.Series[6].Input != 1000 {
		t.Fatalf("week: %+v series=%d", s.Totals, len(s.Series))
	}
	if s.Agents[0].ID != "claude" || s.Agents[0].Input != 1100 || s.Models[0].ID != "p/m2" {
		t.Fatalf("groups: %+v %+v", s.Agents, s.Models)
	}
	s = summarize(All, now, recs)
	if s.Calls != 4 || s.Bucket != "day" || len(s.Series) != 41 {
		t.Fatalf("all: %+v bucket=%s series=%d", s.Totals, s.Bucket, len(s.Series))
	}
	recs = append([]Record{{Time: now.Add(-100 * 24 * time.Hour), Agent: "pi", Provider: "p", Model: "m", Input: 1}}, recs...)
	s = summarize(All, now, recs)
	if s.Bucket != "week" || s.Since.Weekday() != time.Monday || s.Calls != 5 {
		t.Fatalf("all/weeks: bucket=%s since=%s calls=%d", s.Bucket, s.Since, s.Calls)
	}
	if s.Unpriced != 4 || s.Cost != 0 {
		t.Fatalf("pricing: unpriced=%d cost=%v", s.Unpriced, s.Cost)
	}
}

// A provider id given to another place later doesn't take the earlier
// place's calls: they are told apart by where they went.
func TestSummarizeTellsPlacesApart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "https://new.example/v1"}); err != nil {
		t.Fatal(err)
	}
	// noon, so the calls hours before are today's whenever this runs
	y, m, d := time.Now().Date()
	now := time.Date(y, m, d, 12, 0, 0, 0, time.Local)
	recs := []Record{
		{Time: now.Add(-3 * time.Hour), Provider: "relay", Model: "m", Input: 5},                        // kept before hosts were
		{Time: now.Add(-2 * time.Hour), Provider: "relay", Host: "old.example", Model: "m", Input: 100}, // the id's earlier place
		{Time: now.Add(-1 * time.Hour), Provider: "relay", Host: "new.example", Model: "m", Input: 10},  // where it goes now
		{Time: now.Add(-1 * time.Hour), Provider: "relay", Host: "new.example", Model: "m", Input: 1},
	}
	got := map[string]int{}
	for _, g := range summarize(Today, now, recs).Models {
		got[g.ID] = g.Input
	}
	want := map[string]int{"relay/m": 5, "relay/m @ old.example": 100, "relay/m @ new.example": 11}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%v", got)
		}
	}
	// one place, the one it goes now: no host on it
	got = map[string]int{}
	for _, g := range summarize(Today, now, recs[2:]).Models {
		got[g.ID] = g.Input
	}
	if got["relay/m"] != 11 || len(got) != 1 {
		t.Fatalf("%v", got)
	}
}
