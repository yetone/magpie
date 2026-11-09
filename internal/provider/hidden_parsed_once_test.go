package provider

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// Hidden reads providers.json as parsed once until it changes, not a parse
// for each signed-in account: the Providers page asks it on every load. A
// save since is seen at once.
func TestHiddenParsedOnceUntilTheFileChanges(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(24*time.Hour))
	var models []string
	for i := range 100 {
		models = append(models, fmt.Sprintf("m-%d", i))
	}
	save := func(hidden bool) {
		saved := []map[string]any{{"id": "claude", "hidden": hidden}}
		for i := range 30 {
			id := fmt.Sprintf("r%d", i)
			saved = append(saved, map[string]any{"id": id, "name": id, "key": "k", "chat": "http://127.0.0.1:1/v1", "models": models})
		}
		writeFile(t, Path(), map[string]any{"providers": saved})
	}
	save(true)
	// written long enough ago that its time tells a later write apart
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(Path(), old, old); err != nil {
		t.Fatal(err)
	}
	if h := Hidden(); len(h) != 1 || h[0].ID != "claude" {
		t.Fatalf("Hidden = %+v", h)
	}
	parsed := testing.AllocsPerRun(5, func() { load() })
	accounts := testing.AllocsPerRun(10, func() { Accounts() })
	hidden := testing.AllocsPerRun(10, func() { Hidden() })
	if hidden-accounts > parsed/4 {
		t.Errorf("Hidden with the file unchanged: %.0f allocations over its accounts' %.0f, a parse %.0f", hidden-accounts, accounts, parsed)
	}

	save(false)
	if h := Hidden(); len(h) != 0 {
		t.Fatalf("an account shown again since is still hidden: %+v", h)
	}
}

// With no account removed, as for most users, Hidden makes no account:
// the Providers page asks it twice a poll, and reading every sign-in
// for an empty answer was ~0.6 MB of each poll's garbage.
func TestHiddenNoneMakesNoAccount(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(24*time.Hour))
	writeFile(t, Path(), map[string]any{"providers": []map[string]any{{"id": "claude"}}})
	Hidden()
	accounts := testing.AllocsPerRun(10, func() { Accounts() })
	if accounts == 0 {
		t.Fatal("Accounts made nothing, so this test tells nothing")
	}
	if h := testing.AllocsPerRun(10, func() { Hidden() }); h >= accounts {
		t.Errorf("Hidden with nothing removed made %.0f allocations, its accounts %.0f", h, accounts)
	}
	writeFile(t, Path(), map[string]any{"providers": []map[string]any{{"id": "claude", "hidden": true}}})
	if h := Hidden(); len(h) != 1 || h[0].ID != "claude" {
		t.Fatalf("a removed account: Hidden = %+v", h)
	}
}
