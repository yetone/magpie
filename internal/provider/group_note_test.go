package provider

import (
	"strings"
	"testing"
)

// A routing group keeps a note of the user's (ConatusMoon2222 on X): saved
// as one line, its spaces run together, cut at NoteLongest characters, and
// cleared by saving it empty. It goes with the group where the group goes:
// a copy, a group shared to another magpie, a backup's StoredGroups and
// sync's Mirror. A save that changes something else keeps it.
func TestGroupNote(t *testing.T) {
	fastHome(t)
	note := func(id string) string {
		t.Helper()
		g, _, ok := FindGroup(GroupPrefix + id)
		if !ok {
			t.Fatalf("no group %s", id)
		}
		return g.Note
	}
	g := Group{ID: "work", Name: "Work", Note: "  for the office key\n\tweekdays   only ", Members: []string{"rl/claude-opus-5-5", "an/claude-sonnet-5"}}
	if err := SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	if got := note("work"); got != "for the office key weekdays only" {
		t.Fatalf("note %q", got)
	}

	// another change keeps it
	saved, _, _ := FindGroup(GroupPrefix + "work")
	saved.Routing = Rotate
	if err := SaveGroup(saved); err != nil {
		t.Fatal(err)
	}
	if got := note("work"); got != "for the office key weekdays only" {
		t.Fatalf("after a routing change %q", got)
	}

	// a long one is cut, in characters, not bytes
	saved.Note = strings.Repeat("备", NoteLongest+50)
	if err := SaveGroup(saved); err != nil {
		t.Fatal(err)
	}
	if got := note("work"); got != strings.Repeat("备", NoteLongest) {
		t.Fatalf("long note kept %d characters", len([]rune(got)))
	}
	saved.Note = "for the office key"
	if err := SaveGroup(saved); err != nil {
		t.Fatal(err)
	}

	// a copy has it
	cp, err := CopyGroup("work", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := note(cp.ID); got != "for the office key" {
		t.Fatalf("copy's note %q", got)
	}

	// shared to another magpie
	text, err := ShareGroup("work")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, `"note":"for the office key"`) {
		t.Fatalf("shared text without the note: %s", text)
	}

	// a backup and a sync carry it
	stored, err := StoredGroups()
	if err != nil {
		t.Fatal(err)
	}
	ps, err := Stored()
	if err != nil {
		t.Fatal(err)
	}
	if err := Mirror(ps, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup(GroupPrefix + "work"); ok {
		t.Fatal("Mirror with no groups kept work")
	}
	if err := Mirror(ps, stored); err != nil {
		t.Fatal(err)
	}
	if got := note("work"); got != "for the office key" {
		t.Fatalf("after Mirror %q", got)
	}
	if err := DeleteGroup("work"); err != nil {
		t.Fatal(err)
	}
	if err := RestoreGroups(stored); err != nil {
		t.Fatal(err)
	}
	if got := note("work"); got != "for the office key" {
		t.Fatalf("after RestoreGroups %q", got)
	}

	// cleared
	saved, _, _ = FindGroup(GroupPrefix + "work")
	saved.Note = "   "
	if err := SaveGroup(saved); err != nil {
		t.Fatal(err)
	}
	if got := note("work"); got != "" {
		t.Fatalf("cleared note %q", got)
	}
}
