package provider

import (
	"context"
	"testing"
	"time"
)

// A window whose reset has passed has started again, however the reading
// came: Claude Code's /usage tells a reset to the minute and can lag it,
// so a fresh run can say 100% of a session that reset a minute ago. Such a
// reading isn't spent, and Claude Code isn't moved off its account for it;
// one whose reset is still to come is.
func TestFreshClaudeReadingPastItsResetIsNotSpent(t *testing.T) {
	for _, c := range []struct {
		name  string
		reset time.Time
		spent bool
	}{
		{"reset a minute ago", time.Now().Add(-time.Minute), false},
		{"reset in an hour", time.Now().Add(time.Hour), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			at := c.reset.UTC().Format("Jan 2, 2006 at 3:04pm") + " (UTC)"
			_, card := claudeUsageCards(t, "Current session: 100% used · resets "+at+"\nCurrent week (all models): 10% used · resets "+soon(3)+" at 2pm (UTC)\n", nil)
			loginsMu.Lock()
			ls := upsertLogin(readLogins(), savedLogin{Agent: "claude", User: "b@example.com", Plan: "max", On: true, Seen: time.Now().UTC(),
				Auth: mustJSONRaw(t, map[string]any{"claudeAiOauth": map[string]any{"accessToken": "sk-ant-oat01-b", "refreshToken": "sk-ant-ort01-b",
					"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": "max", "scopes": []string{"user:inference", "user:profile"}}}),
				Profile: mustJSONRaw(t, map[string]any{"emailAddress": "b@example.com", "accountUuid": "u-b"})})
			err := writeLogins(ls)
			loginsMu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			NoteClaudeLimits("b@example.com", []ClaudeLimit{{Kind: "five_hour", Used: .1, ResetsAt: time.Now().Add(3 * time.Hour).Unix()}})

			q := card() // a fresh /usage run
			if q.Error != "" || q.AsOf != nil || len(q.Windows) != 2 {
				t.Fatalf("not a fresh reading: %+v", q)
			}
			if got := q.Windows[0].Used >= 100; got != c.spent {
				t.Fatalf("the session reads %g%% (reset %v)", q.Windows[0].Used, q.Windows[0].ResetsAt)
			}
			if got := usedUp(q, time.Now()); got != c.spent {
				t.Fatalf("used up %v, want %v: %+v", got, c.spent, q.Windows)
			}
			from, to, back, ok := NextLogin(context.Background(), "claude")
			if ok != c.spent || c.spent && (from != "a@example.com" || to != "b@example.com" || back) {
				t.Fatalf("got %q→%q back=%v ok=%v, want a move %v", from, to, back, ok, c.spent)
			}
		})
	}
}

// NextLogin counts a window whose reset has passed as started again on
// every account it looks at — the one the agent is on, the one to go back
// to and the spares — for a fresh reading as for one kept from before
// (AsOf), Codex's as Claude's.
func TestNextLoginTakesAPassedResetAsRenewed(t *testing.T) {
	past, future := time.Now().Add(-time.Minute), time.Now().Add(time.Hour)
	full := func(at time.Time) []QuotaWindow {
		return []QuotaWindow{{Name: "5 hours", Used: 100, ResetsAt: &at, Span: 5 * time.Hour}}
	}
	half := []QuotaWindow{{Name: "5 hours", Used: 50, ResetsAt: &future, Span: 5 * time.Hour}}
	room := []QuotaWindow{{Name: "5 hours", Used: 10, ResetsAt: &future, Span: 5 * time.Hour}}
	type move struct {
		ok, back bool
	}
	cases := []struct {
		name      string
		on, spare []QuotaWindow
		returns   bool
		asOf      bool
		want      move
	}{
		{"on renewed", full(past), room, false, false, move{}},
		{"on renewed, kept reading", full(past), room, false, true, move{}},
		{"on still full", full(future), room, false, false, move{ok: true}},
		{"spare renewed", full(future), full(past), false, false, move{ok: true}},
		{"spare still full", full(future), full(future), false, false, move{}},
		{"back to one renewed", half, full(past), true, false, move{ok: true, back: true}},
		{"back to one still full", half, full(future), true, false, move{}},
	}
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			var on, spare string
			if agent == "codex" {
				home := signIn(t) // me@example.com
				rememberLogins(true)
				codexSignIn(t, home, "work@example.com", "r-work")
				rememberLogins(true)
				on, spare = "work@example.com", "me@example.com"
			} else {
				home := claudeHome(t)
				claudeSignIn(t, home, time.Now().Add(time.Hour))
				writeFile(t, home+"/.claude.json", map[string]any{"oauthAccount": map[string]any{"emailAddress": "a@example.com", "accountUuid": "u-a"}})
				rememberLogins(true)
				loginsMu.Lock()
				ls := upsertLogin(readLogins(), savedLogin{Agent: "claude", User: "b@example.com", Plan: "max", Seen: time.Now().UTC(),
					Auth: mustJSONRaw(t, map[string]any{"claudeAiOauth": map[string]any{"accessToken": "sk-ant-oat01-b", "refreshToken": "sk-ant-ort01-b",
						"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": "max", "scopes": []string{"user:inference", "user:profile"}}}),
					Profile: mustJSONRaw(t, map[string]any{"emailAddress": "b@example.com", "accountUuid": "u-b"})})
				err := writeLogins(ls)
				loginsMu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				on, spare = "a@example.com", "b@example.com"
			}
			if err := SetLoginOn(agent, spare, true); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { LoginUsageVia(nil) })
			for _, c := range cases {
				var asOf *time.Time
				if c.asOf {
					at := time.Now().Add(-2 * time.Hour)
					asOf = &at
				}
				LoginUsageVia(func(context.Context, string) map[string]SubscriptionQuota {
					return map[string]SubscriptionQuota{
						on:    {Provider: agent, User: on, Windows: c.on, AsOf: asOf},
						spare: {Provider: agent, User: spare, Windows: c.spare},
					}
				})
				r := loginReturn{}
				if c.returns {
					r = loginReturn{Back: spare, To: on}
				}
				setLoginReturn(agent, r)
				from, to, back, ok := NextLogin(context.Background(), agent)
				if ok != c.want.ok || back != c.want.back || ok && (from != on || to != spare) {
					t.Errorf("%s: got %q→%q back=%v ok=%v, want %+v", c.name, from, to, back, ok, c.want)
				}
			}
		})
	}
}

// What stops an account, for magpie quota wait and the Codex resets, is a
// full window still to reset: one whose reset has passed doesn't stop it,
// nor say when it is back.
func TestUsedUpLeavesOutARenewedWindow(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Minute), now.Add(time.Hour)
	renewed := SubscriptionQuota{Provider: "codex", Windows: []QuotaWindow{{Name: "5 hours", Used: 100, ResetsAt: &past}}}
	if UsedUp(renewed, now) || !BackAt(renewed, now).IsZero() {
		t.Fatalf("a window reset a minute ago stops the account: back %v", BackAt(renewed, now))
	}
	full := SubscriptionQuota{Provider: "codex", Windows: []QuotaWindow{
		{Name: "5 hours", Used: 100, ResetsAt: &past}, {Name: "7 days", Used: 100, ResetsAt: &future},
	}}
	if !UsedUp(full, now) || !BackAt(full, now).Equal(future) {
		t.Fatalf("a full week doesn't stop it: back %v", BackAt(full, now))
	}
	// read as it was, before it reset, it was used up (quota_last's holds)
	if !usedUp(renewed, time.Time{}) {
		t.Fatal("the reading as read isn't used up")
	}
}
