package sessions

import (
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestGatewayStatsRebaseNativeCalendar(t *testing.T) {
	native := Stats{From: "2026-10-06", To: "2026-10-07", Sessions: []Summary{{Agent: "claude", ID: "native", Days: []int{0}, perDay: []summaryDay{{day: 0, output: 4}}}}}
	merged := MergeExternalStats(native, []ExternalDayUsage{{Date: "2026-10-04", Agent: "opencode"}}, nil)
	view := merged.Overview("claude", "", "", 8)
	if !reflect.DeepEqual(view.Days, []int{0, 0, 1, 0}) || !reflect.DeepEqual(view.Output, []int{0, 0, 4, 0}) {
		t.Fatalf("native calendar shifted when gateway history extended range: days=%v output=%v", view.Days, view.Output)
	}
	if native.Sessions[0].Days[0] != 0 || native.Sessions[0].perDay[0].output != 4 {
		t.Fatal("merge mutated the original native snapshot")
	}
}

// The gateway's sessions are merged beside the native ones, not into a copy
// of them (#2278), and the overview reads them in the order a merged list
// had: the most recently active first, a native one first of two as
// recent, every day in the range's calendar.
func TestGatewayStatsMergedBesideNative(t *testing.T) {
	at := func(s int64) time.Time { return time.Unix(1_790_000_000+s, 0) }
	native := Stats{From: "2026-10-06", To: "2026-10-08", Sessions: []Summary{
		{Key: "claude:a", Agent: "claude", ID: "a", Last: at(30), Tokens: Tokens{Output: 5}, Days: []int{0, 2}, ToolCalls: 2, tools: counts{{name: "Read", n: 2}},
			perDay: []summaryDay{{day: 0, output: 2, tools: counts{{name: "Read", n: 1}}}, {day: 2, output: 3, tools: counts{{name: "Read", n: 1}}}}},
		{Key: "claude:b", Agent: "claude", ID: "b", Last: at(10), Tokens: Tokens{Output: 5}, Days: []int{1}, perDay: []summaryDay{{day: 1, output: 5}}},
	}}
	merged := MergeExternalStats(native, []ExternalDayUsage{{Date: "2026-10-04", Agent: "opencode", Tokens: Tokens{Output: 5}}}, []ExternalSession{
		{Agent: "opencode", ID: "g1", Last: at(10), Tokens: Tokens{Output: 5}, Daily: []ExternalDayUsage{{Date: "2026-10-04", Tokens: Tokens{Output: 5}}}},
		{Agent: "opencode", ID: "g2", Last: at(40), Tokens: Tokens{Output: 5}, Daily: []ExternalDayUsage{{Date: "2026-10-08", Tokens: Tokens{Output: 5}}}},
	})
	if &merged.Sessions[0] != &native.Sessions[0] {
		t.Fatal("the native sessions were copied to merge the gateway's")
	}
	all := merged.AllSessions()
	var order []string
	for _, s := range all {
		order = append(order, s.ID)
	}
	if !slices.Equal(order, []string{"g2", "a", "b", "g1"}) || !slices.Equal(all[1].Days, []int{2, 4}) || !slices.Equal(all[3].Days, []int{0}) {
		t.Fatalf("merged %v, a's days %v, g1's %v", order, all[1].Days, all[3].Days)
	}
	if native.Sessions[0].Days[0] != 0 || native.Sessions[0].perDay[1].day != 2 {
		t.Fatal("merge wrote the native sessions' days")
	}
	got := merged.Overview("", "", "", 8)
	if !reflect.DeepEqual(got.Days, []int{1, 0, 1, 1, 2}) || !reflect.DeepEqual(got.Output, []int{5, 0, 2, 5, 8}) {
		t.Fatalf("calendar %v, output %v", got.Days, got.Output)
	}
	// the same as a list made merged
	want := Stats{From: merged.From, To: merged.To, Days: merged.Days, Sessions: all}.Overview("", "", "", 8)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("overview %+v\nmerged list's %+v", got, want)
	}
}

// linkaww on Discord: Usage › Sessions lists the sessions whose keys the
// stats name, and those named only the native ones, so a session that came
// through the gateway alone (every one, in gateway mode) wasn't listed.
func TestGatewayStatsNameTheGatewaySessions(t *testing.T) {
	at := func(s int64) time.Time { return time.Unix(1_790_000_000+s, 0) }
	native := Stats{From: "2026-10-06", To: "2026-10-08", Keys: []string{"claude:a"}, Sessions: []Summary{
		{Key: "claude:a", Agent: "claude", ID: "a", Last: at(30), Days: []int{0}, perDay: []summaryDay{{day: 0, output: 2}}},
	}}
	merged := MergeExternalStats(native, nil, []ExternalSession{
		{Agent: "claude", ID: "g1", Last: at(10), Daily: []ExternalDayUsage{{Date: "2026-10-07", Tokens: Tokens{Output: 5}}}},
		{Agent: "pi", ID: "g2", Last: at(40), Daily: []ExternalDayUsage{{Date: "2026-10-08", Tokens: Tokens{Output: 5}}}},
	})
	if want := []string{"pi:g2", "claude:a", "claude:g1"}; !slices.Equal(merged.Keys, want) {
		t.Fatalf("keys %v, want %v", merged.Keys, want)
	}
	if !slices.Equal(native.Keys, []string{"claude:a"}) {
		t.Fatalf("merge wrote the native keys: %v", native.Keys)
	}
	// gateway mode: no native session at all
	only := MergeExternalStats(Stats{From: "2026-10-06", To: "2026-10-08"}, nil, []ExternalSession{{Agent: "claude", ID: "g1", Last: at(10)}})
	if !slices.Equal(only.Keys, []string{"claude:g1"}) {
		t.Fatalf("gateway only: keys %v", only.Keys)
	}
}
