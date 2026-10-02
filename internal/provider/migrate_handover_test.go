package provider

import (
	"os"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
)

// A deprecated built-in whose plugin the user installed is handed to it
// (ARNO on Discord: Qoder CN listed twice, the built-in and the plugin's,
// once the Qoder plugin served it): one with no accounts at once, one with
// accounts only when asked to move them, through Move. One the plugin is
// signed in to under its own id already stays as it is, as does one the
// user moved back, and nothing happens while the plugin isn't installed.
func TestHandOver(t *testing.T) {
	inUse := []string{"fake-1"}
	ctx, abs, installs := fakePlugin(t, &inUse)
	reset := func(ls ...savedLogin) {
		t.Helper()
		_ = plugin.SignOut(ctx, "fakeco", "")
		os.Remove(migrationsPath())
		setFakeLogins(t, ls...)
	}

	// not installed: nothing, and nothing installed for it
	if out := HandOver(ctx, true); len(out) != 0 || Moved("fakeco") || *installs != 0 {
		t.Fatalf("with no plugin: %v, moved %v, %d installs", out, Moved("fakeco"), *installs)
	}
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}

	// installed, no accounts either side: the plugin's at once
	reset()
	if out := HandOver(ctx, false); out["fakeco"] != nil || !Moved("fakeco") {
		t.Fatalf("no accounts: %v, moved %v", out, Moved("fakeco"))
	}
	// moved back by the user: it stays built-in
	if err := MoveBack(ctx, "fakeco"); err != nil {
		t.Fatal(err)
	}
	if out := HandOver(ctx, true); len(out) != 0 || Moved("fakeco") {
		t.Fatalf("moved back: %v, moved %v", out, Moved("fakeco"))
	}
	if !pluginListed(abs) {
		t.Fatal("moving back took away the plugin the user installed")
	}

	// the built-in has an account: moved only when accounts are asked for
	reset(fakeLogin("a@fake", "r-a", true, true))
	if out := HandOver(ctx, false); len(out) != 0 || Moved("fakeco") {
		t.Fatalf("accounts not asked for: %v, moved %v", out, Moved("fakeco"))
	}
	if out := HandOver(ctx, true); out["fakeco"] != nil || !Moved("fakeco") || len(fakeSaved(t)) != 0 || !plugin.SignedIn("fakeco") {
		t.Fatalf("accounts asked for: %v, moved %v, built-in's %+v", out, Moved("fakeco"), fakeSaved(t))
	}

	// the plugin signed in on its own, the built-in not moved: left as it
	// is, its models in use under the plugin's own id
	if err := MoveBack(ctx, "fakeco"); err != nil {
		t.Fatal(err)
	}
	reset()
	if _, err := plugin.Import(ctx, "fakeco", map[string]any{"type": "oauth", "refresh": "r-b", "access": "a", "expires": 9e15, "accountId": "b@fake"}); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if out := HandOver(ctx, true); len(out) != 0 || Moved("fakeco") {
		t.Fatalf("the plugin signed in on its own: %v, moved %v", out, Moved("fakeco"))
	}
}
