package provider

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// #957 (Evan26Ma): two Plus accounts started at one daily time run out
// together; each can have its own, so one takes over as the other's
// window ends. An account with none follows the time for all, and one set
// off is never started at a time of day.
func TestWarmAtEachAccountsOwn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex-warmup.json")
	f := &fakeWarm{now: at8(28, 6, 0), errs: map[string]error{}}
	idleAll := func() {
		for _, u := range []string{"a@example.com", "b@example.com", "c@example.com"} {
			f.ws[u] = []QuotaWindow{win("5 hours", fiveHours, 0, f.now.Add(fiveHours))}
		}
	}
	f.ws = map[string][]QuotaWindow{}
	idleAll()
	own := map[string]string{"a@example.com": "09:00", "c@example.com": ""}
	run := func(at string) []string {
		t.Helper()
		f.sent = nil
		w := f.warmer(path)
		w.ownAt = func() map[string]string { return own }
		w.warmNow(context.Background(), "", times(at))
		return f.sent
	}
	// six: b, which follows the time for all; a waits for its nine, c for none
	if got := strings.Join(run("06:00"), ","); got != "b@example.com" {
		t.Fatalf("at six sent %q", got)
	}
	// nine: a, as its own name is kept in any case
	f.now = at8(28, 9, 0)
	idleAll()
	own = map[string]string{"a@example.com": "09:00", "c@example.com": ""}
	f.ws["A@Example.com"] = f.ws["a@example.com"]
	delete(f.ws, "a@example.com")
	if got := strings.Join(run("06:00"), ","); got != "A@Example.com" {
		t.Fatalf("at nine sent %q", got)
	}
	// the time for all off: an account's own still starts it, the rest none
	f.now = at8(29, 9, 0)
	idleAll()
	delete(f.ws, "a@example.com")
	if got := strings.Join(run(""), ","); got != "A@Example.com" {
		t.Fatalf("the next day, the time for all off, sent %q", got)
	}
}

// keepWarm looks at the windows when an account's own time comes, even
// with nothing else on, and never with nothing on at all.
func TestWarmLookAtAnAccountsOwnTime(t *testing.T) {
	last, now := at8(28, 8, 59), at8(28, 9, 0)
	if !warmLook("", times(""), map[string]string{"a@x": "09:00"}, false, last, now) {
		t.Error("an account's own nine not looked at")
	}
	if warmLook("", times(""), map[string]string{"a@x": "10:00"}, false, last, now) {
		t.Error("looked at before its time")
	}
	if warmLook("", times(""), map[string]string{"a@x": ""}, true, time.Time{}, now) {
		t.Error("looked at with nothing on")
	}
	if !warmLook("", times("06:00"), nil, false, time.Time{}, now) || !warmLook("week", times(""), nil, false, now.Add(-codexWarmEvery), now) {
		t.Error("the time for all, or the warm-up on reset, not looked at")
	}
}

// An account's own time is kept by its name in lower case, as 06:00
// whichever way it came; "" follows the time for all again; one that isn't
// a time is refused.
func TestSetCodexWarmAt(t *testing.T) {
	azureHome(t)
	if err := SetCodexWarmAt("A@Example.com ", "9:00"); err != nil {
		t.Fatal(err)
	}
	if err := SetCodexWarmAt("b@example.com", "OFF"); err != nil {
		t.Fatal(err)
	}
	if at, ok := CodexWarmAtOf("a@example.com"); !ok || at != "09:00" {
		t.Fatalf("a: %q %v", at, ok)
	}
	got := codexWarmAtOf()
	if len(got) != 2 || got["a@example.com"] != "09:00" || got["b@example.com"] != "" {
		t.Fatalf("own times %v", got)
	}
	if err := SetCodexWarmAt("a@example.com", "nine"); err == nil {
		t.Fatal("nine taken")
	}
	if err := SetCodexWarmAt("a@example.com", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := CodexWarmAtOf("a@example.com"); ok {
		t.Fatal("still its own")
	}
	if err := SetCodexWarmAt("b@example.com", ""); err != nil {
		t.Fatal(err)
	}
	if s := settings.Load(); s.CodexWarmAtOf != nil {
		t.Fatalf("kept %v", s.CodexWarmAtOf)
	}
	if err := SetCodexWarmAt(" ", "06:00"); err == nil {
		t.Fatal("no account taken")
	}
}
