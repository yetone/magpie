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
