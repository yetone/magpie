package usage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// An agent's group says when its newest call in the period was made
// (Group.Last), for the Usage page to fold the clients idle for days
// (WJaunita45604 on X): a summary of the records, one counted on from the
// log's kept prefix after a later call, and the session files' own
// (direct). The other lists say nothing of it.
func TestSummaryAgentsSayTheirLastCall(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.Local)
	early, late := now.AddDate(0, 0, -5), now.Add(-time.Hour)
	recs := []Record{
		{Time: late, Agent: "bun/1.3", Provider: "relay", Model: "m", Input: 10, Status: 200},
		{Time: early, Agent: "bun/1.3", Provider: "relay", Model: "m", Input: 10, Status: 200},
		{Time: early, Agent: "1340C", Provider: "relay", Model: "m", Input: 10, Status: 200},
	}
	last := func(s Summary) map[string]time.Time {
		out := map[string]time.Time{}
		for _, g := range s.Agents {
			out[g.ID] = g.Last
		}
		return out
	}
	s := summarize(Month, now, recs)
	got := last(s)
	if !got["bun"].Equal(late) || !got["1340C"].Equal(early) {
		t.Fatalf("last calls: %v", got)
	}
	b, _ := json.Marshal(s.Models)
	if strings.Contains(string(b), `"last"`) {
		t.Fatalf("a model says a last call: %s", b)
	}

	rows := []Row{}
	for _, r := range recs {
		rows = append(rows, Row{Record: r, Source: "log"})
	}
	got = last(direct(Month, now, rows))
	if !got["bun"].Equal(late) || !got["1340C"].Equal(early) {
		t.Fatalf("session files' last calls: %v", got)
	}
}

func TestLoggedSummaryAgentsLastCallMovesOn(t *testing.T) {
	pageHome(t)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	holdClock(t, now)
	Append(Record{Time: now.Add(-2 * time.Hour), Agent: "1340C", Input: 3, Status: 200})
	if g := Summarize(Today).Agents; len(g) != 1 || !g[0].Last.Equal(now.Add(-2*time.Hour)) {
		t.Fatalf("first: %+v", g)
	}
	Append(Record{Time: now.Add(-time.Minute), Agent: "1340C", Input: 3, Status: 200})
	if g := Summarize(Today).Agents; len(g) != 1 || !g[0].Last.Equal(now.Add(-time.Minute)) || g[0].Calls != 2 {
		t.Fatalf("after another call: %+v", g)
	}
}
