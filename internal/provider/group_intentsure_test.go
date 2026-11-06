package provider

import (
	"math"
	"path/filepath"
	"strings"
	"testing"
)

// A group's intentSure (#5101) is kept from 0 to 1 and nil, and refused
// outside that, NaN among it, with nothing saved.
func TestGroupIntentSureRange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	f := func(v float64) *float64 { return &v }
	for _, v := range []*float64{nil, f(0), f(0.25), f(1)} {
		if err := SaveGroup(Group{ID: "g", Name: "g", Members: []string{"a/m1", "b/m2"}, IntentSure: v}); err != nil {
			t.Fatalf("%v: %v", v, err)
		}
	}
	for _, v := range []float64{-0.01, 1.01, math.NaN(), math.Inf(1)} {
		err := SaveGroup(Group{ID: "g", Name: "g", Members: []string{"a/m1", "b/m2"}, IntentSure: f(v)})
		if err == nil || !strings.Contains(err.Error(), "from 0 to 1") {
			t.Errorf("%g: %v, want refused", v, err)
		}
	}
	for _, g := range Groups() {
		if g.ID == "g" && (g.IntentSure == nil || *g.IntentSure != 1) {
			t.Errorf("saved %v, want the last good 1", g.IntentSure)
		}
	}
}
