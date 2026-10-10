package provider

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

// A request that only reads builds the catalog once for all its look-ups
// (/api/state took 4s, resolving each agent's models anew); outside one,
// and after a write, it is built again.
func TestHoldBuildsOnce(t *testing.T) {
	n := 0
	build := func() int { n++; return n }
	heldOf("t", build)
	heldOf("t", build)
	if n != 2 {
		t.Fatalf("not held: built %d times, want 2", n)
	}
	release := Hold()
	heldOf("t", build)
	heldOf("t", build)
	if n != 3 {
		t.Fatalf("held: built %d times, want 3", n)
	}
	Changed()
	if heldOf("t", build); n != 4 {
		t.Fatalf("after a write: built %d times, want 4", n)
	}
	release()
	release() // a second release is no second one
	if heldOf("t", build); n != 5 {
		t.Fatalf("released: built %d times, want 5", n)
	}
	if held.holds != 0 {
		t.Fatalf("holds left: %d", held.holds)
	}
}

// The settings are read once while a request holds the catalog (a look at
// the agents read settings.json for every model of every agent), and a
// setting saved meanwhile is seen at once.
func TestHoldReadsSettingsOnce(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // a run starts from no files, not the last run's
	defer Hold()()
	if _, ok := VisibleTo("held-agent"); ok {
		t.Fatal("a visibility before any was set")
	}
	// written by something else: the held read stands
	path := settings.Path()
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"visible":{"held-agent":["a"]}}`), 0o600)
	if _, ok := VisibleTo("held-agent"); ok {
		t.Fatal("settings.json read again while held")
	}
	s := settings.Load()
	s.Visible = map[string][]string{"held-agent": {"b"}}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if names, ok := VisibleTo("held-agent"); !ok || len(names) != 1 || names[0] != "b" {
		t.Fatalf("a setting saved while held: %v %v", names, ok)
	}
}

// A provider saved while a request holds the catalog is in it at once.
func TestHoldSeesWrites(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // a run starts from no files, not the last run's
	if err := Save(Provider{ID: "held", Name: "Held", Chat: "https://held.example/v1", Key: "k", Models: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	defer Hold()()
	ids := func() []string {
		var out []string
		for _, e := range Catalog() {
			out = append(out, e.ID)
		}
		return out
	}
	if !slices.Contains(ids(), "held/a") || slices.Contains(ids(), "held/b") {
		t.Fatalf("before: %v", ids())
	}
	if err := Save(Provider{ID: "held", Name: "Held", Chat: "https://held.example/v1", Key: "k", Models: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ids(), "held/b") {
		t.Fatalf("a model picked meanwhile is missing: %v", ids())
	}
	if p, m, ok := Resolve("held/b"); !ok || p.ID != "held" || m != "b" {
		t.Fatalf("Resolve: %v %q %v", p.ID, m, ok)
	}
}

// The providers are built once while a request holds them (#746, sperwe:
// Codex's /models built them seven times, each reading every agent's
// sign-in, and ran past the 5 s Codex waits); a provider saved or an
// account written meanwhile has them built again, and a caller changing
// its list changes no one else's.
func TestHoldBuildsProvidersOnce(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // a run starts from no files, not the last run's
	if err := Save(Provider{ID: "once", Name: "Once", Chat: "https://once.example/v1", Key: "k", Models: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	defer Hold()()
	n := AllBuilt.Load()
	ps := All()
	All()
	if got := AllBuilt.Load() - n; got != 1 {
		t.Fatalf("held: built %d times, want 1", got)
	}
	i := slices.IndexFunc(ps, func(p Provider) bool { return p.ID == "once" })
	if i < 0 {
		t.Fatalf("saved provider missing: %v", ps)
	}
	ps[i].Name = "changed"
	if p, _ := findIn(All(), "once"); p.Name != "Once" {
		t.Fatalf("a caller's change leaked: %q", p.Name)
	}
	if err := Save(Provider{ID: "twice", Name: "Twice", Chat: "https://twice.example/v1", Key: "k", Models: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := findIn(All(), "twice"); err != nil {
		t.Fatal("a provider saved while held is missing")
	}
	n = AllBuilt.Load()
	if err := writeLogins(readLogins()); err != nil {
		t.Fatal(err)
	}
	if All(); AllBuilt.Load() == n {
		t.Fatal("accounts written while held: not built again")
	}
}

// cursor-agent's token is read from the Keychain once a while, not on
// each look at the accounts (#746: some 200 `security` runs a request); a
// sign-in or out in magpie, and CursorToken renewing it, read it afresh.
func TestCursorKeychainReadOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for security")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "runs")
	script := "#!/bin/sh\necho run >> " + log + "\necho tok-$(wc -l < " + log + " | tr -d ' ')\n"
	testenv.Program(t, filepath.Join(dir, "security"), script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	forgetCursorStatus()
	t.Cleanup(forgetCursorStatus)
	runs := func() int {
		b, _ := os.ReadFile(log)
		return strings.Count(string(b), "run")
	}
	for range 3 {
		if tok := cursorKeychainToken(false); tok != "tok-1" {
			t.Fatalf("token %q", tok)
		}
	}
	if runs() != 1 {
		t.Fatalf("security ran %d times, want 1", runs())
	}
	forgetCursorStatus() // signed in or out in magpie
	if tok := cursorKeychainToken(false); tok != "tok-2" || runs() != 2 {
		t.Fatalf("after a sign-in: %q, %d runs", tok, runs())
	}
	if tok := cursorKeychainToken(true); tok != "tok-3" {
		t.Fatalf("fresh: %q", tok)
	}
	if tok := cursorKeychainToken(false); tok != "tok-3" || runs() != 3 {
		t.Fatalf("after a fresh read: %q, %d runs", tok, runs())
	}
}

// migrations.json is read once while a request holds the catalog (the
// GUI's state looked at it thousands of times, one per provider's API),
// and a move written meanwhile, even one read from the file as it was
// before, is seen at once.
func TestHoldReadsMigrationsOnce(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // a run starts from no files, not the last run's
	defer Hold()()
	if Moved("held-sub") {
		t.Fatal("moved before any move")
	}
	os.MkdirAll(filepath.Dir(migrationsPath()), 0o755)
	os.WriteFile(migrationsPath(), []byte(`{"other-sub":{"state":"plugin"}}`), 0o600)
	if Moved("other-sub") {
		t.Fatal("migrations.json read again while held")
	}
	if err := setMigration("held-sub", func(m *Migration) { m.State = MovePlugin }); err != nil {
		t.Fatal(err)
	}
	if !Moved("held-sub") || !Moved("other-sub") {
		t.Fatalf("a move written while held: %v %v", Moved("held-sub"), Moved("other-sub"))
	}
}

// A group read while a request holds the catalog is the caller's own: the
// TUI changes its group in place before saving it, and a save that fails
// left that change in every look-up's providers.json for the rest of the
// hold (SyncCatalog and the gateway's routes read it meanwhile); a group
// handed to SaveGroup isn't cleaned in the caller's lists either.
func TestHeldGroupsAreTheCallers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, id := range []string{"a", "b"} {
		if err := Save(Provider{ID: id, Name: id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m", "big"}}); err != nil {
			t.Fatal(err)
		}
	}
	saved := Group{ID: "pair", Name: "Pair", Members: []string{"a/m", "b/big"}, Off: []string{"b/big"}, Levels: []string{"low", "high"},
		Rules: []Rule{{Use: "a/m", Tokens: 5}, {Use: "b/big", Agents: []string{"codex"}, Time: &TimeWindow{From: "09:00", To: "18:00", Days: []string{"mon"}}}}}
	if err := SaveGroup(saved); err != nil {
		t.Fatal(err)
	}
	defer Hold()()
	pair := func() Group {
		t.Helper()
		for _, g := range Groups() {
			if g.ID == "pair" {
				return g
			}
		}
		t.Fatal("no group pair")
		return Group{}
	}
	same := func(why string) {
		t.Helper()
		g := pair()
		if !slices.Equal(g.Members, saved.Members) || !slices.Equal(g.Off, saved.Off) || !slices.Equal(g.Levels, saved.Levels) ||
			len(g.Rules) != 2 || g.Rules[0].Use != "a/m" || g.Rules[1].Use != "b/big" ||
			!slices.Equal(g.Rules[1].Agents, []string{"codex"}) || g.Rules[1].Time == nil || !slices.Equal(g.Rules[1].Time.Days, []string{"mon"}) {
			t.Fatalf("%s: %+v", why, g)
		}
	}
	// taking a/m out, as the TUI's d does: b/big alone is off, so it fails
	g := pair()
	g.Members = slices.DeleteFunc(g.Members, func(m string) bool { return m == "a/m" })
	g.Rules = slices.DeleteFunc(g.Rules, func(r Rule) bool { return r.Use == "a/m" })
	if err := SaveGroup(g); err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Fatalf("saved: %v", err)
	}
	same("a failed save's change is in the held groups")
	g = pair()
	g.Levels[0], g.Rules[1].Agents[0], g.Rules[1].Time.Days[0] = "max", "x", "sun"
	same("a caller's change is in the held groups")

	// the caller's group, cleaned for saving: as it gave it, saved or not
	in := Group{Name: "R", Members: []string{"a/m", "b/big"}, Rules: []Rule{{Use: " b/big ", Intent: "tests"}}}
	if err := SaveGroup(in); err == nil || !strings.Contains(err.Error(), "classifier") {
		t.Fatalf("saved: %v", err)
	}
	if in.Rules[0].Use != " b/big " {
		t.Fatalf("SaveGroup changed the caller's rule: %q", in.Rules[0].Use)
	}
}
