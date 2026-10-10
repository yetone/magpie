package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// E steps the picked member's own effort, F sends it in its vendor's fast
// mode or not; a model with none is refused it, and the group shows both.
func TestGroupMemberEffortAndFast(t *testing.T) {
	home(t)
	if err := provider.Save(provider.Provider{ID: "an", Name: "Anthropic", Key: "k", Anthropic: "https://api.anthropic.com", Models: []string{"claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "opus", Name: "Opus", Members: []string{"an/claude-opus-5-5", "a/m"}}); err != nil {
		t.Fatal(err)
	}
	m := press(t, model{w: 120, h: 40}, "3", "enter")
	if m.mode != modeGroup || m.gsel != 0 {
		t.Fatalf("mode %d, picked %d", m.mode, m.gsel)
	}
	m = press(t, m, "F")
	wantFlash(t, m, true, "an/claude-opus-5-5 fast")
	if g := group(t, "opus"); !slices.Equal(g.Fast, []string{"an/claude-opus-5-5"}) {
		t.Fatalf("fast %v", g.Fast)
	}
	if v := m.View(); !strings.Contains(v, "· fast") {
		t.Fatalf("fast not shown:\n%s", v)
	}
	m = press(t, m, "E")
	wantFlash(t, m, true, "an/claude-opus-5-5:low set")
	m = press(t, m, "E")
	if g := group(t, "opus"); !slices.Equal(g.Members, []string{"an/claude-opus-5-5:medium", "a/m"}) || !slices.Equal(g.Fast, []string{"an/claude-opus-5-5:medium"}) {
		t.Fatalf("members %v fast %v", g.Members, g.Fast)
	}
	m = press(t, m, "F")
	if g := group(t, "opus"); len(g.Fast) != 0 {
		t.Fatalf("still fast %v", g.Fast)
	}
	// a/m is on a relay: no fast mode to ask for
	m = press(t, m, "down", "F")
	wantFlash(t, m, false, "no fast mode")
}

// Taking a model out of a group whose other one is off is refused; while
// the in-process gateway holds the catalog, the group stays as it was, not
// shifted down a model with an empty one at its end.
func TestGroupTakeOutRefusedWhileHeld(t *testing.T) {
	home(t)
	if err := provider.SaveGroup(provider.Group{ID: "pair", Name: "Pair", Members: []string{"a/m", "b/gpt-5.5"}, Off: []string{"b/gpt-5.5"}, Rules: []provider.Rule{{Use: "a/m", Tokens: 5}}}); err != nil {
		t.Fatal(err)
	}
	defer provider.Hold()()
	m := press(t, model{w: 120, h: 40}, "3", "enter")
	if m.mode != modeGroup || m.gsel != 0 {
		t.Fatalf("mode %d, picked %d", m.mode, m.gsel)
	}
	m = press(t, m, "d", "d")
	wantFlash(t, m, false, "switched off")
	if g := group(t, "pair"); !slices.Equal(g.Members, []string{"a/m", "b/gpt-5.5"}) || len(g.Rules) != 1 || g.Rules[0].Use != "a/m" {
		t.Fatalf("members %v rules %+v", g.Members, g.Rules)
	}
}
