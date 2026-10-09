package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// An account held at its usage cap: a window counting the model at or
// past the cap and not renewed since; back when the last such renews.
func TestCapHeld(t *testing.T) {
	now := time.Now()
	five, week := now.Add(3*time.Hour), now.Add(4*24*time.Hour)
	a := Allowance{
		{Used: 75, Resets: five, Span: 5 * time.Hour},
		{Used: 40, Resets: week, Span: 7 * 24 * time.Hour},
		{Used: 95, Resets: week, Span: 7 * 24 * time.Hour, Model: "opus"},
	}
	all := func(n int) WindowCaps { return WindowCaps{All: n} }
	if h := a.CapHeld("gpt-6", all(70), now); h == nil || h.Used != 75 || h.Cap != 70 || !h.Back.Equal(five) || h.Credits {
		t.Fatalf("at 75%% of 70%%: %+v", h)
	}
	if h := a.CapHeld("gpt-6", all(80), now); h != nil {
		t.Fatal("held below its cap")
	}
	if h := a.CapHeld("gpt-6", all(0), now); h != nil {
		t.Fatal("held with no cap")
	}
	// Opus's own window counts for Opus alone
	if h := a.CapHeld("claude-opus-5", all(90), now); h == nil || h.Used != 95 || !h.Back.Equal(week) {
		t.Fatalf("opus at 95%% of 90%%: %+v", h)
	}
	if h := a.CapHeld("claude-sonnet-5", all(90), now); h != nil {
		t.Fatal("Opus's window held another model")
	}
	// renewed since it was read: empty again
	if h := a.CapHeld("gpt-6", all(70), five.Add(time.Minute)); h != nil {
		t.Fatal("held by a window that has renewed")
	}
	// one not saying when it renews: back not known
	if h := (Allowance{{Used: 80}}).CapHeld("gpt-6", all(70), now); h == nil || !h.Back.IsZero() {
		t.Fatalf("no reset: %+v", h)
	}
}

// capReached counts the windows that stop the account: not the on-demand
// ones (Aside), not one model's.
func TestCapReached(t *testing.T) {
	now := time.Now()
	later, gone := now.Add(time.Hour), now.Add(-time.Minute)
	for _, c := range []struct {
		name string
		w    QuotaWindow
		want bool
	}{
		{"five hours past it", QuotaWindow{Used: 75, ResetsAt: &later}, true},
		{"at it", QuotaWindow{Used: 70, ResetsAt: &later}, true},
		{"below it", QuotaWindow{Used: 69.5, ResetsAt: &later}, false},
		{"renewed since", QuotaWindow{Used: 90, ResetsAt: &gone}, false},
		{"on-demand spending", QuotaWindow{Used: 90, ResetsAt: &later, Aside: true}, false},
		{"one model's", QuotaWindow{Used: 90, ResetsAt: &later, Model: "opus"}, false},
	} {
		if got := capReached(SubscriptionQuota{Windows: []QuotaWindow{c.w}}, WindowCaps{All: 70}, now); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	if capReached(SubscriptionQuota{Windows: []QuotaWindow{{Used: 99, ResetsAt: &later}}}, WindowCaps{}, now) {
		t.Error("no cap reached")
	}
}

// WithCapped marks every window a cap counts, as CapHeld holds them: a
// window one model's own counts for that model, so it is marked like the
// rest — the GUI had nothing to show for an account held at its Opus
// window, though routing was holding it there (TestCapHeld).
func TestWithCappedMarksPerModelWindows(t *testing.T) {
	later := time.Now().Add(time.Hour)
	windows := []QuotaWindow{
		{Used: 30, ResetsAt: &later},
		{Used: 90, ResetsAt: &later, Model: "opus"},
		{Used: 90, ResetsAt: &later, Aside: true},
		{Unlimited: true, Used: 90, ResetsAt: &later},
	}
	w := WithCapped(map[string]SubscriptionQuota{"me@example.com": {Windows: windows}})["me@example.com"].Windows
	if !w[0].Capped {
		t.Error("the window every model's is not marked")
	}
	if !w[1].Capped {
		t.Error("the window one model's own is not marked")
	}
	if w[0].CapsSome || !w[1].CapsSome {
		t.Error("the window one model's own is not told from the account's")
	}
	if w[2].Capped {
		t.Error("on-demand spending is marked")
	}
	if w[3].Capped {
		t.Error("an unlimited window is marked")
	}
	if (SubscriptionQuota{Windows: windows}).Windows[1].Capped {
		t.Error("the windows given were marked, not copies")
	}
}

// A window can have a cap of its own beside the account's (willz on
// Discord): a friend's account stops at 50% of its five hours while its
// week may go to 40%. The account is held while any window is at or past
// its own share; a window with none of its own takes the account's.
func TestWindowCapHeld(t *testing.T) {
	now := time.Now()
	five, week := now.Add(3*time.Hour), now.Add(4*24*time.Hour)
	// the vendor's readings, as allowanceOf makes them: windows by name
	read := func(fiveUsed, weekUsed float64) Allowance {
		return allowanceOf([]QuotaWindow{
			{Name: "5 hours", Used: fiveUsed, ResetsAt: &five, Span: 5 * time.Hour},
			{Name: "Weekly", Used: weekUsed, ResetsAt: &week, Span: 7 * 24 * time.Hour},
		}, now)
	}
	friend := WindowCaps{Windows: map[string]int{"5 hours": 50, "weekly": 40}}
	// the five hours at 60%, past its 50%: held, at that window's cap
	if h := read(60, 10).CapHeld("gpt-6", friend, now); h == nil || h.Cap != 50 || h.Used != 60 || !h.Back.Equal(five) {
		t.Fatalf("5 hours at 60%% of 50%%: %+v", h)
	}
	// the week at 30% of its 40%, the five hours at 20% of 50%: used
	if h := read(20, 30).CapHeld("gpt-6", friend, now); h != nil {
		t.Fatalf("both under their own caps, held: %+v", h)
	}
	// the week at 45%, past its 40%, though under the five hours' 50%
	if h := read(20, 45).CapHeld("gpt-6", friend, now); h == nil || h.Cap != 40 || !h.Back.Equal(week) {
		t.Fatalf("week at 45%% of 40%%: %+v", h)
	}
	// one window's own beside the account's: the week follows the account
	mixed := WindowCaps{All: 10, Windows: map[string]int{"5 hours": 50}}
	if h := read(30, 5).CapHeld("gpt-6", mixed, now); h != nil {
		t.Fatalf("5 hours at 30%% of its own 50%%, held: %+v", h)
	}
	if h := read(30, 12).CapHeld("gpt-6", mixed, now); h == nil || h.Cap != 10 {
		t.Fatalf("week at 12%% of the account's 10%%: %+v", h)
	}
	// 100 on a window is none there, whatever the account's
	free := WindowCaps{All: 10, Windows: map[string]int{"weekly": 100}}
	if h := read(5, 95).CapHeld("gpt-6", free, now); h != nil {
		t.Fatalf("week with no cap of its own held: %+v", h)
	}
	// set not to spend its credits, that window is still held at 100%
	free.Credits = true
	if h := read(5, 100).CapHeld("gpt-6", free, now); h == nil || h.Cap != 100 || !h.Credits {
		t.Fatalf("credits off, week at 100%%: %+v", h)
	}
	// the window's name is matched in any case
	if c, _ := (WindowCaps{Windows: map[string]int{"weekly": 40}}).Of(" WEEKLY "); c != 40 {
		t.Fatalf("named in another case: %d", c)
	}
	// capReached, for the agent signed in, goes by each window's own too
	ws := []QuotaWindow{{Name: "5 hours", Used: 60, ResetsAt: &five}, {Name: "Weekly", Used: 30, ResetsAt: &week}}
	if !capReached(SubscriptionQuota{Windows: ws}, friend, now) {
		t.Fatal("5 hours at 60% of 50% not reached")
	}
	ws[0].Used = 20
	if capReached(SubscriptionQuota{Windows: ws}, friend, now) {
		t.Fatal("reached under both windows' own caps")
	}
}

// Old settings, with no window's own cap, are read as they were: one
// share every window. A window's own is kept in lower case, and 0 has the
// window follow the account's cap again.
func TestSetWindowCap(t *testing.T) {
	signIn(t) // me@example.com on codex
	if err := SetAccountCap("codex", "me@example.com", 70); err != nil {
		t.Fatal(err)
	}
	c := AccountCapsOf("codex", "me@example.com")
	if c.All != 70 || c.Windows != nil {
		t.Fatalf("an old cap read as %+v", c)
	}
	if s, _ := c.Of("Weekly"); s != 70 {
		t.Fatalf("the week with the account's cap: %d", s)
	}
	if err := SetWindowCap("codex", "ME@example.com", "Weekly", 40); err != nil {
		t.Fatal(err)
	}
	c = AccountCapsOf("codex", "me@example.com")
	if s, _ := c.Of("weekly"); s != 40 || c.All != 70 {
		t.Fatalf("the week's own: %+v", c)
	}
	if s, _ := c.Of("5 hours"); s != 70 {
		t.Fatalf("the five hours not following the account's cap: %d", s)
	}
	for _, bad := range []int{-1, 101} {
		if err := SetWindowCap("codex", "me@example.com", "Weekly", bad); err == nil {
			t.Errorf("%d taken", bad)
		}
	}
	if err := SetWindowCap("codex", "me@example.com", "", 40); err == nil {
		t.Error("a window with no name taken")
	}
	if err := SetWindowCap("codex", "me@example.com", "weekly", 0); err != nil {
		t.Fatal(err)
	}
	if c := AccountCapsOf("codex", "me@example.com"); c.Windows != nil {
		t.Fatalf("cleared, still %+v", c)
	}
	if p, _ := Find("codex"); p.AccountWindowCaps != nil || p.AccountCap("me@example.com") != 70 {
		t.Fatalf("saved as %+v", p.AccountWindowCaps)
	}
}

func TestParseCap(t *testing.T) {
	for in, want := range map[string]int{"70": 70, "70%": 70, " 5 % ": 5, "off": 0, "none": 0, "0": 0, "100": 0, "-": 0} {
		if got, err := ParseCap(in); err != nil || got != want {
			t.Errorf("%q: %d %v", in, got, err)
		}
	}
	for _, in := range []string{"", "abc", "150", "-5", "0.5"} {
		if _, err := ParseCap(in); err == nil {
			t.Errorf("%q taken", in)
		}
	}
}

// The cap is the user's, not the vendor's: a Codex account at 80% of its
// week with a 70% cap is held for routing, but the resets go by the
// vendor's own 100% — no reset is spent for it, auto-used or about to run
// out — and it reads as used up only at 100%.
func TestCapNeverSpendsACodexReset(t *testing.T) {
	now := time.Now()
	five, week, until := now.Add(2*time.Hour), now.Add(3*24*time.Hour), now.Add(20*24*time.Hour)
	ws := []QuotaWindow{
		{Name: "5-hour", Used: 80, ResetsAt: &five, Span: 5 * time.Hour},
		{Name: "Weekly", Used: 80, ResetsAt: &week, Span: 7 * 24 * time.Hour},
	}
	q := SubscriptionQuota{Provider: "codex", Windows: ws}
	if !capReached(q, WindowCaps{All: 70}, now) {
		t.Fatal("80% isn't past a 70% cap")
	}
	if weekUsedUp(ws, now) != nil {
		t.Fatal("an auto-used reset would be spent on a week at 80%")
	}
	if usedUp(q, now) || !BackAt(q, now).IsZero() {
		t.Fatal("80% read as used up")
	}
	if spendExpiringNow(ws, &ResetCredits{Count: 1, Until: &until}, now) {
		t.Fatal("a reset with 20 days to run is spent at 80%")
	}
	if h := allowanceOf(ws, now).CapHeld("gpt-6", WindowCaps{All: 70}, now); h == nil {
		t.Fatal("routing doesn't hold it")
	}
}

// Codex signed in to an account past its usage cap is signed in to the
// next one with room, and kept off it while it is at the cap, though it is
// below where the switch list goes back (90%): lifted, it goes back.
func TestCodexSwitchedAtCap(t *testing.T) {
	home := signIn(t) // me@example.com, acct-1
	rememberLogins(true)
	codexSignIn(t, home, "new@example.com", "r-new") // signed in to now
	rememberLogins(true)
	used := map[string]float64{"acct-new@example.com": 75, "acct-1": 20}
	reset := time.Now().Add(3 * time.Hour).Unix()
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"plan_type": "plus", "rate_limit": map[string]any{
			"primary_window": map[string]any{"used_percent": used[r.Header.Get("chatgpt-account-id")], "limit_window_seconds": 18000, "reset_at": reset}}})
	}))
	defer fake.Close()
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })
	switched := func() string {
		t.Helper()
		loginUsageCache.Lock()
		loginUsageCache.m = nil
		loginUsageCache.Unlock()
		to, err := SwitchWhenSpent(context.Background(), "codex")
		if err != nil {
			t.Fatal(err)
		}
		return to
	}
	if err := SetLoginOn("codex", "me@example.com", true); err != nil {
		t.Fatal(err)
	}
	// 75% and no cap: it stays
	if to := switched(); to != "" {
		t.Fatalf("switched to %s at 75%% with no cap", to)
	}
	if err := SetAccountCap("codex", "New@Example.com", 70); err != nil {
		t.Fatal(err)
	}
	if c := AccountCapOf("codex", "new@example.com"); c != 70 {
		t.Fatalf("cap kept as %d", c)
	}
	if to := switched(); to != "me@example.com" {
		t.Fatalf("at its cap, switched to %q", to)
	}
	// below 90%, but at its cap: not back yet
	if to := switched(); to != "" {
		t.Fatalf("went back to %s while it is at its cap", to)
	}
	// the cap lifted, it has room: back
	if err := SetAccountCap("codex", "new@example.com", 0); err != nil {
		t.Fatal(err)
	}
	if c := AccountCapOf("codex", "new@example.com"); c != 0 {
		t.Fatalf("cap lifted, kept as %d", c)
	}
	if to := switched(); to != "new@example.com" {
		t.Fatalf("cap lifted, switched to %q", to)
	}
	// a cap out of 1–99 isn't taken
	if err := SetAccountCap("codex", "me@example.com", 100); err == nil {
		t.Fatal("a 100% cap taken")
	}
	if err := SetAccountCap("codex", "nobody@example.com", 50); err == nil {
		t.Fatal("a cap set on an account it hasn't")
	}
}
