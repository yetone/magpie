package memory

import (
	"slices"
	"testing"
	"time"
)

func uses(t *testing.T, id string, n int) {
	t.Helper()
	if err := change(func(f *file) error { find(f, id).Uses = n; return nil }); err != nil {
		t.Fatal(err)
	}
}

func suggested(t *testing.T) []Suggestion {
	t.Helper()
	s, err := Suggest()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A memory learned in two projects is suggested to hold everywhere: the
// same text, or the same words in another order; one project's memories
// alone, or two short ones sharing words, are not.
func TestSuggestGlobalAcrossProjects(t *testing.T) {
	fresh(t)
	a := save(t, "Commits list explicit filenames, never git add -A.", Rule, "project:a")
	b := save(t, "commits list explicit filenames never git add -A", Rule, "project:b")
	c := save(t, "Never git add -A: commits list explicit filenames.", Rule, "project:c")
	save(t, "Builds run on mote.", Rule, "project:a")
	save(t, "Builds run on CI.", Rule, "project:b") // short, alike in words: another fact
	save(t, "Tests run with -race.", Rule, "project:a")
	save(t, "Tests run with -race.", Rule, "agent:codex")
	got := suggested(t)
	if len(got) != 1 || got[0].To != ToGlobal || !slices.Equal(got[0].Projects, []string{"a", "b", "c"}) {
		t.Fatalf("suggested %+v, want the one rule of three projects", got)
	}
	ids := slices.Sorted(slices.Values(got[0].IDs))
	if want := slices.Sorted(slices.Values([]string{a.ID, b.ID, c.ID})); !slices.Equal(ids, want) {
		t.Fatalf("ids %v, want %v", ids, want)
	}
}

// One project's memory whose sources were in another project's folder
// was learned in both.
func TestSuggestGlobalBySourceFolder(t *testing.T) {
	fresh(t)
	NoteProject(&Project{Key: "a", Name: "a", Roots: []string{"/src/a"}})
	NoteProject(&Project{Key: "b", Name: "b", Roots: []string{"/src/b"}})
	k, s := Preference, "project:a"
	text := "Replies are short."
	Save(Input{Text: &text, Kind: &k, Scope: &s}, FromAgent, &Source{Agent: "codex", Dir: "/src/a"}, nil)
	if got := suggested(t); len(got) != 0 {
		t.Fatalf("one project's: %+v", got)
	}
	Save(Input{Text: &text, Kind: &k, Scope: &s}, FromAgent, &Source{Agent: "codex", Dir: "/src/b/web"}, nil)
	if got := suggested(t); len(got) != 1 || !slices.Equal(got[0].Projects, []string{"a", "b"}) {
		t.Fatalf("suggested %+v, want it from a and b", got)
	}
}

// Taken, the one kept holds everywhere, carrying the others' sources and
// uses, and they are superseded by it; the user's own is the one kept,
// as written. It is the user's change, and isn't suggested again.
func TestPromoteKeepsTheUsersOwn(t *testing.T) {
	at := fresh(t)
	a := save(t, "Use pnpm, not npm.", Preference, "project:a")
	uses(t, a.ID, 4)
	text, k, s := "use pnpm, not npm", Preference, "project:b"
	b, _, _ := Save(Input{Text: &text, Kind: &k, Scope: &s}, FromUser, nil, nil)
	*at = at.Add(time.Hour)
	m, err := Promote([]string{a.ID, b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != b.ID || m.Scope != Global || m.Text != "use pnpm, not npm" || m.Uses != 4 || !byUser(m) {
		t.Fatalf("kept %+v, want the user's own, global", m)
	}
	o, _ := Get(a.ID)
	if o.Status != Superseded || o.SupersededBy != b.ID {
		t.Fatalf("the other: %+v", o)
	}
	if got := suggested(t); len(got) != 0 {
		t.Fatalf("suggested again: %+v", got)
	}
	// a memory no longer in a project is no promotion
	if _, err := Promote([]string{a.ID}); err != ErrNotSuggested {
		t.Fatalf("promoted a superseded one: %v", err)
	}
}

// The same text global already: the project ones go into it.
func TestPromoteIntoTheGlobalOne(t *testing.T) {
	fresh(t)
	g := save(t, "Dates are ISO 8601.", Rule, Global)
	a := save(t, "Dates are ISO 8601.", Rule, "project:a")
	b := save(t, "Dates are ISO 8601.", Rule, "project:b")
	m, err := Promote([]string{a.ID, b.ID})
	if err != nil || m.ID != g.ID || m.Scope != Global {
		t.Fatalf("kept %+v, %v; want the global one", m, err)
	}
	for _, id := range []string{a.ID, b.ID} {
		if o, _ := Get(id); o.Status != Superseded || o.SupersededBy != g.ID || o.Scope == Global {
			t.Fatalf("%+v", o)
		}
	}
}

// A procedure searched for often is suggested as a skill until it is
// one, and a suggestion turned down stays down, on another computer too.
func TestSuggestSkillAndDecline(t *testing.T) {
	a, b, _ := twoComputers(t)
	a.use(t)
	p := save(t, "To release: tag, wait for the assets, then post.", Procedure, Global)
	r := save(t, "Never force-push main.", Rule, Global)
	uses(t, p.ID, skillUses-1)
	uses(t, r.ID, 50)
	if got := suggested(t); len(got) != 0 {
		t.Fatalf("suggested %+v before it was used enough", got)
	}
	uses(t, p.ID, skillUses)
	got := suggested(t)
	if len(got) != 1 || got[0].To != ToSkill || got[0].IDs[0] != p.ID || got[0].Uses != skillUses {
		t.Fatalf("suggested %+v, want the procedure", got)
	}
	if err := Decline(got[0].Key); err != nil {
		t.Fatal(err)
	}
	if got := suggested(t); len(got) != 0 {
		t.Fatalf("declined, still suggested: %+v", got)
	}
	carry(t, a, b)
	b.use(t)
	if got := suggested(t); len(got) != 0 {
		t.Fatalf("declined on a, suggested on b: %+v", got)
	}
	// made a skill: archived, noted, not suggested
	a.use(t)
	q := save(t, "To deploy the site: wrangler deploy, then curl it.", Procedure, Global)
	uses(t, q.ID, skillUses)
	if err := MadeSkill(q.ID, "deploy-site"); err != nil {
		t.Fatal(err)
	}
	if m, _ := Get(q.ID); m.Status != Archived || m.Skill != "deploy-site" {
		t.Fatalf("%+v", m)
	}
	if got := suggested(t); len(got) != 0 {
		t.Fatalf("a skill already, suggested: %+v", got)
	}
}
