package memory

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func mdOf(id string) string { return filepath.Join(Dir(), id+".md") }

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// handWrite writes p as a person's editor would, a second later than
// anything before it.
func handWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Second)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
}

// Each memory is a Markdown file of its own: its fields as front matter,
// its text as the body, and nothing else is kept in one file of all.
func TestEachMemoryIsAMarkdownFile(t *testing.T) {
	fresh(t)
	m := save(t, "Releases go out on Tuesdays", Fact, Global, "release")
	s := readFile(t, mdOf(m.ID))
	for _, want := range []string{"---\nkind: fact\n", "scope: global\n", "tags: [release]\n", "origin: agent\n", "---\n\nReleases go out on Tuesdays\n"} {
		if !strings.Contains(s, want) {
			t.Fatalf("%q not in\n%s", want, s)
		}
	}
	if _, err := os.Stat(Path()); !errors.Is(err, fs404) {
		t.Fatalf("memory.json written: %v", err)
	}
	back := get(t, m.ID)
	if back.Text != m.Text || !back.Created.Equal(m.Created) || back.Sources[0].Agent != "codex" || back.Origin != FromAgent {
		t.Fatalf("read back %+v", back)
	}
	// a project's scope holds a path, its case kept
	p := save(t, "Uses make", Fact, "project:dir:/Users/Me/Proj")
	if got := get(t, p.ID); got.Scope != "project:dir:/Users/Me/Proj" {
		t.Fatalf("scope read back as %q", got.Scope)
	}
}

var fs404 = os.ErrNotExist

// memory.json from before is moved into the folder on the first read, all
// of it: memories, how often each was used, projects, dismissed texts,
// deletions and declined suggestions. It is kept as memory.json.migrated,
// and nothing moved reads as the user's hand edit.
func TestMemoryJSONIsMovedIn(t *testing.T) {
	fresh(t)
	old := `{"version":1,"memories":[
{"id":"m_a","text":"Uses pnpm","kind":"preference","scope":"global","origin":"inferred","status":"candidate","created":"2026-11-01T10:00:00+08:00","updated":"2026-11-02T10:00:00+08:00","used":"2026-11-03T10:00:00+08:00","uses":4,"sources":[{"agent":"claude","quote":"use pnpm","at":"2026-11-01T10:00:00+08:00"}]},
{"id":"m_b","text":"Deploys need the VPN","kind":"fact","scope":"project:github.com/x/y","origin":"user","status":"active","pinned":true,"why":"the host is private","created":"2026-11-01T11:00:00Z","updated":"2026-11-01T11:00:00Z","edited":"2026-11-01T11:00:00Z"}],
"projects":{"github.com/x/y":{"key":"github.com/x/y","name":"y","roots":["/w/y"]}},
"dismissed":["tabs, not spaces"],"removed":{"m_gone":"2026-11-05T00:00:00Z"},"declined":["k1"]}`
	if err := os.WriteFile(Path(), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	all, projects, err := All()
	if err != nil || len(all) != 2 {
		t.Fatalf("%v %v", all, err)
	}
	a, b := get(t, "m_a"), get(t, "m_b")
	if a.Origin != FromInferred || a.Status != Candidate || a.Uses != 4 || a.Used.IsZero() || !a.Edited.IsZero() || len(a.Sources) != 1 {
		t.Fatalf("m_a: %+v", a)
	}
	if !b.Pinned || b.Why != "the host is private" || !b.Edited.Equal(b.Updated) || b.Scope != "project:github.com/x/y" {
		t.Fatalf("m_b: %+v", b)
	}
	if projects["github.com/x/y"] == nil {
		t.Fatalf("projects %v", projects)
	}
	bun, err := Collect()
	if err != nil || !slices.Equal(bun.Dismissed, []string{"tabs, not spaces"}) || bun.Removed["m_gone"].IsZero() || !slices.Equal(bun.Declined, []string{"k1"}) {
		t.Fatalf("%+v %v", bun, err)
	}
	if _, err := os.Stat(Path()); !errors.Is(err, fs404) {
		t.Fatal("memory.json left in place")
	}
	if s := readFile(t, Path()+".migrated"); s != old {
		t.Fatal("memory.json not kept as it was")
	}
	if !strings.Contains(readFile(t, mdOf("m_a")), "Uses pnpm") {
		t.Fatal("no file for m_a")
	}
}

// A memory file the user edits by hand is their change: theirs (Fixed),
// so a tidy leaves it and a sync of an agent's older change from another
// computer doesn't write over it; and its text is what they wrote.
func TestHandEditIsTheUsers(t *testing.T) {
	a, b, at := twoComputers(t)
	a.use(t)
	m := infer(t, "Uses npm", Global)
	carry(t, a, b)
	a.use(t)
	p := mdOf(m.ID)
	handWrite(t, p, strings.Replace(readFile(t, p), "\n\nUses npm\n", "\n\nUses pnpm, never npm\n", 1))
	got := get(t, m.ID)
	if got.Text != "Uses pnpm, never npm" || got.Origin != FromUser || !Fixed(got) || !byUser(got) {
		t.Fatalf("hand edit read as %+v", got)
	}
	if cs, err := Tidy(Plan{Stale: []Stale{{ID: m.ID, Why: "old"}}, Before: at.Add(time.Hour)}); err != nil || len(cs) != 0 {
		t.Fatalf("tidy changed the user's: %+v %v", cs, err)
	}
	// on the other computer an agent changes it later than the hand edit
	b.use(t)
	*at = time.Now().Add(24 * time.Hour)
	if _, err := Edit(m.ID, Input{Tags: &[]string{"js"}}, false); err != nil {
		t.Fatal(err)
	}
	carry(t, b, a)
	a.use(t)
	if got := get(t, m.ID); got.Text != "Uses pnpm, never npm" {
		t.Fatalf("an agent's sync wrote over the hand edit: %+v", got)
	}
	if !strings.Contains(readFile(t, p), "Uses pnpm, never npm") {
		t.Fatalf("file: %s", readFile(t, p))
	}
}

// A file the user adds is a memory of theirs, its name its id; what the
// front matter leaves out is a fact that holds everywhere, in use.
func TestHandAddedFileIsAMemory(t *testing.T) {
	fresh(t)
	save(t, "something", Fact, Global) // the folder and meta.json are there
	handWrite(t, mdOf("vpn"), "Deploys need the VPN\n")
	handWrite(t, mdOf("tz"), "---\nkind: preference\ntags: [time]\ncreated: 2026-11-01\n---\nDates in UTC+8\n")
	v, z := get(t, "vpn"), get(t, "tz")
	if v.Text != "Deploys need the VPN" || v.Kind != Fact || v.Scope != Global || v.Status != Active || v.Origin != FromUser {
		t.Fatalf("vpn: %+v", v)
	}
	if z.Kind != Preference || !slices.Equal(z.Tags, []string{"time"}) || z.Created.Day() != 1 {
		t.Fatalf("tz: %+v", z)
	}
	hits, err := Search(Query{Text: "VPN"})
	if err != nil || len(hits) != 1 || hits[0].ID != "vpn" {
		t.Fatalf("search: %+v %v", hits, err)
	}
}

// A memory file that can't be read is unknown: left out, and never
// written over nor removed, whatever changes the rest, a sync included.
func TestBrokenFileIsLeftAsItIs(t *testing.T) {
	fresh(t)
	m := save(t, "kept", Fact, Global)
	bad := "---\nkind: [fact\n---\nhalf typed"
	handWrite(t, mdOf(m.ID), bad)
	all, _, err := All()
	if err != nil || len(all) != 0 {
		t.Fatalf("%v %v", all, err)
	}
	save(t, "another", Fact, Global)
	other := *m
	other.Text, other.Updated = "from elsewhere", m.Updated.Add(time.Hour)
	if _, err := Put(&Bundle{Memories: []*Memory{&other}}); err != nil {
		t.Fatal(err)
	}
	if err := Remove(m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed: %v", err)
	}
	if s := readFile(t, mdOf(m.ID)); s != bad {
		t.Fatalf("written over: %q", s)
	}
	// fixed by hand, it is read again
	handWrite(t, mdOf(m.ID), "kept, fixed")
	if got := get(t, m.ID); got.Text != "kept, fixed" {
		t.Fatalf("%+v", got)
	}
}

// A change to several memories cut off halfway is finished by the next
// read: never left half done.
func TestCutOffChangeIsFinished(t *testing.T) {
	fresh(t)
	a := infer(t, "Ships on Tuesdays", Global)
	b := infer(t, "Releases go out every Tuesday", Global)
	n := 0
	cut = func() error {
		if n++; n == 1 {
			return errors.New("crash")
		}
		return nil
	}
	_, err := Tidy(Plan{Merges: []Merge{{IDs: []string{a.ID, b.ID}, Text: "Releases ship on Tuesdays"}}})
	cut = func() error { return nil }
	if err == nil {
		t.Fatal("the cut didn't stop the write")
	}
	all, _, err := All()
	if err != nil {
		t.Fatal(err)
	}
	var merged *Memory
	for _, m := range all {
		if m.Text == "Releases ship on Tuesdays" {
			merged = m
		}
	}
	if merged == nil || get(t, a.ID).SupersededBy != merged.ID || get(t, b.ID).SupersededBy != merged.ID {
		t.Fatalf("left half done: %+v", all)
	}
	if _, err := os.Stat(filepath.Join(Dir(), "journal.json")); !errors.Is(err, fs404) {
		t.Fatalf("journal left: %v", err)
	}
	// and none of it reads as a hand edit
	if merged.Origin != FromInferred || get(t, a.ID).Origin != FromInferred {
		t.Fatalf("read as the user's: %+v", merged)
	}
}

// A hand edit made while magpie changes the same memory stays: the write
// leaves a file that changed since it was read.
func TestHandEditDuringAChangeStays(t *testing.T) {
	fresh(t)
	m := save(t, "Uses npm", Fact, Global)
	err := change(func(f *file) error {
		handWrite(t, mdOf(m.ID), "Uses pnpm")
		find(f, m.ID).Tags = []string{"js"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := get(t, m.ID); got.Text != "Uses pnpm" || got.Origin != FromUser {
		t.Fatalf("%+v", got)
	}
}

// A memory removed while its file is edited by hand stays: the edit is
// the user's, and they kept it.
func TestHandEditDuringARemoveStays(t *testing.T) {
	fresh(t)
	m := save(t, "Uses npm", Fact, Global)
	err := change(func(f *file) error {
		handWrite(t, mdOf(m.ID), "Uses pnpm")
		f.Memories = nil
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := get(t, m.ID); got.Text != "Uses pnpm" || got.Origin != FromUser {
		t.Fatalf("%+v", got)
	}
}

// A search's use is kept in meta.json: the memory's own file isn't
// rewritten for it.
func TestUseDoesNotRewriteTheMemory(t *testing.T) {
	at := fresh(t)
	m := save(t, "Deploys need the VPN", Fact, Global)
	before := readFile(t, mdOf(m.ID))
	*at = at.Add(time.Hour)
	if hits, err := Search(Query{Text: "VPN"}); err != nil || len(hits) != 1 {
		t.Fatalf("%v %v", hits, err)
	}
	if got := get(t, m.ID); got.Uses != 1 {
		t.Fatalf("use not kept: %+v", got)
	}
	if readFile(t, mdOf(m.ID)) != before {
		t.Fatal("the memory's file was rewritten for a search")
	}
}

// index.json is a cache: gone or garbled, memory reads the same.
func TestIndexIsOnlyACache(t *testing.T) {
	fresh(t)
	save(t, "one", Fact, Global)
	save(t, "two", Fact, Global)
	want, _, _ := All()
	for _, s := range []string{"", "{garbled"} {
		p := filepath.Join(Dir(), "index.json")
		if s == "" {
			os.Remove(p)
		} else {
			os.WriteFile(p, []byte(s), 0o600)
		}
		got, _, err := All()
		if err != nil || len(got) != len(want) || got[0].Text != want[0].Text || got[1].Text != want[1].Text {
			t.Fatalf("%q: %v %v", s, got, err)
		}
	}
}
