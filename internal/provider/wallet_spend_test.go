package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// wallet is a reading of a balance-only window (AStudio's 通用积分): what
// is left, nothing else.
func wallet(left float64, ws ...QuotaWindow) SubscriptionQuota {
	b := left
	return SubscriptionQuota{Provider: "codex", Windows: append([]QuotaWindow{{Name: "通用积分", Balance: &b}}, ws...)}
}

// A wallet's falls are counted as spent, its rises (a top-up) are not,
// and the count starts again with the next local day or the window's own
// reset (#5770).
func TestWalletSpendCounted(t *testing.T) {
	signIn(t)
	zone := time.FixedZone("UTC+8", 8*3600)
	t0 := time.Date(2026, 11, 9, 10, 0, 0, 0, zone)
	read := func(left float64, at time.Time) QuotaWindow {
		return withWalletSpend(wallet(left), "me@example.com", at).Windows[0]
	}
	for i, c := range []struct {
		left, spent float64
	}{{1000, 0}, {900, 100}, {950, 100}, {800, 250}, {800, 250}} {
		w := read(c.left, t0.Add(time.Duration(i)*time.Minute))
		if w.Spent == nil || *w.Spent != c.spent || w.SpentSince == nil || !w.SpentSince.Equal(t0) {
			t.Fatalf("reading %d (%v left): spent %v since %v, want %v since %v", i, c.left, w.Spent, w.SpentSince, c.spent, t0)
		}
		// no cap: the window reads as it did, its balance alone
		if w.Used != 0 || w.Amount != 0 || w.Limit != 0 {
			t.Fatalf("an uncapped wallet read as used: %+v", w)
		}
	}
	// a restart reads the count back
	walletSpends.Lock()
	walletSpends.path = ""
	walletSpends.Unlock()
	if w := read(700, t0.Add(time.Hour)); *w.Spent != 350 {
		t.Fatalf("after a restart: spent %v", *w.Spent)
	}
	// the next local day starts again from its first reading
	day := time.Date(2026, 11, 10, 0, 0, 1, 0, zone)
	if w := read(600, day); *w.Spent != 0 || !w.SpentSince.Equal(day) {
		t.Fatalf("next day: spent %v since %v", *w.Spent, w.SpentSince)
	}
	// a window telling its reset: its period, not the day
	reset := day.Add(2 * time.Hour)
	ws := func(left float64, at time.Time) QuotaWindow {
		q := wallet(0)
		b := left
		q.Windows = []QuotaWindow{{Name: "星火积分", Balance: &b, ResetsAt: &reset}}
		return withWalletSpend(q, "me@example.com", at).Windows[0]
	}
	ws(50, day)
	if w := ws(40, day.Add(time.Hour)); *w.Spent != 10 {
		t.Fatalf("before its reset: %v", *w.Spent)
	}
	if w := ws(45, reset.Add(time.Minute)); *w.Spent != 0 {
		t.Fatalf("after its reset: %v", *w.Spent)
	}
	// a window with a share of its own, a failed reading: left alone
	q := withWalletSpend(SubscriptionQuota{Provider: "codex", Windows: []QuotaWindow{{Name: "Weekly", Used: 30}}}, "me@example.com", t0)
	if q.Windows[0].Spent != nil {
		t.Fatal("a window with no balance counted")
	}
	q = wallet(10)
	q.Error = "sign in again"
	if q = withWalletSpend(q, "me@example.com", t0); q.Windows[0].Spent != nil {
		t.Fatal("a failed reading counted")
	}
}

// #5770: 9won capped an AStudio account at 4 and it spent over 1,000
// credits past it, since a wallet read 0% used until it was empty. A
// capped account's wallet reads as what it spent of what its period had,
// and the cap holds it.
func TestWalletCapHolds(t *testing.T) {
	signIn(t)
	now := time.Now()
	if err := SetAccountCap("codex", "me@example.com", 4); err != nil {
		t.Fatal(err)
	}
	caps := AccountCapsOf("codex", "me@example.com")
	read := func(left float64, at time.Time) QuotaWindow {
		return withWalletSpend(wallet(left), "me@example.com", at).Windows[0]
	}
	w := read(10000, now)
	if h := allowanceOf([]QuotaWindow{w}, now).CapHeld("any", caps, now); h != nil {
		t.Fatalf("held before spending: %+v", h)
	}
	w = read(9700, now.Add(time.Minute)) // 300 of 10,000: 3%
	if w.Amount != 300 || w.Limit != 10000 || w.Used != 3 {
		t.Fatalf("300 spent read as %+v", w)
	}
	if h := allowanceOf([]QuotaWindow{w}, now).CapHeld("any", caps, now); h != nil {
		t.Fatalf("held at 3%% of 4%%: %+v", h)
	}
	w = read(9500, now.Add(2*time.Minute)) // 500: 5%
	h := allowanceOf([]QuotaWindow{w}, now).CapHeld("any", caps, now)
	if h == nil || h.Used < 4 || h.Cap != 4 {
		t.Fatalf("500 of 10,000 spent, cap 4: held %+v (window %+v)", h, w)
	}
	// a window's own cap holds it as well
	if err := SetAccountCap("codex", "me@example.com", 0); err != nil {
		t.Fatal(err)
	}
	if err := SetWindowCap("codex", "me@example.com", "通用积分", 4); err != nil {
		t.Fatal(err)
	}
	caps = AccountCapsOf("codex", "me@example.com")
	w = read(9400, now.Add(3*time.Minute))
	if h := allowanceOf([]QuotaWindow{w}, now).CapHeld("any", caps, now); h == nil {
		t.Fatalf("the wallet's own cap 4 not held at %+v", w)
	}
	// 9won's own ask: at most 700 credits of the wallet a day, an amount
	if err := SetWindowCap("codex", "me@example.com", "通用积分", 0); err != nil {
		t.Fatal(err)
	}
	if err := SetWindowAmount("codex", "me@example.com", []string{"通用积分"}, 700); err != nil {
		t.Fatal(err)
	}
	caps = AccountCapsOf("codex", "me@example.com")
	w = read(9350, now.Add(4*time.Minute)) // 650 spent
	if h := allowanceOf([]QuotaWindow{w}, now).CapHeld("any", caps, now); h != nil {
		t.Fatalf("held at 650 of an amount cap of 700: %+v (window %+v)", h, w)
	}
	w = read(9250, now.Add(5*time.Minute)) // 750 spent
	if h := allowanceOf([]QuotaWindow{w}, now).CapHeld("any", caps, now); h == nil {
		t.Fatalf("750 spent, amount cap 700, not held (window %+v)", w)
	}
}

// Through the plugin host, as 9won's AStudio account went: a wallet window
// on an account capped at 4, read again after it spent 5% of what it had,
// reads as 5 used, and the cap holds the account.
func TestPluginWalletCapHolds(t *testing.T) {
	windows := filepath.Join(t.TempDir(), "windows.json")
	put := func(left int) {
		t.Helper()
		raw := fmt.Sprintf(`{"a@fake": [{"name": "通用积分", "balance": %d}], "full@fake": [{"name": "通用积分", "balance": %d}]}`, left, left)
		if err := os.WriteFile(windows, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	put(10000)
	q := fakePluginUsage(t, windows)["a@fake"]
	if len(q.Windows) != 1 || q.Windows[0].Balance == nil || q.Provider == "" {
		t.Fatalf("the wallet: %+v", q)
	}
	if err := SetAccountCap(q.Provider, "a@fake", 4); err != nil {
		t.Fatal(err)
	}
	put(9500)
	StaleAllowance("plugin:fakeco", "a@fake")
	StaleAllowance("plugin:fakeco", "full@fake")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	u := LoginUsage(ctx, "plugin:fakeco")
	now := time.Now()
	w := u["a@fake"].Windows[0]
	if w.Used != 5 || w.Amount != 500 || w.Limit != 10000 {
		t.Fatalf("500 of 10,000 spent read as %+v", w)
	}
	if h := allowanceOf(u["a@fake"].Windows, now).CapHeld("any", AccountCapsOf(q.Provider, "a@fake"), now); h == nil {
		t.Fatal("the cap of 4 didn't hold an account 5% spent")
	}
	// full@fake has no cap: its wallet reads as its balance alone
	if f := u["full@fake"].Windows[0]; f.Used != 0 || f.Spent == nil || *f.Spent != 500 {
		t.Fatalf("the uncapped account: %+v", f)
	}
}
