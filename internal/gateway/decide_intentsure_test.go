package gateway

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #5101: the confidence an intent needs of Jev was fixed at 0.4 for every
// group. A group may set its own (IntentSure): lower it and Jev's unsure
// picks count, raise it and fewer do, set it to 0 and every pick but
// "none of these" counts.

// chatFor is chat for the group named, not group/r.
func chatFor(group, first string, extra string) string {
	if extra != "" {
		extra = "," + extra
	}
	return fmt.Sprintf(`{"model":%s%s,"messages":[{"role":"user","content":%s}]}`, quote(group), extra, quote(first))
}

// ptrSure is the group's IntentSure as a test names it.
func ptrSure(sure float64) *float64 { return &sure }

// sure 0.3 misses a group's intent rule at the default 0.4, hits it with
// IntentSure 0.25, and counts again at 0.4 once the field is gone.
func TestGroupIntentSureThreshold(t *testing.T) {
	s, _, _, j := jevved(t, "", provider.Rule{Use: "b/big", Intent: "debugging"})
	j.choice, j.sure = "debugging", 0.3
	_, r := postOK(t, s, "d0", chat("why does this crash?", nil, 0, ""))
	if r.Rule.N != 0 || r.Rule.Classified.Intent != "" || r.Rule.Classified.Sure != 0.3 {
		t.Fatalf("default 0.4: %+v", r.Rule.Classified)
	}
	// a torn pick stays none at 0.4 but matches at 0.25
	g, _, _ := provider.FindGroup("group/r")
	sure := 0.25
	g.IntentSure = &sure
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	_, r = postOK(t, s, "d1", chat("why does this crash, again?", nil, 0, ""))
	if r.Rule.N != 1 || r.Rule.Classified.Intent != "debugging" || r.Rule.Classified.Sure != 0.3 {
		t.Fatalf("0.25: %+v", r.Rule.Classified)
	}
	// the trace keeps the sure and the said with it, whichever way the
	// threshold went
	if r.Rule.Classified.Said != "debugging" {
		t.Fatalf("said: %+v", r.Rule.Classified)
	}
	// unset: 0.4 again
	g.IntentSure = nil
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	_, r = postOK(t, s, "d2", chat("why does this crash, still?", nil, 0, ""))
	if r.Rule.N != 0 || r.Rule.Classified.Intent != "" {
		t.Fatalf("unset: %+v", r.Rule.Classified)
	}
}

// IntentSure 0 takes Jev's pick whatever its sure, "none of these" still
// not an intent; the effort is picked as ever.
func TestGroupIntentSureZeroTakesTheArgmax(t *testing.T) {
	s, _, _, j := jevved(t, provider.EffortAuto, provider.Rule{Use: "b/big", Intent: "debugging"})
	j.choice, j.sure = "debugging", 0.01
	g, _, _ := provider.FindGroup("group/r")
	zero := 0.0
	g.IntentSure = &zero
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	out, r := postOK(t, s, "z1", chat("why does this crash?", nil, 0, `,"reasoning_effort":"low"`))
	if !strings.Contains(out, "from kb") || r.Rule.N != 1 || r.Rule.Classified.Intent != "debugging" || r.Rule.Classified.Sure != 0.01 {
		t.Fatalf("zero: %s %+v", out, r.Rule.Classified)
	}
	// none of these is no intent, not even at 0
	j.choice = noIntent
	_, r = postOK(t, s, "z2", chat("what now?", nil, 0, `,"reasoning_effort":"low"`))
	if r.Rule.N != 0 || r.Rule.Classified.Intent != "" || r.Rule.Classified.Said != noIntent {
		t.Fatalf("none of these at 0: %+v", r.Rule.Classified)
	}
	if r.Rule.Classified.Effort == "" {
		t.Fatalf("effort not asked: %+v", r.Rule.Classified)
	}
}

// A group's IntentSure is of the group's own: another group classifying
// with the same classifier keeps the default 0.4.
func TestGroupIntentSureIsPerGroup(t *testing.T) {
	s, _, _, j := jevved(t, "", provider.Rule{Use: "b/big", Intent: "debugging"})
	j.choice, j.sure = "debugging", 0.3
	g, _, _ := provider.FindGroup("group/r")
	sure := 0.25
	g.IntentSure = &sure
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	other := provider.Group{ID: "other", Name: "Other", Members: []string{"a/small", "b/big"}, Routing: provider.Rotate, Classifier: "ts/jev-latest",
		Rules: []provider.Rule{{Use: "a/small", Intent: "debugging"}}}
	if err := provider.SaveGroup(other); err != nil {
		t.Fatal(err)
	}
	out, r := postOK(t, s, "p1", chatFor("group/other", "why does this crash?", ""))
	if !strings.Contains(out, "from ka") || r.Rule.N != 0 || r.Rule.Classified.Intent != "" {
		t.Fatalf("other group at 0.4: %s %+v", out, r.Rule.Classified)
	}
	out, r = postOK(t, s, "p2", chat("why does this crash, again?", nil, 0, ""))
	if !strings.Contains(out, "from kb") || r.Rule.N != 1 || r.Rule.Classified.Intent != "debugging" {
		t.Fatalf("ruled group at 0.25: %s %+v", out, r.Rule.Classified)
	}
}

// The group's IntentSure out of range is refused, 0 through 1 kept.
func TestGroupIntentSureRange(t *testing.T) {
	for _, x := range []float64{-0.1, 1.1} {
		g := provider.Group{ID: "bad", Name: "Bad", Members: []string{"a/small", "b/big"}, Classifier: "c/cls",
			Rules: []provider.Rule{{Use: "a/small", Intent: "debugging"}}}
		sure := x
		g.IntentSure = &sure
		if err := provider.SaveGroup(g); err == nil {
			t.Fatalf("%v saved", x)
		}
	}
}

// Levels are another scale (every message at one of them): the torn keep
// the turn before's level, IntentSure or no IntentSure (#5101 leaves them
// as they are).
func TestGroupIntentSureLeavesLevels(t *testing.T) {
	s, _, _, j := jevved(t, provider.EffortAuto, provider.Rule{Use: "a/small", Intent: "简单任务"}, provider.Rule{Use: "b/big", Intent: "复杂任务"})
	j.levels, j.sure = 0.9, 0.09
	g, _, _ := provider.FindGroup("group/r")
	sure := 0.0
	g.IntentSure = &sure
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	// torn over the level, the turn before's is kept, as ever
	_, r := postOK(t, s, "l1", chat("写个纸牌游戏", nil, 0, ""))
	if r.Rule.N != 0 || r.Rule.Classified.Intent != "" || r.Rule.Classified.Sure != 0.09 {
		t.Fatalf("torn level, none before: %+v", r.Rule.Classified)
	}
}
