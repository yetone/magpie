package provider

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
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

// A provider saved while a request holds the catalog is in it at once.
func TestHoldSeesWrites(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(dir, "security"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
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
