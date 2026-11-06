package cli

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// intent-sure= sets how sure the classifier must be of a group's intent
// (#5101): from 0 to 1, empty back to the 0.4 every group asked before, and
// anything else, NaN among it, refused with the group unchanged.
func TestGroupIntentSurePairs(t *testing.T) {
	none := func(string) (string, error) { return "", nil }
	g := provider.Group{}
	for _, c := range []struct {
		v    string
		want float64
	}{{"0.25", 0.25}, {"0", 0}, {"1", 1}, {" 0.6 ", 0.6}} {
		if err := applyGroupPairs(&g, []string{"intent-sure=" + c.v}, none, false); err != nil || g.IntentSure == nil || *g.IntentSure != c.want {
			t.Fatalf("intent-sure=%q: %v %v, want %g", c.v, err, g.IntentSure, c.want)
		}
	}
	for _, v := range []string{"-0.1", "1.5", "NaN", "nan", "high"} {
		err := applyGroupPairs(&g, []string{"intent-sure=" + v}, none, false)
		if err == nil || !strings.Contains(err.Error(), "from 0 to 1") {
			t.Errorf("intent-sure=%q: %v, want refused", v, err)
		}
		if g.IntentSure == nil || *g.IntentSure != 0.6 {
			t.Errorf("intent-sure=%q changed the group: %v", v, g.IntentSure)
		}
	}
	if err := applyGroupPairs(&g, []string{"intentsure="}, none, false); err != nil || g.IntentSure != nil {
		t.Fatalf("empty: %v %v, want nil", err, g.IntentSure)
	}
}
