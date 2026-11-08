package provider

import (
	"encoding/json"
	"testing"
)

// Claude Max 20x is sold as Claude Ultra now, but Claude Code still says
// "max" for both Max plans: the account chip reads its rate-limit tier, or
// a 20x account reads the same as a 5x one.
func TestClaudeMax20xIsUltra(t *testing.T) {
	for _, c := range []struct{ sub, tier, want string }{
		{"max", "default_claude_max_20x", "ultra"},
		{"max", "default_claude_max_5x", "max"},
		{"max", "", "max"},
		{"pro", "default_claude_ai", "pro"},
		{"team", "default_claude_max_5x", "team"},
		{"ultra", "default_claude_max_20x", "ultra"},
	} {
		if got := claudePlan(c.sub, c.tier); got != c.want {
			t.Errorf("claudePlan(%q, %q) = %q, want %q", c.sub, c.tier, got, c.want)
		}
	}

	auth := func(tier string) []byte {
		b, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{
			"accessToken": "a", "refreshToken": "r", "subscriptionType": "max", "rateLimitTier": tier,
		}})
		return b
	}
	c, _ := parseClaudeCredentials(auth("default_claude_max_20x"))
	l, err := claudeLogin(c, map[string]any{"emailAddress": "u@example.com"})
	if err != nil || l.Plan != "ultra" {
		t.Fatalf("a new 20x sign-in: plan %q, %v; want ultra", l.Plan, err)
	}

	// accounts saved before as "max" read by their tier
	b, _ := json.Marshal([]savedLogin{
		{Agent: "claude", User: "twenty@example.com", Plan: "max", Auth: auth("default_claude_max_20x")},
		{Agent: "claude", User: "five@example.com", Plan: "max", Auth: auth("default_claude_max_5x")},
	})
	ls, err := parseLogins(b)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, l := range ls {
		got[l.User] = l.Plan
	}
	if got["twenty@example.com"] != "ultra" || got["five@example.com"] != "max" {
		t.Fatalf("saved plans read as %v; want 20x ultra, 5x max", got)
	}

	// Claude Code renewing a held Ultra account's sign-in under another's
	// profile still says "max": it is the same plan, so the held one is found
	other := json.RawMessage(`{"emailAddress":"other@example.com"}`)
	held := savedLogin{Agent: "claude", User: "twenty@example.com", Plan: "ultra", Held: true, HeldAs: claudeProfileKey(other),
		Auth: auth("default_claude_max_20x"), Profile: json.RawMessage(`{"emailAddress":"twenty@example.com"}`)}
	renewed := savedLogin{Agent: "claude", User: "other@example.com", Plan: "max", Auth: auth("default_claude_max_20x"), Profile: other}
	if h, ok := claudeRenewedHeld([]savedLogin{held}, renewed); !ok || h.User != held.User {
		t.Fatalf("a held Ultra account renewed under another profile isn't found: %v %v", h.User, ok)
	}
}
