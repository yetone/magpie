package agent

import (
	"fmt"
	"strings"
	"testing"
)

// The drift poll looks at each dsh entry's config several times a poll; an
// entry whose text hasn't changed is parsed once, and a writer's node is
// its own, so a change to it never shows in the next look.
func TestDshLookParsesAnEntryOnce(t *testing.T) {
	lines := []string{"- id: " + dshPiRow, "  name: pi", "  config:", "    model: \"m1\"", "    providers:"}
	for i := range 400 {
		lines = append(lines, fmt.Sprintf("      p%d:", i), "        baseURL: http://127.0.0.1:1/v1", "        models: [a, b, c]")
	}
	it := dshItem{id: dshPiRow, lines: lines}
	if got := dshConfig(it)["model"]; got != "m1" {
		t.Fatalf("model: %q", got)
	}
	if n := testing.AllocsPerRun(20, func() { dshConfig(it); dshConfigIndex([]dshItem{it}, dshPiRow) }); n > 50 {
		t.Errorf("a look at an unchanged entry: %.0f allocations, want a few", n)
	}
	// the lookup itself: no key built from the 1200 lines
	if n := testing.AllocsPerRun(20, func() { dshNodeShared(it) }); n != 0 {
		t.Errorf("finding a parsed entry: %.0f allocations, want 0", n)
	}
	// the cache keeps its own copy of the lines: a caller changing its
	// slice in place gets the new text, not the node of the old
	own := append([]string{}, lines...)
	mine := dshItem{id: dshPiRow, lines: own}
	if got := dshConfig(mine)["model"]; got != "m1" {
		t.Fatalf("copy: %q", got)
	}
	own[3] = "    model: m3"
	if got := dshConfig(mine)["model"]; got != "m3" {
		t.Errorf("lines changed in place: %q, want m3", got)
	}
	w, err := dshNode(it)
	if err != nil {
		t.Fatal(err)
	}
	yamlSetKey(yamlKey(w, "config"), "model", yamlKey(w, "name"))
	if got := dshConfig(it)["model"]; got != "m1" {
		t.Errorf("a writer's change showed in a look: %q", got)
	}
	it.lines = append(append([]string{}, it.lines[:3]...), append([]string{"    model: m2"}, it.lines[4:]...)...)
	if got := dshConfig(it)["model"]; got != "m2" {
		t.Errorf("changed text: %q, want m2", got)
	}
	if _, err := dshNodeShared(dshItem{id: "bad", lines: []string{"- id: bad", "  : ["}}); err == nil || !strings.Contains(err.Error(), "bad") {
		t.Errorf("bad entry: %v", err)
	}
}
