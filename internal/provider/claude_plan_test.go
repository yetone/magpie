package provider

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
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
		// upgraded from Pro: Claude Code keeps "pro", the tier says Max
		{"pro", "default_claude_max_5x", "max"},
		{"pro", "default_claude_max_20x", "ultra"},
		{"enterprise", "default_claude_max_20x", "enterprise"},
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

// The Usage card of Claude Code's own sign-in, the only Claude account,
// named its plan by `claude auth status`, which says "max" for Max 20x
// too: an Ultra account read "Max" (shangyuan0818 on X). It is named as
// the account is. Credentials and profile are shaped as Claude Code 2.1
// writes them; plan_display_name is what /api/oauth/profile answers
// (organization.plan_display_name "Ultra" for a 20x account, none for 5x).
func TestClaudeUsageCardPlanIsUltra(t *testing.T) {
	for _, c := range []struct {
		name, tier, shown, want string
	}{
		{"20x", "default_claude_max_20x", "Ultra", "ultra"},
		{"20x, profile not yet read", "default_claude_max_20x", "", "ultra"},
		{"no tier, Claude names it Ultra", "", "Ultra", "ultra"},
		{"5x", "default_claude_max_5x", "", "max"},
		// a profile left from another account doesn't rename a known tier
		{"5x under a 20x profile", "default_claude_max_5x", "Ultra", "max"},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := claudeHome(t)
			writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), map[string]any{
				"claudeAiOauth": map[string]any{
					"accessToken": "sk-ant-oat01-a", "refreshToken": "sk-ant-ort01-a",
					"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": "max",
					"rateLimitTier": c.tier, "scopes": []string{"user:inference", "user:profile"},
				},
			})
			acct := map[string]any{"emailAddress": "a@example.com", "billingType": "stripe_subscription"}
			if c.shown != "" {
				acct["planDisplayName"] = c.shown
			}
			writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{"oauthAccount": acct})
			dir := t.TempDir()
			exe := filepath.Join(dir, "claude")
			testenv.Program(t, exe, "#!/bin/sh\necho '{\"loggedIn\":true,\"authMethod\":\"claude.ai\",\"apiProvider\":\"firstParty\",\"email\":\"a@example.com\",\"subscriptionType\":\"max\"}'\n")
			old := claudeExecutable
			claudeExecutable = func() string { return exe }
			t.Cleanup(func() { claudeExecutable = old; forgetClaudeStatus() })
			forgetClaudeStatus()
			var out atomic.Value
			out.Store("Current session: 40% used")
			fakeClaudeUsage(t, &out, nil)

			if q := claudeSubscriptionUsage(context.Background(), "a@example.com"); q.Plan != c.want {
				t.Errorf("Usage card plan %q, want %q", q.Plan, c.want)
			}
			if p, ok := claudeAccount(); !ok || p.Account.Plan != c.want {
				t.Errorf("account plan %v %q, want %q", ok, p.Account.Plan, c.want)
			}
		})
	}
}

// A sign-in or saved account whose credentials carry no tier is named by
// the plan name Claude gave its profile.
func TestClaudeSavedPlanFromProfileName(t *testing.T) {
	c, _ := parseClaudeCredentials([]byte(`{"claudeAiOauth":{"accessToken":"a","refreshToken":"r","subscriptionType":"max"}}`))
	l, err := claudeLogin(c, map[string]any{"emailAddress": "u@example.com", "planDisplayName": "Ultra"})
	if err != nil || l.Plan != "ultra" {
		t.Fatalf("sign-in plan %q, %v; want ultra", l.Plan, err)
	}
	b, _ := json.Marshal([]savedLogin{{Agent: "claude", User: "u@example.com", Plan: "max",
		Auth:    json.RawMessage(`{"claudeAiOauth":{"accessToken":"a","refreshToken":"r","subscriptionType":"max"}}`),
		Profile: json.RawMessage(`{"emailAddress":"u@example.com","planDisplayName":"Ultra"}`)}})
	ls, err := parseLogins(b)
	if err != nil || len(ls) != 1 || ls[0].Plan != "ultra" {
		t.Fatalf("saved plan %+v %v; want ultra", ls, err)
	}
}

// CoderMaurice on X: a Claude account upgraded from Pro to Max kept
// "Claude Pro" on its row in the editor, while the tray drew Max's weekly
// Fable window for it. Claude Code keeps the subscription type it was
// signed in with ("pro") and renews only the rate-limit tier beside the
// same tokens, so the plan is the tier's: on a saved account, read back
// from the directory its /usage ran in, and on one saved before.
func TestClaudeUpgradedFromProReadsMax(t *testing.T) {
	claudeThree(t)
	pro := "pro@x.com"
	if err := SwitchLogin("claude", "max@x.com"); err != nil {
		t.Fatal(err)
	}
	planOf := func() string {
		for _, l := range Logins("claude") {
			if l.User == pro {
				if l.Active {
					t.Fatal("the Pro account is still Claude Code's own")
				}
				return l.Plan
			}
		}
		t.Fatal("no Pro account")
		return ""
	}
	if p := planOf(); p != "pro" {
		t.Fatalf("before the upgrade the account reads %q", p)
	}
	renewedAt := func() time.Time {
		for _, l := range readLogins() {
			if l.User == pro {
				return l.Renewed
			}
		}
		return time.Time{}
	}
	was := renewedAt()
	var text atomic.Value
	text.Store("Current session: 33% used\nCurrent week (all models): 40% used\nCurrent week (Fable): 40% used")
	runs := fakeClaudeUsage(t, &text, nil)
	UsageClaudeVia(func(_ context.Context, dir string) (string, error) {
		runs.Add(1)
		if dir == claudeAccountDir(pro) {
			// Claude Code's run renews the profile: the tokens it holds
			// stay, the tier is Max's now, the subscription type still Pro's
			c, ok := readClaudeDir(dir)
			if !ok {
				return "", errors.New("no sign-in in the account's directory")
			}
			oauth := c.raw["claudeAiOauth"].(map[string]any)
			oauth["rateLimitTier"] = "default_claude_max_5x"
			writeFile(t, filepath.Join(dir, ".credentials.json"), c.raw)
		}
		return text.Load().(string), nil
	})
	ctx := context.Background()
	RefreshUsage(ctx, "claude", pro)
	q := LoginUsage(ctx, "claude")[pro]
	if q.Error != "" || len(q.Windows) < 3 {
		t.Fatalf("the refresh read no allowance: %+v", q)
	}
	if q.Plan != "max" {
		t.Errorf("the reading names the plan %q, want max", q.Plan)
	}
	if p := planOf(); p != "max" {
		t.Errorf("the saved account reads %q after its run, want max", p)
	}
	for _, l := range readLogins() {
		if l.User == pro {
			c, _ := parseClaudeCredentials(l.Auth)
			if c.OAuth.RateLimitTier != "default_claude_max_5x" || c.OAuth.AccessToken != "tok-"+pro {
				t.Errorf("saved sign-in %+v; want the same tokens with Max's tier", c.OAuth)
			}
		}
	}
	// only the plan changed: the sign-in wasn't renewed
	if r := renewedAt(); !r.Equal(was) {
		t.Errorf("a tier renewed alone marked the sign-in renewed at %v (was %v)", r, was)
	}

	// saved before: logins.json says "pro" beside Max's tier
	b, _ := json.Marshal([]savedLogin{{Agent: "claude", User: "u@example.com", Plan: "pro",
		Auth: json.RawMessage(`{"claudeAiOauth":{"accessToken":"a","refreshToken":"r","subscriptionType":"pro","rateLimitTier":"default_claude_max_5x"}}`)}})
	ls, err := parseLogins(b)
	if err != nil || len(ls) != 1 || ls[0].Plan != "max" {
		t.Fatalf("saved plan %+v %v; want max", ls, err)
	}
}
