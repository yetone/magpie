package memory

import (
	"os"
	"strings"
	"testing"
	"time"
)

func lastDay(t *testing.T) StatDay {
	t.Helper()
	r, err := Stats(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Days) != 7 {
		t.Fatalf("%d days", len(r.Days))
	}
	return r.Days[6]
}

// Each way memory changes is counted on its day as one thing: made (by
// origin), updated, or leaving use (by who), and an agent's search with
// words is counted with what it found. The same text saved again is an
// update, not a new memory; a change of status alone is no update; a
// memory set aside twice, or deleted once set aside, leaves use once.
func TestStatsCountEachChange(t *testing.T) {
	at := fresh(t)
	a := save(t, "Uses pnpm", Preference, Global)
	save(t, "uses pnpm.", Preference, Global) // the same again: an update
	b := infer(t, "Deploys from CI", Global)
	c := infer(t, "Answers in Chinese", Global)
	if _, _, err := Save(Input{Text: str("Ships on Fridays")}, FromUser, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Edit(a.ID, Input{Text: str("Uses pnpm, never npm")}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := SetStatus(Active, b.ID); err != nil { // kept: no update
		t.Fatal(err)
	}
	if _, err := Forget(b.ID); err != nil {
		t.Fatal(err)
	}
	if err := Remove(b.ID); err != nil { // already set aside
		t.Fatal(err)
	}
	if err := Remove(c.ID); err != nil {
		t.Fatal(err)
	}
	Search(Query{Text: "pnpm"})
	Search(Query{Text: "kubernetes"})
	Search(Query{}) // a listing, not a recall
	Rank(Query{Text: "pnpm"})

	d := lastDay(t)
	if !d.Recorded || d.Created[FromAgent] != 1 || d.Created[FromInferred] != 2 || d.Created[FromUser] != 1 || len(d.Created) != 3 {
		t.Fatalf("created: %+v", d)
	}
	if d.Updated != 2 {
		t.Fatalf("updated %d, want 2 (the same text again, the edit)", d.Updated)
	}
	if d.Cleaned[ByAgent] != 1 || d.Cleaned[ByYou] != 1 || len(d.Cleaned) != 2 {
		t.Fatalf("cleaned: %v", d.Cleaned)
	}
	if d.Searches != 2 || d.Found != 1 || d.Recalled != 1 {
		t.Fatalf("searches %d found %d recalled %d", d.Searches, d.Found, d.Recalled)
	}

	// the next day's counts are that day's
	*at = at.Add(24 * time.Hour)
	if d := lastDay(t); !d.Day.empty() {
		t.Fatalf("a new day starts at none: %+v", d)
	}
}

// A tidy's merge is one update and its members leave use, by the tidy; the
// memory it makes isn't a new one. Its set-asides and retention's are
// counted by who did them.
func TestStatsCountTidyAndExpiry(t *testing.T) {
	at := fresh(t)
	a := infer(t, "Ships on Tuesdays", Global)
	b := infer(t, "Releases go out every Tuesday", Global)
	s := infer(t, "Uses Node 18", Global)
	old := infer(t, "Old candidate", Global)
	if _, err := Tidy(Plan{Merges: []Merge{{IDs: []string{a.ID, b.ID}, Text: "Releases ship on Tuesdays"}}, Stale: []Stale{{ID: s.ID, Why: "Node 22 now"}}}); err != nil {
		t.Fatal(err)
	}
	*at = at.Add(Keep + time.Hour)
	if p, err := Prune(); err != nil || p.Archived != 2 { // old and the merged candidate
		t.Fatalf("%+v %v", p, err)
	}
	_ = old
	r, err := Stats(30)
	if err != nil {
		t.Fatal(err)
	}
	upd := 0
	for _, d := range r.Days {
		upd += d.Updated
	}
	if upd != 0 { // the tidy was 31 days back, out of the window
		t.Fatalf("updated in the window: %d", upd)
	}
	if last := r.Days[len(r.Days)-1]; last.Cleaned[ByExpiry] != 2 {
		t.Fatalf("expiry: %v", last.Cleaned)
	}
	r, err = Stats(keepDays)
	if err != nil {
		t.Fatal(err)
	}
	made, tidy := 0, 0
	upd = 0
	for _, d := range r.Days {
		made += d.Created[FromInferred]
		upd += d.Updated
		tidy += d.Cleaned[ByTidy]
	}
	if made != 4 || upd != 1 || tidy != 3 {
		t.Fatalf("made %d updated %d tidied %d, want 4, 1 and 3", made, upd, tidy)
	}
}

// The days before counting began take what was made from the memory
// files' dates, and nothing else; a merged memory isn't made twice. The
// counts hold no memory's text.
func TestStatsBackfillAndNoText(t *testing.T) {
	at := fresh(t)
	a := infer(t, "Ships on Tuesdays", Global)
	b := infer(t, "Releases go out every Tuesday", Global)
	if _, err := Tidy(Plan{Merges: []Merge{{IDs: []string{a.ID, b.ID}, Text: "Releases ship on Tuesdays"}}}); err != nil {
		t.Fatal(err)
	}
	Search(Query{Text: "Tuesdays"})
	raw, err := os.ReadFile(statsPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "Tuesday") {
		t.Fatalf("a memory's text in the counts: %s", raw)
	}
	// as a magpie from before counting began: no counts at all
	if err := os.Remove(statsPath()); err != nil {
		t.Fatal(err)
	}
	*at = at.Add(48 * time.Hour)
	r, err := Stats(7)
	if err != nil {
		t.Fatal(err)
	}
	if r.Since != "" {
		t.Fatalf("since %q", r.Since)
	}
	d := r.Days[4] // two days back
	if !d.Backfilled || d.Recorded || d.Created[FromInferred] != 2 || d.Updated != 0 || d.Searches != 0 {
		t.Fatalf("backfilled day: %+v", d)
	}
	if r.Days[6].Backfilled || r.Days[6].Recorded {
		t.Fatalf("today, with nothing made: %+v", r.Days[6])
	}
}

// A counts file that can't be read is never written over, and stops
// nothing memory does.
func TestStatsUnreadableIsLeftAlone(t *testing.T) {
	fresh(t)
	save(t, "Uses pnpm", Preference, Global)
	if err := os.WriteFile(statsPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	save(t, "Ships on Fridays", Fact, Global)
	if b, _ := os.ReadFile(statsPath()); string(b) != "{not json" {
		t.Fatalf("written over: %s", b)
	}
	if _, err := Stats(7); err == nil {
		t.Fatal("an unreadable file read as no counts")
	}
}
