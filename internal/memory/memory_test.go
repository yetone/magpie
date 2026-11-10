package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fresh points memory.json at a file of the test's own, and holds the
// clock at a time the test moves.
func fresh(t *testing.T) *time.Time {
	t.Helper()
	p := filepath.Join(t.TempDir(), "memory.json")
	old, oldNow := Path, now
	Path = func() string { return p }
	at := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	now = func() time.Time { return at }
	t.Cleanup(func() { Path, now = old, oldNow })
	return &at
}

func str(s string) *string { return &s }

func save(t *testing.T, text, kind, scope string, tags ...string) *Memory {
	t.Helper()
	m, _, err := Save(Input{Text: &text, Kind: &kind, Scope: &scope, Tags: &tags}, FromAgent, &Source{Agent: "codex"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSaveKeepsOneOfTheSameText(t *testing.T) {
	fresh(t)
	a := save(t, "UI uses no gradients.", Preference, Global, "ui")
	b, added, err := Save(Input{Text: str("  ui uses no   gradients "), Kind: str(Preference), Scope: str(Global), Tags: &[]string{"Design"}}, FromAgent, &Source{Agent: "claude"}, nil)
	if err != nil || added {
		t.Fatalf("the same text again: added %v, %v", added, err)
	}
	if b.ID != a.ID || len(b.Sources) != 2 || strings.Join(b.Tags, ",") != "ui,design" {
		t.Fatalf("merged into %+v", b)
	}
	// another scope is another memory
	if _, added, _ := Save(Input{Text: str("UI uses no gradients."), Kind: str(Preference), Scope: str("project:github.com/a/b")}, FromAgent, nil, nil); !added {
		t.Fatal("the same text in a project's scope wasn't added")
	}
	all, _, _ := All()
	if len(all) != 2 {
		t.Fatalf("%d memories, want 2", len(all))
	}
}

// What the user typed is theirs: an agent saving the same text later
// adds its source, never its kind or why.
func TestUsersMemoryNotChangedByAnAgentsSave(t *testing.T) {
	fresh(t)
	m := save(t, "Commit with explicit file names", Rule, Global)
	if _, err := Edit(m.ID, Input{Why: str("other sessions' work sits in the tree")}, true); err != nil {
		t.Fatal(err)
	}
	got, _, err := Save(Input{Text: str("commit with explicit file names."), Kind: str(Preference), Scope: str(Global), Why: str("an agent's guess")}, FromAgent, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != Rule || got.Why != "other sessions' work sits in the tree" || got.Origin != FromUser {
		t.Fatalf("the user's memory became %+v", got)
	}
}

func TestSaveRefusesWhatIsntAMemory(t *testing.T) {
	fresh(t)
	for name, in := range map[string]Input{
		"no text":   {Text: str("  ")},
		"bad kind":  {Text: str("x"), Kind: str("mood")},
		"bad scope": {Text: str("x"), Scope: str("project:")},
		"too long":  {Text: str(strings.Repeat("字", MaxText+1))},
	} {
		if _, _, err := Save(in, FromUser, nil, nil); err == nil {
			t.Errorf("%s: saved", name)
		}
	}
}

func TestForgetSetsAsideAndRemoveDeletes(t *testing.T) {
	fresh(t)
	m := save(t, "The test box is mote", Reference, Global)
	if f, err := Forget(m.ID); err != nil || f.Status != Archived {
		t.Fatalf("forget: %+v, %v", f, err)
	}
	if hits, _ := Search(Query{Text: "mote"}); len(hits) != 0 {
		t.Fatalf("an archived memory was found: %+v", hits)
	}
	if hits, _ := Search(Query{Text: "mote", Status: Archived}); len(hits) != 1 {
		t.Fatal("not found among the archived")
	}
	// saved again, an archived memory is a new one
	if _, added, _ := Save(Input{Text: str("The test box is mote"), Kind: str(Reference)}, FromAgent, nil, nil); !added {
		t.Fatal("an archived memory took the save")
	}
	if err := Remove(m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := Get(m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed memory: %v", err)
	}
	if err := Remove("m_nothing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removing nothing: %v", err)
	}
}

// A memory.json that can't be read is unknown: a save fails, and the file
// is left as it was for the user to recover.
func TestUnreadableFileIsNeverWrittenOver(t *testing.T) {
	fresh(t)
	bad := []byte(`{"version":1,"memories":[{"id":"m_1","text":"kept`)
	if err := os.WriteFile(Path(), bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Save(Input{Text: str("new")}, FromUser, nil, nil); err == nil {
		t.Fatal("saved over a file that can't be read")
	}
	if b, _ := os.ReadFile(Path()); string(b) != string(bad) {
		t.Fatalf("the file became %q", b)
	}
	if _, _, err := All(); err == nil {
		t.Fatal("an unreadable file listed as empty")
	}
	if _, err := Digest("codex"); err == nil {
		t.Fatal("an unreadable file's digest read as empty")
	}
}

// The last good generation is read when the file is cut short.
func TestCutShortFileReadFromItsBackup(t *testing.T) {
	fresh(t)
	save(t, "first", Fact, Global)
	save(t, "second", Fact, Global) // keeps the first generation as .bak
	if err := os.WriteFile(Path(), []byte(`{"memo`), 0o600); err != nil {
		t.Fatal(err)
	}
	all, _, err := All()
	if err != nil || len(all) != 1 || all[0].Text != "first" {
		t.Fatalf("read %v, %v; want the backup's one memory", all, err)
	}
}

func TestCleanTags(t *testing.T) {
	got := strings.Join(CleanTags([]string{" #Go ", "go", "Release Notes", "", strings.Repeat("x", 41)}), ",")
	if got != "go,release-notes" {
		t.Fatalf("tags %q", got)
	}
}

// A memory distilled from a conversation that the user deleted or
// archived isn't proposed again; an agent's own save of the same text
// still is kept, and one the user archived stays set aside.
func TestDismissedNotProposedAgain(t *testing.T) {
	fresh(t)
	cand := Candidate
	propose := func(text, scope string) (*Memory, bool) {
		t.Helper()
		k := Preference
		m, added, err := Save(Input{Text: &text, Kind: &k, Scope: &scope, Status: &cand}, FromInferred, &Source{Agent: "codex", Session: "s1"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return m, added
	}
	a, added := propose("Prefers pnpm.", Global)
	if !added || a.Status != Candidate || a.Origin != FromInferred {
		t.Fatalf("first proposal: %+v %v", a, added)
	}
	if err := Remove(a.ID); err != nil {
		t.Fatal(err)
	}
	if m, added := propose("prefers  PNPM", Global); added || m != nil {
		t.Fatalf("deleted, then proposed again: %+v", m)
	}
	if m, added := propose("Prefers pnpm.", "project:p1"); added || m != nil {
		t.Fatalf("deleted, then proposed again in another scope: %+v", m)
	}
	// the user's or an agent's own save is theirs to make
	if m := save(t, "Prefers pnpm.", Preference, Global); m == nil || m.Status != Active {
		t.Fatalf("an agent's save of a dismissed text: %+v", m)
	}

	// archived by the user: dismissed too
	b, _ := propose("Answers in Chinese.", Global)
	if _, err := Edit(b.ID, Input{Status: str(Archived)}, true); err != nil {
		t.Fatal(err)
	}
	if m, added := propose("Answers in Chinese.", Global); added || m != nil {
		t.Fatalf("archived, then proposed again: %+v", m)
	}
	// archived by an agent (memory_forget): set aside, not proposed again
	// in that scope, but not dismissed
	c, _ := propose("Runs tests on mote.", Global)
	if _, err := Forget(c.ID); err != nil {
		t.Fatal(err)
	}
	if m, added := propose("Runs tests on mote.", Global); added || m == nil || m.ID != c.ID || m.Status != Archived {
		t.Fatalf("set aside, then proposed again: %+v %v", m, added)
	}
	if m, added := propose("Runs tests on mote.", "project:p1"); !added || m == nil {
		t.Fatalf("set aside by an agent in one scope, not proposed in another: %+v", m)
	}
	f, err := view()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Dismissed) != 2 {
		t.Fatalf("dismissed: %q", f.Dismissed)
	}
}

// Some memories to review, kept or archived at once (RealZeroP27 on X):
// the ones named change in one write as Edit changes one, an archived
// candidate is not proposed again, an id no longer there is passed over,
// and a status that isn't one changes none of them.
func TestSetStatusOfSome(t *testing.T) {
	fresh(t)
	cand := Candidate
	propose := func(text string) *Memory {
		t.Helper()
		k, s := Preference, Global
		m, _, err := Save(Input{Text: &text, Kind: &k, Scope: &s, Status: &cand}, FromInferred, &Source{Agent: "codex"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	a, b, c, d := propose("Prefers pnpm."), propose("Ships on Fridays."), propose("Answers in Chinese."), propose("Runs tests on mote.")
	status := func(id string) string {
		t.Helper()
		m, err := Get(id)
		if err != nil {
			t.Fatal(err)
		}
		return m.Status
	}

	if n, err := SetStatus(Active, a.ID, b.ID, "m_gone"); err != nil || n != 2 {
		t.Fatalf("kept %d, %v", n, err)
	}
	if status(a.ID) != Active || status(b.ID) != Active || status(c.ID) != Candidate || status(d.ID) != Candidate {
		t.Fatalf("after keeping two: %s %s %s %s", status(a.ID), status(b.ID), status(c.ID), status(d.ID))
	}
	if hits, _ := Search(Query{Text: "pnpm"}); len(hits) != 1 {
		t.Fatalf("a kept memory isn't searched: %+v", hits)
	}

	if n, err := SetStatus("gone", c.ID, d.ID); err == nil || n != 0 {
		t.Fatalf("a bad status: %d, %v", n, err)
	}
	if status(c.ID) != Candidate || status(d.ID) != Candidate {
		t.Fatal("a bad status changed a memory")
	}

	if n, err := SetStatus(Archived, c.ID, d.ID); err != nil || n != 2 {
		t.Fatalf("archived %d, %v", n, err)
	}
	if status(c.ID) != Archived || status(d.ID) != Archived {
		t.Fatal("not archived")
	}
	if m := propose("answers in chinese"); m != nil {
		t.Fatalf("archived, then proposed again: %+v", m)
	}

	if _, err := SetStatus(Active, "m_gone", "m_none"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("none of them there: %v", err)
	}
}
