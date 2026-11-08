package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A Kimi Code key whose month is at 98.85% goes on on its booster while
// the booster holds money (#5721): a quota refusal rests it the quota rest,
// not until the month renews 17 days on. With the booster used up, the
// month counts as full as before (the rest held to its longest).
func TestKimiKeyOnItsBoosterIsNotRestedToTheMonth(t *testing.T) {
	forgetRouting()
	now := time.Now()
	month := now.Add(17 * 24 * time.Hour)
	windows := func(credits bool) provider.Allowance {
		return provider.Allowance{
			{Name: "5 hours", Used: 0, Resets: now.Add(80 * time.Minute), Span: 5 * time.Hour, Credits: credits},
			{Name: "Month", Used: 98.85, Resets: month, Span: 30 * 24 * time.Hour, Credits: credits},
		}
	}
	keyWindows(t, map[string]provider.Allowance{"sk-left": windows(true), "sk-spent": windows(false)})
	key := func(k string) candidate {
		return candidate{p: provider.Provider{ID: "kimi-code", Key: k}, model: "kimi-for-coding", rest: "kimi-code#" + k}
	}
	s := &Server{}
	refused := []byte(`{"error":{"message":"You've reached your usage limit","type":"rate_limit_error"}}`)

	if !key("sk-left").full(now).IsZero() {
		t.Fatal("a key on its booster counts as full at the month's 98%")
	}
	if r := s.restAfter(key("sk-left"), 429, http.Header{}, refused); r.By == "window" || !r.Until.Before(now.Add(24*time.Hour)) {
		t.Fatalf("a key on its booster rests %v by %s (%s)", time.Until(r.Until), r.By, r.Why)
	}

	if !key("sk-spent").full(now).Equal(month) {
		t.Fatal("a key with its booster used up isn't full at the month's 98%")
	}
	if r := s.restAfter(key("sk-spent"), 429, http.Header{}, refused); r.Why != failQuota || r.By != "window" || r.Until.Before(now.Add(7*24*time.Hour)) {
		t.Fatalf("a key with its booster used up rests %v by %s (%s)", time.Until(r.Until), r.By, r.Why)
	}
}
