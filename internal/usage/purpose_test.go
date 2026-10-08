package usage

import (
	"slices"
	"testing"
	"time"
)

func TestPurposeFilter(t *testing.T) {
	for _, tc := range []struct {
		purpose string
		kinds   []string
	}{
		{"kind:thread_title", []string{"thread_title", "thread_title_reconsideration", "title_generation", "title"}},
		{"kind:guardian", []string{"guardian", "auto_review", "guardian_review"}},
		{"kind:memory_consolidation", []string{"memory_consolidation", "memgen", "memory"}},
		{"kind:collab_spawn", []string{"collab_spawn", "thread_spawn", "agent_job"}},
		{"kind:ambient_suggestions", []string{"ambient_suggestions", "ambient_suggestion_safety"}},
		{"kind:future_kind", []string{"future_kind"}},
		{"kind:unmarked", []string{"unmarked"}},
		{"unmarked", []string{""}},
	} {
		t.Run(tc.purpose, func(t *testing.T) {
			f := Filter{Purpose: tc.purpose, Agent: "codex", Failed: true}
			for _, kind := range tc.kinds {
				r := Record{Kind: kind, Agent: "codex", Status: 429}
				if !f.keeps(r) {
					t.Fatalf("%q did not keep %q", tc.purpose, kind)
				}
				r.Status = 200
				if f.keeps(r) {
					t.Fatal("purpose bypassed the failure filter")
				}
			}
			if f.keeps(Record{Kind: "different_unknown", Agent: "codex", Status: 429}) {
				t.Fatal("unknown purposes were combined")
			}
		})
	}
}

func TestPurposeRequestPages(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	start := Today.Since(now).Add(time.Second)
	kinds := []string{"thread_title", "review", "thread_title_reconsideration", "future_kind", "", "title_generation", "unmarked"}
	for i, kind := range kinds {
		Append(Record{Time: start.Add(time.Duration(i) * time.Second), Agent: "codex", Provider: "openai", Model: "m",
			Kind: kind, Input: 10, Output: 2, CacheRead: 3, CacheWrite: 4, Status: 200})
	}
	for _, purpose := range []string{"", "kind:thread_title", "kind:review", "kind:future_kind", "unmarked", "kind:unmarked", "kind:absent"} {
		for _, f := range []Filter{{Purpose: purpose}, {Purpose: purpose, Provider: "absent"}, {Purpose: purpose, Day: start.Format(time.DateOnly)}} {
			for _, offset := range []int{0, 1, 100} {
				got := QueryPage(Today, f, offset, 1)
				want := pageFromLedger(Today, f, offset, 1, LedgerOf(Today, Filter{}))
				equalPage(t, got, want)
			}
		}
	}
	first := QueryPage(Today, Filter{Purpose: "kind:thread_title"}, 0, 1)
	second := QueryPage(Today, Filter{Purpose: "kind:thread_title"}, 1, 1)
	if first.Total != 3 || first.Sum.Calls != 3 || first.Sum.Input != 30 || first.Sum.Output != 6 || first.Sum.CacheRead != 9 || first.Sum.CacheWrite != 12 || first.Sum.Cost <= 0 {
		t.Fatalf("title totals: %+v", first)
	}
	if second.Total != first.Total || second.Sum != first.Sum || len(first.Rows) != 1 || len(second.Rows) != 1 || first.Rows[0].Kind != "title_generation" || second.Rows[0].Kind != "thread_title_reconsideration" {
		t.Fatalf("pagination changed totals or raw kinds: first %+v, second %+v", first, second)
	}
	if !slices.Equal(first.Purposes, []string{"kind:future_kind", "kind:review", "kind:thread_title", "kind:unmarked", "unmarked"}) {
		t.Fatalf("filter options lost other purposes: %v", first.Purposes)
	}
}
