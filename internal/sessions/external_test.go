package sessions

import (
	"reflect"
	"testing"
)

func TestGatewayStatsRebaseNativeCalendar(t *testing.T) {
	native := Stats{From: "2026-10-06", To: "2026-10-07", Sessions: []Summary{{Agent: "claude", ID: "native", Days: []int{0}, perDay: map[int]*summaryDay{0: {output: 4}}}}}
	merged := MergeExternalStats(native, []ExternalDayUsage{{Date: "2026-10-04", Agent: "opencode"}}, nil)
	view := merged.Overview("claude", "", "", 8)
	if !reflect.DeepEqual(view.Days, []int{0, 0, 1, 0}) || !reflect.DeepEqual(view.Output, []int{0, 0, 4, 0}) {
		t.Fatalf("native calendar shifted when gateway history extended range: days=%v output=%v", view.Days, view.Output)
	}
	if native.Sessions[0].Days[0] != 0 || native.Sessions[0].perDay[0].output != 4 {
		t.Fatal("merge mutated the original native snapshot")
	}
}
