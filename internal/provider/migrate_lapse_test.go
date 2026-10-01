package provider

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// An account the vendor refused while it ran on the plugin goes back to
// the built-in marked, though back writes its sign-in as one just made
// (every real mover clears the mark there): the built-in had marked it
// the same way, and it would show signed in until its next refusal.
func TestMoveBackKeepsLapse(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	inUse := []string{"fake-1"}
	fakeMover(t, &inUse)
	mv := movers["fakeco"]
	mv.pkg = abs
	back := mv.back
	mv.back = func(ls []savedLogin, user string, auth map[string]any) ([]savedLogin, string, error) {
		ls, u, err := back(ls, user, auth)
		for i := range ls {
			if ls[i].Agent == "fakeco" && ls[i].User == u {
				ls[i].Lapsed = "" // as zed's, factory's and mimo's back do
			}
		}
		return ls, u, err
	}
	loginsMu.Lock()
	if err := writeLogins([]savedLogin{fakeLogin("a@fake", "r-a", true, true), fakeLogin("b@fake", "r-b", false, true)}); err != nil {
		t.Fatal(err)
	}
	loginsMu.Unlock()
	if err := Move(ctx, "fakeco"); err != nil {
		t.Fatal(err)
	}
	m, _ := MigrationOf("fakeco")
	for _, ma := range m.Accounts {
		if ma.User == "b@fake" {
			notePluginLapse(mustPlugin(t), ma.Key, http.StatusUnauthorized)
		}
	}
	if err := MoveBack(ctx, "fakeco"); err != nil {
		t.Fatal(err)
	}
	s := fakeSaved(t)
	if s["b@fake"].Lapsed == "" || s["a@fake"].Lapsed != "" {
		t.Fatalf("moved back: a lapsed %q, b lapsed %q", s["a@fake"].Lapsed, s["b@fake"].Lapsed)
	}
}

// An account whose sign-in the vendor refuses while the move reads the
// plugin's models (a models hook throwing signIn: expired, as Zed's does
// on a refused model token) moves along marked, as the built-in would
// have marked it on its own model read, and doesn't stop the move.
func TestMoveRefusedWhileListing(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	inUse := []string{"fake-1"}
	fakeMover(t, &inUse)
	movers["fakeco"].pkg = abs
	loginsMu.Lock()
	if err := writeLogins([]savedLogin{fakeLogin("a@fake", "r-a", true, true), fakeLogin("g@fake", "r-models-gone", false, true)}); err != nil {
		t.Fatal(err)
	}
	loginsMu.Unlock()
	if err := Move(ctx, "fakeco"); err != nil {
		t.Fatalf("a sign-in refused while listing stopped the move: %v", err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		lapsed := map[string]bool{}
		for _, l := range pluginLogins(mustPlugin(t)) {
			lapsed[l.User] = l.Lapsed != ""
		}
		if lapsed["g@fake"] && !lapsed["a@fake"] {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("lapsed after the move: %v, want g@fake alone", lapsed)
		}
	}
}

// An account whose plugin says only in words that its sign-in has expired
// (Grok's, past its time with no CLI to renew it) moves along untried and
// unmarked, as a built-in Grok account never was marked; it used to stop
// the whole move. A plugin failing an account for any other reason still
// does (TestMove's r-dead).
func TestMoveExpiredInWords(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	inUse := []string{"fake-1"}
	fakeMover(t, &inUse)
	movers["fakeco"].pkg = abs
	loginsMu.Lock()
	if err := writeLogins([]savedLogin{fakeLogin("a@fake", "r-a", true, true), fakeLogin("x@fake", "r-models-expired", false, true)}); err != nil {
		t.Fatal(err)
	}
	loginsMu.Unlock()
	if err := Move(ctx, "fakeco"); err != nil {
		t.Fatalf("an expired sign-in stopped the move: %v", err)
	}
	users := map[string]bool{}
	for _, l := range pluginLogins(mustPlugin(t)) {
		users[l.User] = true
		if l.Lapsed != "" {
			t.Fatalf("%s marked lapsed: %q", l.User, l.Lapsed)
		}
	}
	if !users["a@fake"] || !users["x@fake"] {
		t.Fatalf("moved %v", users)
	}
}

// A move's lock left by a magpie killed mid-move (a hung move stopped by
// hand) holds nothing: the next move takes it, where it used to be
// refused as "another magpie is moving" for a quarter of an hour.
func TestMoveLockOfDeadMagpie(t *testing.T) {
	claudeHome(t)
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Skip(err)
	}
	p := migrationsPath() + ".lock"
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strconv.Itoa(gone.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockMoves()
	if err != nil {
		t.Fatalf("a dead magpie's lock: %v", err)
	}
	// one held by a magpie still running stays held
	if _, err := lockMoves(); err == nil {
		t.Fatal("a live lock was taken")
	}
	unlock()
}
