package memory

import (
	"slices"
	"testing"
	"time"
)

// A candidate nobody reviewed for Keep is archived; one archived or
// superseded for Keep more is removed, recorded as removed and, distilled,
// dismissed so it isn't proposed again. What the user wrote or pinned
// stays whatever its state and age, and so does anything changed within
// Keep.
func TestPruneKeepsTheUsersAndTheRecent(t *testing.T) {
	at := fresh(t)
	cand := infer(t, "Uses pnpm", Global)
	gone := infer(t, "Deploys from CI", Global)
	sup := infer(t, "Tabs, not spaces", Global)
	pinned := infer(t, "Pinned candidate", Global)
	mine := save(t, "My own, archived", Fact, Global)
	if _, err := SetStatus(Archived, gone.ID); err != nil { // the user dropped it
		t.Fatal(err)
	}
	if _, err := SetStatus(Archived, mine.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := Edit(mine.ID, Input{Text: str("My own, archived.")}, true); err != nil {
		t.Fatal(err)
	}
	pin := true
	if _, err := Edit(pinned.ID, Input{Pinned: &pin}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Tidy(Plan{Merges: []Merge{{IDs: []string{sup.ID, cand.ID}, Text: "x"}}}); err != nil {
		t.Fatal(err)
	}
	// the merge made cand superseded too; bring it back as a candidate
	if _, err := Edit(cand.ID, Input{Status: ptr(Candidate)}, false); err != nil {
		t.Fatal(err)
	}

	*at = at.Add(Keep - time.Hour)
	if p, err := Prune(); err != nil || p != (Pruned{}) {
		t.Fatalf("within Keep: %+v %v", p, err)
	}

	*at = at.Add(2 * time.Hour)
	p, err := Prune()
	if err != nil {
		t.Fatal(err)
	}
	// gone and sup removed; cand and the merged one (both candidates) archived
	if p.Removed != 2 || p.Archived != 2 {
		t.Fatalf("first: %+v", p)
	}
	if get(t, cand.ID).Status != Archived {
		t.Fatal("candidate not archived")
	}
	for _, id := range []string{gone.ID, sup.ID} {
		if _, err := Get(id); err != ErrNotFound {
			t.Fatalf("%s kept: %v", id, err)
		}
	}
	if m := get(t, pinned.ID); m.Status != Candidate {
		t.Fatalf("pinned changed: %+v", m)
	}
	if m := get(t, mine.ID); m.Status != Archived {
		t.Fatalf("user's own changed: %+v", m)
	}
	b, err := Collect()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Removed[sup.ID]; !ok || !slices.Contains(b.Dismissed, norm(sup.Text)) {
		t.Fatalf("not recorded: %+v", b)
	}
	// a sync from a computer that still has it doesn't bring it back
	old := &Bundle{Memories: []*Memory{sup}}
	if _, err := Put(old); err != nil {
		t.Fatal(err)
	}
	if _, err := Get(sup.ID); err != ErrNotFound {
		t.Fatalf("synced back: %v", err)
	}
	// nor does distilling it again
	if m := infer(t, "Tabs, not spaces", Global); m != nil {
		t.Fatalf("proposed again: %+v", m)
	}

	*at = at.Add(Keep + time.Hour)
	if p, err := Prune(); err != nil || p.Removed != 2 {
		t.Fatalf("second: %+v %v", p, err)
	}
	all, _, _ := All()
	var left []string
	for _, m := range all {
		left = append(left, m.ID)
	}
	slices.Sort(left)
	want := []string{pinned.ID, mine.ID}
	slices.Sort(want)
	if !slices.Equal(left, want) {
		t.Fatalf("left %v, want %v", left, want)
	}
}

// Undoing a tidy keeps the memory a merge made when one it was made from
// has been removed since: it is all that is left of that one.
func TestUntidyKeepsAMergeWhoseMemberWasPruned(t *testing.T) {
	at := fresh(t)
	a := infer(t, "Ships on Tuesdays", Global)
	b := infer(t, "Releases go out every Tuesday", Global)
	cs, err := Tidy(Plan{Merges: []Merge{{IDs: []string{a.ID, b.ID}, Text: "Releases ship on Tuesdays"}}})
	if err != nil || len(cs) != 1 {
		t.Fatalf("%+v %v", cs, err)
	}
	*at = at.Add(Keep + time.Hour)
	if _, err := Edit(cs[0].Into, Input{Pinned: ptr(true)}, false); err != nil { // the merged one stays
		t.Fatal(err)
	}
	if _, err := Prune(); err != nil {
		t.Fatal(err)
	}
	if _, err := Edit(cs[0].Into, Input{Pinned: ptr(false)}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Untidy(cs); err != nil {
		t.Fatal(err)
	}
	if m := get(t, cs[0].Into); m.Text != "Releases ship on Tuesdays" {
		t.Fatalf("merged one: %+v", m)
	}
}

func ptr[T any](v T) *T { return &v }
