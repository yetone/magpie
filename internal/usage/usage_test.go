package usage

import (
	"encoding/json"
	"path/filepath"
	"strings"
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
	t.Setenv("USERPROFILE", home)
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

// A built-in moved onto its plugin sends the same account's calls through
// plugin://kiro: its history stays one, not split at the move.
func TestSummarizeKeepsMovedAccountTogether(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	y, m, d := time.Now().Date()
	now := time.Date(y, m, d, 12, 0, 0, 0, time.Local)
	recs := []Record{
		{Time: now.Add(-3 * time.Hour), Provider: "kiro", Host: "dee@example.com", Model: "m", Input: 100},      // the built-in
		{Time: now.Add(-2 * time.Hour), Provider: "kiro", Host: "kiro as dee@example.com", Model: "m", Input: 10}, // its plugin
		{Time: now.Add(-1 * time.Hour), Provider: "kiro", Host: "q.us-east-1.amazonaws.com as dee@example.com", Model: "m", Input: 1},
		{Time: now.Add(-1 * time.Hour), Provider: "kiro", Host: "kiro as bo@example.com", Model: "m", Input: 7}, // another account
	}
	got := map[string]int{}
	for _, g := range summarize(Today, now, recs).Models {
		got[g.ID] = g.Input
	}
	if len(got) != 2 || got["kiro/m @ dee@example.com"] != 111 || got["kiro/m @ bo@example.com"] != 7 {
		t.Fatalf("%v", got)
	}
}

// Calls that name their session are summed per agent's session.
func TestSummarizeSessions(t *testing.T) {
	now := time.Date(2026, 9, 23, 15, 30, 0, 0, time.UTC)
	recs := []Record{
		{Time: now.Add(-3 * time.Hour), Agent: "claude", Provider: "p", Model: "m", Input: 10, Session: "a"},
		{Time: now.Add(-2 * time.Hour), Agent: "claude", Provider: "p", Model: "m2", Input: 30, Session: "a"},
		{Time: now.Add(-2 * time.Hour), Agent: "claude", Provider: "p", Model: "m", Input: 5, Session: "b"},
		{Time: now.Add(-1 * time.Hour), Agent: "codex", Provider: "p", Model: "m", Input: 7, Session: "a"},
		{Time: now.Add(-1 * time.Hour), Agent: "codex", Provider: "p", Model: "m", Input: 100},
	}
	ss := summarize(Today, now, recs).Sessions
	if len(ss) != 3 || ss[0].ID != "a" || ss[0].Agent != "claude" || ss[0].Input != 40 || ss[0].Calls != 2 ||
		ss[1].Agent != "codex" || ss[1].Input != 7 || ss[2].ID != "b" {
		t.Fatalf("%+v", ss)
	}
}

// The timed calls (#196) sum to a mean time to the first token and how
// fast the replies were written after it; old records, whole replies and
// failures aren't in them, and a record without them reads as before.
func TestSummarizeTimesFirstTokens(t *testing.T) {
	now := time.Date(2026, 9, 23, 15, 30, 0, 0, time.UTC)
	at := now.Add(-time.Hour)
	recs := []Record{
		{Time: at, Provider: "p", Model: "m", Output: 100, Millis: 3000, TTFT: 1000, FirstText: 2000, Status: 200},
		{Time: at, Provider: "p", Model: "m", Output: 50, Millis: 1500, TTFT: 500, Status: 200},
		{Time: at, Provider: "p", Model: "m", Output: 999, Millis: 800, Status: 200}, // not streamed
		{Time: at, Provider: "p", Model: "m", Millis: 100, TTFT: 90, Status: 502},    // failed
	}
	s := summarize(Today, now, recs)
	m := s.Models[0]
	if m.Timed != 2 || m.MeanTTFT() != 750 || m.DecodeMs != 3000 || m.DecodeOut != 150 || m.Speed() != 50 {
		t.Fatalf("model: %+v", m.Totals)
	}
	if s.Timed != 2 || s.Series[14].Timed != 2 {
		t.Fatalf("totals: %+v", s.Totals)
	}
	if (Totals{}).Speed() != 0 || (Totals{}).MeanTTFT() != 0 {
		t.Fatal("nothing timed")
	}
	b, _ := json.Marshal(Record{Time: at, Provider: "p", Model: "m", Millis: 5})
	if strings.Contains(string(b), "ttft") || strings.Contains(string(b), "first_text") {
		t.Fatalf("untimed record: %s", b)
	}
}

// FormatCost stays in dollars unless cny is asked for and a usable rate is
// given; it keeps the same 0/2/3-decimal rule either currency, and a rate
// that's missing or non-positive falls back to USD rather than hiding the
// number or dividing by zero.
func TestFormatCost(t *testing.T) {
	for _, c := range []struct {
		amount   float64
		currency string
		rate     float64
		want     string
	}{
		{0.019, "usd", 0, "$0.019"},
		{1.23, "usd", 0, "$1.23"},
		{123, "usd", 0, "$123"},
		{0.019, "cny", 7.2, "¥0.137"},
		{1.23, "cny", 7.2, "¥8.86"},
		{123, "cny", 7.2, "¥886"},
		{1.23, "cny", 0, "$1.23"},   // no rate: stays USD
		{1.23, "cny", -1, "$1.23"},  // a bad rate: stays USD
		{1.23, "eur", 7.2, "$1.23"}, // any other currency: USD
	} {
		if got := FormatCost(c.amount, c.currency, c.rate); got != c.want {
			t.Errorf("FormatCost(%v, %q, %v) = %q, want %q", c.amount, c.currency, c.rate, got, c.want)
		}
	}
}
