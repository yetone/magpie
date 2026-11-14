package agent

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The window's drift poll asks whether a value is one of magpie's models
// once while provider.StampNow stays the same, not on every poll: each ask
// built the field's whole option list, the agents' model lists under it
// built again once a minute. A stamp that moved asks again, and so does
// Drift itself, which Reapply and rewiring act on.
func TestDriftShownAsksAValueOncePerStamp(t *testing.T) {
	asked := 0
	f := Field{Key: "model", Options: func(map[string]string) []Option {
		asked++
		return []Option{{Value: "own-model", Ref: "fake/m1"}}
	}}
	a := &Agent{ID: "drift-shown-test"}
	vals := map[string]string{"model": "own-model"}
	var memo valueMemo
	for range 5 {
		if !memo.in(a, f, "own-model", vals, fieldOptions{}) {
			t.Fatal("own-model is magpie's")
		}
	}
	if asked != 1 {
		t.Fatalf("five polls built the options %d times, want 1", asked)
	}
	// another value of the agent's settings is another question
	if !memo.in(a, f, "own-model", map[string]string{"model": "own-model", "effort": "high"}, fieldOptions{}) || asked != 2 {
		t.Fatalf("other settings: asked %d times, want 2", asked)
	}
	provider.Changed()
	if !memo.in(a, f, "own-model", vals, fieldOptions{}) || asked != 3 {
		t.Fatalf("after a change: asked %d times, want 3", asked)
	}
	for range 3 {
		magpieValueIn(a, f, "own-model", vals, fieldOptions{})
	}
	if asked != 6 {
		t.Fatalf("Drift's own asks: %d, want every one (6)", asked)
	}
}
