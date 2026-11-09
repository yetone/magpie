package provider

import (
	"math"
	"testing"
	"time"
)

// 9won_25273 on Discord: an account capped at a fixed amount of a window
// counted in a unit (at most 300 of a day's 1000 credits) is held once
// the vendor's count reaches it, and its share is the amount's share of
// the total; a window read without a total leaves the amount aside.
func TestWindowAmountCapHeld(t *testing.T) {
	now := time.Now()
	day := now.Add(5 * time.Hour)
	read := func(count float64) Allowance {
		return allowanceOf([]QuotaWindow{
			{Name: "Daily credits", Used: count / 10, Amount: count, Limit: 1000, Unit: "credits", ResetsAt: &day, Span: 24 * time.Hour},
		}, now)
	}
	caps := WindowCaps{Amounts: map[string]float64{"daily credits": 300}}
	if !caps.Holds() {
		t.Fatal("an amount cap holds nothing")
	}
	if h := read(299).CapHeld("gpt-6", caps, now); h != nil {
		t.Fatalf("299 of 300 credits, held: %+v", h)
	}
	h := read(300).CapHeld("gpt-6", caps, now)
	if h == nil || h.Amount != 300 || h.Count != 300 || h.Unit != "credits" || h.Cap != 30 || !h.Back.Equal(day) || h.Credits {
		t.Fatalf("300 of 300 credits: %+v", h)
	}
	// the amount's share rounds up: 250.5 of 1000 holds at 26%, not 25%
	odd := WindowCaps{Amounts: map[string]float64{"daily credits": 250.5}}
	if h := read(260).CapHeld("gpt-6", odd, now); h == nil || h.Cap != 26 {
		t.Fatalf("250.5 of 1000: %+v", h)
	}
	// the amount stands in for the account's own share on that window
	both := WindowCaps{All: 10, Amounts: map[string]float64{"daily credits": 300}}
	if h := read(200).CapHeld("gpt-6", both, now); h != nil {
		t.Fatalf("200 credits, at 20%% past the account's 10%%, held: %+v", h)
	}
	// near the cap by a few points of its share
	if !read(290).NearCap("gpt-6", caps, 2, now) || read(200).NearCap("gpt-6", caps, 2, now) {
		t.Fatal("NearCap by the amount's share")
	}
	// renewed since it was read
	if h := read(400).CapHeld("gpt-6", caps, day.Add(time.Minute)); h != nil {
		t.Fatalf("held by a renewed window: %+v", h)
	}
	// a reading with no total: the amount can't be told, the account's
	// share goes on
	noTotal := allowanceOf([]QuotaWindow{{Name: "Daily credits", Used: 50, Amount: 900, ResetsAt: &day}}, now)
	if h := noTotal.CapHeld("gpt-6", caps, now); h != nil {
		t.Fatalf("an amount read against no total held: %+v", h)
	}
	if h := noTotal.CapHeld("gpt-6", both, now); h == nil || h.Cap != 10 || h.Amount != 0 {
		t.Fatalf("no total, the account's 10%%: %+v", h)
	}
	// capReached, for the agent signed in, goes by the count too
	ws := []QuotaWindow{{Name: "Daily credits", Used: 31, Amount: 310, Limit: 1000, Unit: "credits", ResetsAt: &day}}
	if !capReached(SubscriptionQuota{Windows: ws}, caps, now) {
		t.Fatal("310 of 300 credits not reached")
	}
	ws[0].Amount, ws[0].Used = 100, 10
	if capReached(SubscriptionQuota{Windows: ws}, caps, now) {
		t.Fatal("100 of 300 credits reached")
	}
}

// An amount and a share on one window replace each other; 0 lifts the
// amount; a bad amount is refused; settings keep it in lower case.
func TestSetWindowAmount(t *testing.T) {
	signIn(t) // me@example.com on codex
	if err := SetWindowCap("codex", "me@example.com", "Weekly", 40); err != nil {
		t.Fatal(err)
	}
	if err := SetWindowAmount("codex", "ME@example.com", []string{"Weekly"}, 300); err != nil {
		t.Fatal(err)
	}
	c := AccountCapsOf("codex", "me@example.com")
	if c.Amounts["weekly"] != 300 || c.Windows != nil {
		t.Fatalf("the amount beside the week's own share: %+v", c)
	}
	if err := SetWindowCap("codex", "me@example.com", "weekly", 50); err != nil {
		t.Fatal(err)
	}
	c = AccountCapsOf("codex", "me@example.com")
	if c.Amounts != nil || c.Windows["weekly"] != 50 {
		t.Fatalf("a share set over the amount: %+v", c)
	}
	if err := SetWindowAmount("codex", "me@example.com", []string{"weekly"}, 12.5); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []float64{-1, math.NaN(), math.Inf(1)} {
		if err := SetWindowAmount("codex", "me@example.com", []string{"weekly"}, bad); err == nil {
			t.Errorf("%v taken", bad)
		}
	}
	if err := SetWindowAmount("codex", "me@example.com", []string{" "}, 10); err == nil {
		t.Error("a window with no name taken")
	}
	if p, _ := Find("codex"); p.AccountWindowAmounts["me@example.com"]["weekly"] != 12.5 {
		t.Fatalf("saved as %+v", p.AccountWindowAmounts)
	}
	if err := SetWindowAmount("codex", "me@example.com", []string{"weekly"}, 0); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("codex"); p.AccountWindowAmounts != nil || p.AccountWindowCaps != nil {
		t.Fatalf("lifted, still %+v %+v", p.AccountWindowAmounts, p.AccountWindowCaps)
	}
	got := normalWindowAmounts(map[string]map[string]float64{"ME@Example.com": {" Weekly ": 5, "five": 0, "inf": math.Inf(1), "nan": math.NaN()}, "": {"x": 1}})
	if len(got) != 1 || len(got["me@example.com"]) != 1 || got["me@example.com"]["weekly"] != 5 {
		t.Fatalf("normal: %+v", got)
	}
}
