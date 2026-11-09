package memory

import (
	"slices"
	"testing"
	"time"
)

func infer(t *testing.T, text, scope string, tags ...string) *Memory {
	t.Helper()
	k, st := Fact, Candidate
	m, _, err := Save(Input{Text: &text, Kind: &k, Scope: &scope, Tags: &tags, Status: &st}, FromInferred, &Source{Agent: "claude", Quote: text}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func get(t *testing.T, id string) *Memory {
	t.Helper()
	m, err := Get(id)
	if err != nil {
		t.Fatalf("%s: %v", id, err)
	}
	return m
}

// Two memories that say the same thing become one, which carries both
// one's tags and sources and is in use when either was; the two are kept,
// superseded by it. Undone, the two are back as they were and the merged
// one is gone.
func TestTidyMergesAndUndoes(t *testing.T) {
	at := fresh(t)
	a := save(t, "Ships on Tuesdays", Fact, Global, "release")
	*at = at.Add(time.Hour)
	b := infer(t, "Releases go out every Tuesday", Global, "ship")
	cs, err := Tidy(Plan{Merges: []Merge{{IDs: []string{b.ID, a.ID}, Text: "Releases ship on Tuesdays", Kind: "nonsense"}}})
	if err != nil || len(cs) != 1 || !cs[0].New {
		t.Fatalf("%+v %v", cs, err)
	}
	m := get(t, cs[0].Into)
	if m.Text != "Releases ship on Tuesdays" || m.Kind != Fact || m.Status != Active || m.Origin != FromInferred ||
		!slices.Equal(m.Tags, []string{"ship", "release"}) || len(m.Sources) != 2 || !m.Created.Equal(a.Created) {
		t.Fatalf("merged: %+v", m)
	}
	for _, id := range []string{a.ID, b.ID} {
		if o := get(t, id); o.Status != Superseded || o.SupersededBy != m.ID {
			t.Fatalf("member: %+v", o)
		}
	}
	if n, err := Untidy(cs); err != nil || n != 2 {
		t.Fatalf("undo: %d %v", n, err)
	}
	if get(t, a.ID).Status != Active || get(t, b.ID).Status != Candidate || get(t, a.ID).SupersededBy != "" {
		t.Fatal("not back as they were")
	}
	if _, err := Get(m.ID); err != ErrNotFound {
		t.Fatalf("merged one left: %v", err)
	}
}

// What the user wrote or pinned is never rewritten or set aside: merged
// with others it is the one kept, word for word; two of them aren't
// merged; and named stale, it stays.
func TestTidyNeverChangesTheUsers(t *testing.T) {
	fresh(t)
	u, _, err := Save(Input{Text: str("Use tabs"), Kind: str(Preference), Scope: str(Global)}, FromUser, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := infer(t, "Pinned one", Global)
	pin := true
	if _, err := Edit(p.ID, Input{Pinned: &pin}, false); err != nil {
		t.Fatal(err)
	}
	x := infer(t, "Indents with tabs", Global)
	y := infer(t, "Old fact", Global)
	cs, err := Tidy(Plan{
		Merges: []Merge{{IDs: []string{x.ID, u.ID}, Text: "Rewritten"}, {IDs: []string{u.ID, p.ID}, Text: "Both"}},
		Stale:  []Stale{{ID: u.ID}, {ID: p.ID}, {ID: y.ID, Why: "done"}},
	})
	if err != nil || len(cs) != 2 {
		t.Fatalf("%+v %v", cs, err)
	}
	if c := cs[0]; c.New || c.Into != u.ID || !slices.Equal(c.IDs, []string{x.ID}) {
		t.Fatalf("merge: %+v", c)
	}
	if m := get(t, u.ID); m.Text != "Use tabs" || m.Status != Active {
		t.Fatalf("user's: %+v", m)
	}
	if get(t, p.ID).Status != Candidate || get(t, x.ID).SupersededBy != u.ID || get(t, y.ID).Status != Archived {
		t.Fatal("wrong ones changed")
	}
	if n, _ := Untidy(cs); n != 2 || get(t, x.ID).Status != Candidate || get(t, y.ID).Status != Candidate {
		t.Fatalf("undo brought back %d", n)
	}
	if _, err := Get(u.ID); err != nil {
		t.Fatal("undo removed the user's memory it was merged into")
	}
}

// A merge across scopes, or of memories gone or out of use, isn't done.
func TestTidySkipsWhatNoLongerHolds(t *testing.T) {
	fresh(t)
	a := infer(t, "One", Global)
	b := infer(t, "Two", "project:github.com/a/b")
	c := infer(t, "Three", Global)
	if _, err := Forget(c.ID); err != nil {
		t.Fatal(err)
	}
	cs, err := Tidy(Plan{Merges: []Merge{
		{IDs: []string{a.ID, b.ID}, Text: "x"},
		{IDs: []string{a.ID, c.ID}, Text: "x"},
		{IDs: []string{a.ID, "m_gone"}, Text: "x"},
		{IDs: []string{a.ID, a.ID}, Text: "x"},
	}, Stale: []Stale{{ID: c.ID}}})
	if err != nil || len(cs) != 0 {
		t.Fatalf("%+v %v", cs, err)
	}
}

// A candidate distilled from a conversation and left unreviewed past
// Before is set aside; a newer one, or one an agent saved, stays.
func TestTidySetsAsideUnreviewed(t *testing.T) {
	at := fresh(t)
	old := infer(t, "Old candidate", Global)
	agent := save(t, "Agent's", Fact, Global)
	*at = at.Add(40 * 24 * time.Hour)
	nw := infer(t, "New candidate", Global)
	cs, err := Tidy(Plan{Before: at.Add(-30 * 24 * time.Hour)})
	if err != nil || len(cs) != 1 || cs[0].IDs[0] != old.ID || cs[0].Why != Unreviewed {
		t.Fatalf("%+v %v", cs, err)
	}
	if get(t, old.ID).Status != Archived || get(t, nw.ID).Status != Candidate || get(t, agent.ID).Status != Active {
		t.Fatal("wrong ones set aside")
	}
}

// Undo leaves alone what the user changed since: a memory they brought
// back or archived themselves stays as they left it, and a merged one
// they edited stays theirs.
func TestUntidyKeepsWhatTheUserChanged(t *testing.T) {
	fresh(t)
	a := infer(t, "A one", Global)
	b := infer(t, "A two", Global)
	c := infer(t, "Stale", Global)
	cs, err := Tidy(Plan{Merges: []Merge{{IDs: []string{a.ID, b.ID}, Text: "A"}}, Stale: []Stale{{ID: c.ID}}})
	if err != nil || len(cs) != 2 {
		t.Fatalf("%+v %v", cs, err)
	}
	if _, err := Edit(cs[0].Into, Input{Text: str("A, as I put it")}, true); err != nil {
		t.Fatal(err)
	}
	act := Active
	if _, err := Edit(c.ID, Input{Status: &act}, true); err != nil {
		t.Fatal(err)
	}
	if n, err := Untidy(cs); err != nil || n != 2 {
		t.Fatalf("undo: %d %v", n, err)
	}
	if m := get(t, cs[0].Into); m.Text != "A, as I put it" {
		t.Fatalf("edited merge: %+v", m)
	}
	if get(t, c.ID).Status != Active {
		t.Fatal("the user's own change undone")
	}
}
