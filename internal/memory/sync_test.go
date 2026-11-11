package memory

import (
	"os"
	"testing"
	"time"
)

// two computers' memory.json, as Put and Collect see them: a test moves
// between them by swapping Path
type computer struct{ path string }

func (c computer) use(t *testing.T) {
	t.Helper()
	Path = func() string { return c.path }
}

func twoComputers(t *testing.T) (a, b computer, at *time.Time) {
	at = fresh(t)
	a = computer{Path()}
	b = computer{t.TempDir() + "/memory.json"}
	return a, b, at
}

// carry merges what from has into to, as a sync does.
func carry(t *testing.T, from, to computer) bool {
	t.Helper()
	from.use(t)
	bun, err := Collect()
	if err != nil {
		t.Fatal(err)
	}
	to.use(t)
	changed, err := Put(bun)
	if err != nil {
		t.Fatal(err)
	}
	return changed
}

func texts(t *testing.T) map[string]*Memory {
	t.Helper()
	all, _, err := All()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*Memory{}
	for _, m := range all {
		out[m.Text] = m
	}
	return out
}

func TestSyncCarriesMemoriesBothWays(t *testing.T) {
	a, b, at := twoComputers(t)
	a.use(t)
	save(t, "Deploys go through mote", Procedure, Global)
	b.use(t)
	*at = at.Add(time.Minute)
	save(t, "Replies to users are in Chinese", Preference, Global)
	carry(t, a, b)
	carry(t, b, a)
	for _, c := range []computer{a, b} {
		c.use(t)
		if got := texts(t); len(got) != 2 {
			t.Fatalf("%s has %d memories, want both", c.path, len(got))
		}
	}
	// nothing new: nothing written
	if carry(t, a, b) || carry(t, b, a) {
		t.Fatal("a sync with nothing new changed memory.json")
	}
}

func TestSyncDeletionStaysDeleted(t *testing.T) {
	a, b, at := twoComputers(t)
	a.use(t)
	m := save(t, "Old fact", Fact, Global)
	carry(t, a, b)
	a.use(t)
	*at = at.Add(time.Minute)
	if err := Remove(m.ID); err != nil {
		t.Fatal(err)
	}
	// b still has it: merged back, it stays gone on a, and goes from b
	carry(t, b, a)
	a.use(t)
	if _, ok := texts(t)["Old fact"]; ok {
		t.Fatal("a memory deleted here came back from a computer that had it")
	}
	carry(t, a, b)
	b.use(t)
	if _, ok := texts(t)["Old fact"]; ok {
		t.Fatal("the deletion didn't reach the other computer")
	}
	// a deletion remembered keepRemoved: after it, the record goes
	*at = at.Add(keepRemoved + time.Hour)
	a.use(t)
	bun, _ := Collect()
	if len(bun.Removed) != 0 {
		t.Fatalf("a deletion older than keepRemoved is still carried: %v", bun.Removed)
	}
}

func TestSyncEditAfterDeletionKeepsIt(t *testing.T) {
	a, b, at := twoComputers(t)
	a.use(t)
	m := save(t, "Fact", Fact, Global)
	carry(t, a, b)
	a.use(t)
	*at = at.Add(time.Minute)
	if err := Remove(m.ID); err != nil {
		t.Fatal(err)
	}
	b.use(t)
	*at = at.Add(time.Minute)
	if _, err := Edit(m.ID, Input{Text: str("Fact, corrected")}, true); err != nil {
		t.Fatal(err)
	}
	carry(t, b, a)
	a.use(t)
	if _, ok := texts(t)["Fact, corrected"]; !ok {
		t.Fatal("an edit made after the deletion elsewhere was lost")
	}
}

func TestSyncLaterChangeWins(t *testing.T) {
	a, b, at := twoComputers(t)
	a.use(t)
	m := save(t, "Tabs", Preference, Global)
	carry(t, a, b)
	a.use(t)
	*at = at.Add(time.Minute)
	Edit(m.ID, Input{Text: str("Tabs, width 4")}, true)
	b.use(t)
	*at = at.Add(time.Minute)
	Edit(m.ID, Input{Text: str("Tabs, width 8")}, true)
	carry(t, a, b)
	carry(t, b, a)
	for _, c := range []computer{a, b} {
		c.use(t)
		got := texts(t)
		if _, ok := got["Tabs, width 8"]; !ok || len(got) != 1 {
			t.Fatalf("%s: %v, want the later edit alone", c.path, got)
		}
	}
}

// A tidy on one computer, later than the user's pin on the other but
// before it saw the pin, never sets the user's memory aside.
func TestSyncMachineChangeNeverOverUsersOwn(t *testing.T) {
	a, b, at := twoComputers(t)
	a.use(t)
	m := save(t, "Use mote for builds", Rule, Global)
	carry(t, a, b)
	b.use(t)
	*at = at.Add(time.Minute)
	pin := true
	Edit(m.ID, Input{Pinned: &pin}, true)
	a.use(t)
	*at = at.Add(time.Minute)
	if _, err := Forget(m.ID); err != nil { // not the user's: an agent's forget
		t.Fatal(err)
	}
	carry(t, a, b)
	carry(t, b, a)
	for _, c := range []computer{a, b} {
		c.use(t)
		got := texts(t)["Use mote for builds"]
		if got == nil || !got.Pinned || got.Status != Active {
			t.Fatalf("%s: %+v, want the pinned memory active", c.path, got)
		}
	}
	// the user unpinning it later is theirs, and goes everywhere
	*at = at.Add(time.Minute)
	pin = false
	Edit(m.ID, Input{Pinned: &pin}, true)
	carry(t, b, a)
	a.use(t)
	if texts(t)["Use mote for builds"].Pinned {
		t.Fatal("the user's unpin didn't go to the other computer")
	}
}

func TestSyncKeepsMostUses(t *testing.T) {
	a, b, at := twoComputers(t)
	a.use(t)
	m := save(t, "Fact", Fact, Global)
	carry(t, a, b)
	a.use(t)
	*at = at.Add(2 * time.Minute)
	touch([]string{m.ID})
	if !carry(t, a, b) {
		t.Fatal("uses from the other computer weren't taken")
	}
	if got := texts(t)["Fact"]; got.Uses != 1 || !got.Used.Equal(*at) {
		t.Fatalf("%+v, want a's use", got)
	}
	// a use alone is no change to sync
	a.use(t)
	x, _ := Collect()
	*at = at.Add(2 * time.Minute)
	touch([]string{m.ID})
	y, _ := Collect()
	if x.Hash() != y.Hash() {
		t.Fatal("a search changed what tells a sync something changed")
	}
}

func TestCombineIsTheSameEitherWay(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	fresh(t)
	a := &Bundle{
		Memories:  []*Memory{{ID: "m_1", Text: "x", Updated: t0}, {ID: "m_2", Text: "y", Updated: t0}},
		Projects:  map[string]*Project{"p": {Key: "p", Name: "p", Roots: []string{"/a"}}},
		Dismissed: []string{"b", "a"},
	}
	b := &Bundle{
		Memories:  []*Memory{{ID: "m_1", Text: "x2", Updated: t0}, {ID: "m_3", Text: "z", Updated: t0}},
		Projects:  map[string]*Project{"p": {Key: "p", Name: "q", Roots: []string{"C:\\a"}}},
		Dismissed: []string{"c"},
		Removed:   map[string]time.Time{"m_2": t0.Add(time.Hour)},
	}
	x, y := Combine(a, b), Combine(b, a)
	if x.Hash() != y.Hash() {
		t.Fatal("Combine(a, b) differs from Combine(b, a)")
	}
	if len(x.Memories) != 2 || len(x.Projects["p"].Roots) != 2 || len(x.Dismissed) != 3 {
		t.Fatalf("merged %+v", x)
	}
}

func TestCollectUnreadableIsAnError(t *testing.T) {
	fresh(t)
	if b, err := Collect(); b != nil || err != nil {
		t.Fatalf("no memory.json: %v, %v; want nothing", b, err)
	}
	if err := os.WriteFile(Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Collect(); err == nil {
		t.Fatal("an unreadable memory.json was collected")
	}
	if _, err := Put(&Bundle{Memories: []*Memory{{ID: "m_1", Text: "x"}}}); err == nil {
		t.Fatal("a sync wrote over an unreadable memory.json")
	}
}
