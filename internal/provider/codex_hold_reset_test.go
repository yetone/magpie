package provider

import (
	"context"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// keepCodexReading puts q in the cache as the reading of the account Codex
// is signed in to (signIn's me@example.com), made at at, as a look hours
// ago left it.
func keepCodexReading(t *testing.T, q SubscriptionQuota, at time.Time) {
	t.Helper()
	loginUsageCache.Lock()
	loginUsageCache.m = map[string]loginUsageEntry{"codex/me@example.com": {at: at, q: q, read: q}}
	loginUsageCache.pending = nil
	loginUsageCache.Unlock()
	t.Cleanup(func() {
		loginUsageCache.Lock()
		loginUsageCache.m, loginUsageCache.pending = nil, nil
		loginUsageCache.Unlock()
	})
}

// heldAt is a held reading whose 5h window was used up and starts again
// at reset.
func heldAt(reset time.Time) SubscriptionQuota {
	week := reset.Add(5 * 24 * time.Hour)
	return SubscriptionQuota{Provider: "codex", Held: true, Windows: []QuotaWindow{
		{Name: "5 hours", Used: 100, ResetsAt: &reset},
		{Name: "7 days", Used: 63, ResetsAt: &week},
	}}
}

// davezfr on X: Codex signed in to account 1 showed "magpie" for its
// account in the Codex app, account 2 its own. Account 1 had run out
// earlier, and the reading magpie kept of it in memory said held, with no
// end: the catalog sync (CodexUsedUpKnown) took the account as still out
// once Codex was on it again, past its window's reset, and made magpie
// Codex's provider for it (codex.out), where the app reads no account. A
// hold that came with a window used up ends when that window starts
// again, as the reading kept on disk already has it (lastQuotasT.reading).
func TestCodexHoldEndsAtItsWindowsReset(t *testing.T) {
	signIn(t)
	rememberLogins(true)
	now := time.Now()

	keepCodexReading(t, heldAt(now.Add(-time.Minute)), now.Add(-3*time.Hour))
	if CodexUsedUpKnown() {
		t.Fatal("CodexUsedUpKnown = true on a hold whose used-up window started again a minute ago")
	}
	if codexHeldKnown("me@example.com") {
		t.Fatal("codexHeldKnown = true on a hold whose used-up window started again")
	}
	// allowances read only when asked: the background look gets the kept
	// reading, as old as it is, and must not take it as held either
	setQuotaReads(t, "asked")
	t.Cleanup(func() { setQuotaReads(t, "") })
	if CodexUsedUp(context.Background()) {
		t.Fatal("CodexUsedUp = true, reads held back, on a hold whose window started again")
	}
	setQuotaReads(t, "")

	// before the reset it is held
	keepCodexReading(t, heldAt(now.Add(time.Hour)), now.Add(-3*time.Hour))
	if !CodexUsedUpKnown() {
		t.Fatal("CodexUsedUpKnown = false on a hold whose window starts again in an hour")
	}
	// a hold with no window used up has no reset to end it: it stands
	// till a fresh read (TestCodexHeldWithNoWindowUpKeptTillAFreshRead)
	q := heldAt(now.Add(-time.Minute))
	q.Windows[0].Used = 40
	keepCodexReading(t, q, now.Add(-3*time.Hour))
	if !CodexUsedUpKnown() {
		t.Fatal("CodexUsedUpKnown = false on a hold with no window used up")
	}
}

// The catalog sync goes by what is known (CodexUsedUpKnown). When the
// loop's fresh read changes that, the sync is told, though the loop's own
// last answer was the same: Codex was on another account with room at the
// last look, then signed in to one whose kept reading said held, and the
// sync made magpie its provider for it. The fresh read saying room left
// magpie its provider till something else ran the sync.
func TestCodexUsedUpToldWhenKnownChanges(t *testing.T) {
	told := 0
	was := catalog.Changed
	catalog.Changed = func() { told++ }
	t.Cleanup(func() { catalog.Changed = was; codexWasUsedUp.Store(false) })
	codexWasUsedUp.Store(false)
	// the sync knew held, the loop last said not, the read now says not
	noteCodexUsedUp(true, false)
	if told != 1 {
		t.Fatalf("told %d times, want 1: the sync took the account as out", told)
	}
	noteCodexUsedUp(false, false)
	if told != 1 {
		t.Fatalf("told %d times, want still 1", told)
	}
}
