package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// baksili_ on X: with Extra usage off in magpie, a Claude account whose
// plan is used up is held at 100% as routing plans it (capHeld, which
// every account of a provider, group or fallback goes through), not
// sent a request Anthropic would bill as extra usage; one with room is
// not. With it on (the default) nothing holds it. Every account held,
// the client is told why in those words and how to go on, and the
// call's record says so.
func TestClaudeNoExtraUsageHoldsTheAccount(t *testing.T) {
	fresh(t)
	capUsage(t, map[string]float64{"full@example.com": 100, "room@example.com": 40})
	p := provider.Provider{ID: "claude", Name: "Claude"}
	acct := func(user string) provider.Provider {
		return provider.Provider{ID: "claude", Name: "Claude", Account: &provider.Account{Agent: "claude", User: user}}
	}
	now := time.Now()
	if h := capHeld(p, acct("full@example.com"), "claude-sonnet-5", now); h != nil {
		t.Fatalf("held with Extra usage on: %+v", h)
	}
	if err := settings.Save(settings.Settings{ClaudeNoExtraUsage: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = settings.Save(settings.Settings{}) })
	if h := capHeld(p, acct("room@example.com"), "claude-sonnet-5", now); h != nil {
		t.Fatalf("held with its plan not used up: %+v", h)
	}
	h := capHeld(p, acct("full@example.com"), "claude-sonnet-5", now)
	if h == nil || h.cap != 100 || !h.noCredits || !h.extra || h.back.IsZero() {
		t.Fatalf("used up, not held at 100%% for its extra usage: %+v", h)
	}
	ws := barredOf([]candidate{{p: acct("full@example.com"), model: "claude-sonnet-5", rest: "claude#full@example.com", capped: h}}, p, false, provider.Anthropic, nil)
	if len(ws) != 1 || !ws[0].ExtraUsage || !ws[0].NoCredits || ws[0].Capped != 100 {
		t.Fatalf("traced: %+v", ws)
	}
	msg, back := cappedError("claude-sonnet-5", ws, now)
	for _, want := range []string{"set in magpie not to spend extra usage", "has used up a usage window and is set not to spend extra usage", "claudeNoExtraUsage off"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "credits (Usage") || back.IsZero() {
		t.Errorf("refusal told as a Codex account's credits, or no wait: %q %v", msg, back)
	}
	if got := cappedRecord(ws); got != "every account held: set not to spend extra usage" {
		t.Errorf("record %q", got)
	}
	// a Codex account set not to spend its credits beside it: both named
	cw := ws[0]
	cw.ExtraUsage, cw.Who = false, "codex@example.com"
	if got := cappedRecord(append(ws, cw)); got != "every account held: set not to spend its credits or extra usage" {
		t.Errorf("record of both %q", got)
	}
}
