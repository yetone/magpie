package gateway

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// wanggenjidev on X: an OpenCode Go key set to stop at 99% of its windows,
// as a Codex account is, so the next key takes over and this one is back
// when the window resets. A key whose windows magpie reads and that is at
// or past the usage cap set on it is left out of the plan (barred, with
// the cap that holds it); an unknown reading (not read yet, or the read
// failed) holds nothing, and a window that has renewed since it was read
// is empty again.
func TestKeyUsageCapPassesTheKeyOver(t *testing.T) {
	fresh(t)
	now := time.Now()
	five := func(used float64, resets time.Time) provider.Allowance {
		return provider.Allowance{{Name: "5 hours", Used: used, Resets: resets, Span: 5 * time.Hour}, {Name: "weekly", Used: 10, Resets: now.Add(3 * 24 * time.Hour), Span: 7 * 24 * time.Hour}}
	}
	of := map[string]provider.Allowance{
		"go-a": five(99, now.Add(2*time.Hour)),
		"go-b": five(50, now.Add(2*time.Hour)),
	}
	keyWindows(t, of)
	p := provider.Provider{ID: "go", Name: "OpenCode Go", Chat: "https://opencode.ai/zen/go/v1", Models: []string{"m1"}, Routing: provider.Ordered,
		Key: "go-a", KeyName: "A", Keys: []provider.KeyAccount{{Name: "B", Key: "go-b"}},
		AccountCaps: map[string]int{provider.KeyID("go-a"): 99}}
	rests := func() (on, barred string, held *capHold) {
		out, _, _, bar := perKeyEvery(p, "m1", provider.Chat)
		for _, c := range out {
			on += c.p.KeyName + " "
		}
		for _, c := range bar {
			barred += c.p.KeyName + " "
			held = c.capped
		}
		return
	}
	// A is at 99% of its five hours: B goes first, A is left out
	on, barred, held := rests()
	if on != "B " || barred != "A " || held == nil || held.cap != 99 || held.used != 99 || held.back.IsZero() {
		t.Fatalf("A at its cap: on %q, barred %q, held %+v", on, barred, held)
	}
	// below it, A is first again
	of["go-a"] = five(98, now.Add(2*time.Hour))
	if on, barred, _ = rests(); on != "A B " || barred != "" {
		t.Fatalf("A below its cap: on %q, barred %q", on, barred)
	}
	// the window renewed since it was read: empty again
	of["go-a"] = five(100, now.Add(-time.Minute))
	if on, barred, _ = rests(); on != "A B " || barred != "" {
		t.Fatalf("A's window renewed: on %q, barred %q", on, barred)
	}
	// unknown is not over the cap
	delete(of, "go-a")
	if on, barred, _ = rests(); on != "A B " || barred != "" {
		t.Fatalf("A's windows not known: on %q, barred %q", on, barred)
	}
	// a cap of one window's own: the week at 50%, the five hours with none
	of["go-a"] = provider.Allowance{{Name: "5 hours", Used: 98, Resets: now.Add(time.Hour), Span: 5 * time.Hour}, {Name: "weekly", Used: 60, Resets: now.Add(24 * time.Hour), Span: 7 * 24 * time.Hour}}
	p.AccountCaps = nil
	p.AccountWindowCaps = map[string]map[string]int{provider.KeyID("go-a"): {"weekly": 50}}
	if on, barred, held = rests(); on != "B " || barred != "A " || held == nil || held.cap != 50 {
		t.Fatalf("A's week past its own cap: on %q, barred %q, held %+v", on, barred, held)
	}
	// no cap set: used to 100%
	p.AccountWindowCaps = nil
	of["go-a"] = five(100, now.Add(time.Hour))
	if on, barred, _ = rests(); on != "A B " || barred != "" {
		t.Fatalf("A with no cap: on %q, barred %q", on, barred)
	}
}
