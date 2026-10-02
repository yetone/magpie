package provider

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const subscriptionNotice = "You are currently using your subscription to power your Claude Code usage"

// Exercise the account cards through LoginUsage, with only a fake CLI and
// a quota snapshot in the test's home. No request reaches a real account.
func claudeUsageCards(t *testing.T, text string, snapshot *lastQuota) (*atomic.Value, func() SubscriptionQuota) {
	t.Helper()
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{
		"oauthAccount": map[string]any{"emailAddress": "a@example.com"},
	})
	rememberLogins(true)
	var out atomic.Value
	out.Store(text)
	fakeClaudeUsage(t, &out, nil)
	lastQuotas.Lock()
	lastQuotas.m, lastQuotas.loaded = nil, false
	lastQuotas.Unlock()
	loginUsageCache.Lock()
	loginUsageCache.m = nil
	loginUsageCache.Unlock()
	t.Cleanup(func() {
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
		loginUsageCache.Lock()
		loginUsageCache.m = nil
		loginUsageCache.Unlock()
	})
	if snapshot != nil {
		user := snapshot.Q.User
		if user == "" {
			user = "a@example.com"
		}
		writeFile(t, lastQuotasPath(), map[string]lastQuota{snapshot.Q.Provider + "/" + strings.ToLower(user): *snapshot})
	}
	return &out, func() SubscriptionQuota {
		AskClaudeUsage()
		cards := LoginUsage(context.Background(), "claude")
		q, ok := cards["a@example.com"]
		if !ok {
			t.Fatalf("no card for the signed-in account: %+v", cards)
		}
		return q
	}
}

// Advance the retry clock without waiting or changing any reading.
func retryClaudeUsage() {
	claudeUsage.Lock()
	defer claudeUsage.Unlock()
	for k, e := range claudeUsage.m {
		e.tried = time.Now().Add(-time.Minute)
		claudeUsage.m[k] = e
	}
}

func TestClaudeUsageUnavailableKeepsAccountReading(t *testing.T) {
	at := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	reset := time.Now().Add(time.Hour).Truncate(time.Second)
	snapshot := lastQuota{At: at, Q: SubscriptionQuota{Provider: "claude", Plan: "max", Windows: []QuotaWindow{
		{Name: "5 hours", Used: 40, ResetsAt: &reset},
	}}}
	for _, text := range []string{subscriptionNotice, "", subscriptionNotice + ".", "\x1b[32m" + subscriptionNotice + "\x1b[0m", subscriptionNotice + "\nWhat's contributing to your limits usage?"} {
		t.Run(text, func(t *testing.T) {
			_, card := claudeUsageCards(t, text, &snapshot)
			for range 2 { // cached failure must not turn an old reading into a new one
				q := card()
				if q.Error != "" || q.AsOf == nil || !q.AsOf.Equal(at) || len(q.Windows) != 1 || q.Windows[0].Used != 40 || !q.Windows[0].ResetsAt.Equal(reset) {
					t.Fatalf("usage notice should keep this account's dated reading: %+v", q)
				}
			}
		})
	}
}

func TestClaudeUsageRecentObservationSurvivesTemporaryRead(t *testing.T) {
	_, card := claudeUsageCards(t, subscriptionNotice, nil)
	NoteClaudeLimits("a@example.com", []ClaudeLimit{{Kind: "five_hour", Used: .42}})
	for range 2 {
		q := card()
		if q.Error != "" || len(q.Windows) != 1 || q.Windows[0].Used != 42 {
			t.Fatalf("recent observation must survive a repeated temporary failure: %+v", q)
		}
	}
}

func TestClaudeUsageUnavailableWithoutAccountReading(t *testing.T) {
	for _, tt := range []struct {
		name     string
		snapshot *lastQuota
	}{
		{"no cache", nil},
		{"another account", &lastQuota{At: time.Now(), Q: SubscriptionQuota{Provider: "claude", User: "b@example.com", Windows: []QuotaWindow{{Name: "5 hours", Used: 70}}}}},
		{"another provider", &lastQuota{At: time.Now(), Q: SubscriptionQuota{Provider: "codex", User: "a@example.com", Windows: []QuotaWindow{{Name: "5 hours", Used: 70}}}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, card := claudeUsageCards(t, subscriptionNotice, tt.snapshot)
			q := card()
			if q.Error == "" || q.AsOf != nil || len(q.Windows) != 0 {
				t.Fatalf("no reading for this account: %+v", q)
			}
		})
	}
}

func TestClaudeUsageUnavailablePreservesExpiredReading(t *testing.T) {
	at := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	reset := time.Now().Add(-time.Hour).Truncate(time.Second)
	snapshot := lastQuota{At: at, Q: SubscriptionQuota{Provider: "claude", Windows: []QuotaWindow{{Name: "5 hours", Used: 100, ResetsAt: &reset}}}}
	_, card := claudeUsageCards(t, subscriptionNotice, &snapshot)
	q := card()
	if q.Error != "" || q.AsOf == nil || !q.AsOf.Equal(at) || len(q.Windows) != 1 || q.Windows[0].Used != 100 || q.Windows[0].ResetsAt == nil || !q.Windows[0].ResetsAt.Equal(reset) {
		t.Fatalf("expired reading must not become a current zero: %+v", q)
	}
}

func TestClaudeUsageExpiredReadingDoesNotSwitchAccounts(t *testing.T) {
	past, future := time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour)
	for _, tt := range []struct {
		name       string
		windows    []QuotaWindow
		back, move bool
	}{
		{"only expired", []QuotaWindow{{Name: "5 hours", Used: 100, ResetsAt: &past}}, false, false},
		{"return to account with room", []QuotaWindow{{Name: "5 hours", Used: 100, ResetsAt: &past}}, true, true},
		{"weekly still spent", []QuotaWindow{{Name: "5 hours", Used: 100, ResetsAt: &past}, {Name: "7 days", Used: 99, ResetsAt: &future}}, false, true},
		{"session still spent", []QuotaWindow{{Name: "5 hours", Used: 99, ResetsAt: &future}, {Name: "7 days", Used: 100, ResetsAt: &past}}, false, true},
		{"remaining window has room", []QuotaWindow{{Name: "5 hours", Used: 100, ResetsAt: &past}, {Name: "7 days", Used: 20, ResetsAt: &future}}, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := lastQuota{At: time.Now().Add(-2 * time.Hour), Q: SubscriptionQuota{Provider: "claude", Windows: tt.windows}}
			_, card := claudeUsageCards(t, subscriptionNotice, &snapshot)
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
			if tt.back {
				setLoginReturn("claude", loginReturn{Back: "b@example.com", To: "a@example.com"})
			}
			NoteClaudeLimits("b@example.com", []ClaudeLimit{{Kind: "five_hour", Used: .1, ResetsAt: future.Unix()}})
			AskClaudeUsage()
			from, to, back, ok := NextLogin(context.Background(), "claude")
			if ok != tt.move || tt.move && (from != "a@example.com" || to != "b@example.com" || back != tt.back) {
				t.Fatalf("move=%v back=%v: got %q→%q back=%v ok=%v", tt.move, tt.back, from, to, back, ok)
			}
			// Choosing an account must not erase the snapshot used for display.
			q := card()
			if q.AsOf == nil || len(q.Windows) != len(tt.windows) || q.Windows[0].Used != tt.windows[0].Used || !q.Windows[0].ResetsAt.Equal(*tt.windows[0].ResetsAt) {
				t.Fatalf("account selection changed the displayed reading: %+v", q)
			}
		})
	}
}

func TestClaudeUsageFailureDoesNotHideAccountErrors(t *testing.T) {
	for _, text := range []string{
		"Error: not logged in",
		"Claude Code: HTTP 401 Unauthorized",
		"Claude Code: HTTP 403 Forbidden",
		"Claude Code: HTTP 401 Unauthorized after a timeout",
		"Claude Code: HTTP 403 Forbidden: rate_limit_error",
		"Claude Code: network timeout\nHTTP 401 Unauthorized",
		"You've hit your session limit · resets 3:30am (UTC)",
		subscriptionNotice + "\nAuthentication failed",
		"\x1b[32m" + subscriptionNotice + ".\x1b[0m\n\x1b[31mAuthentication failed\x1b[0m",
		subscriptionNotice + ".\nHTTP 401 Unauthorized",
		"Current session: 40% used\nHTTP 401 Unauthorized",
		"You've hit your limit · resets 3:30am (UTC)",
		"You are currently using your overages to power your Claude Code usage. We will automatically switch you back to your subscription rate limits when they reset",
	} {
		t.Run(text, func(t *testing.T) {
			out, card := claudeUsageCards(t, "Current session: 40% used", nil)
			if q := card(); q.Error != "" || len(q.Windows) != 1 {
				t.Fatalf("first reading: %+v", q)
			}
			NoteClaudeLimits("a@example.com", []ClaudeLimit{{Kind: "five_hour", Used: .9}})
			out.Store(text)
			retryClaudeUsage()
			for range 2 {
				q := card()
				if q.Error == "" || q.AsOf != nil || len(q.Windows) != 0 {
					t.Fatalf("the account error must remain visible: %+v", q)
				}
			}
		})
	}
}

func TestClaudeUsageUnavailableAfterAccountSwitch(t *testing.T) {
	out, card := claudeUsageCards(t, "Current session: 40% used", nil)
	if q := card(); q.Error != "" || len(q.Windows) != 1 {
		t.Fatalf("first account: %+v", q)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{
		"oauthAccount": map[string]any{"emailAddress": "b@example.com"},
	})
	forgetClaudeStatus()
	rememberLogins(true)
	out.Store(subscriptionNotice)
	AskClaudeUsage()
	q, ok := LoginUsage(context.Background(), "claude")["b@example.com"]
	if !ok || q.Error == "" || q.AsOf != nil || len(q.Windows) != 0 {
		t.Fatalf("switched account must not inherit the old account's allowance: %+v", q)
	}
}

func TestQuotaReportKeepsReadingTime(t *testing.T) {
	claudeHome(t)
	at := time.Now().Add(-time.Hour)
	c := &subscriptionUsageCache
	c.Lock()
	oldAt, oldData, oldAsked := c.at, c.data, c.asked
	c.at, c.asked = time.Now(), false
	c.data = []SubscriptionQuota{{Provider: "claude", AsOf: &at, Windows: []QuotaWindow{{Name: "5 hours", Used: 100}}}}
	c.Unlock()
	t.Cleanup(func() {
		c.Lock()
		c.at, c.data, c.asked = oldAt, oldData, oldAsked
		c.Unlock()
	})
	for _, q := range QuotaReport(context.Background(), time.Now()) {
		if q.Provider == "claude" {
			// JSON is the public report's contract, including the reading time.
			b, err := json.Marshal(q)
			if err != nil || !strings.Contains(string(b), `"asOf":"`+at.Format(time.RFC3339Nano)+`"`) {
				t.Fatalf("report lost the snapshot's time: %s (%v)", b, err)
			}
			return
		}
	}
	t.Fatal("report has no Claude card")
}

func TestClaudeUsageUnavailableRecovers(t *testing.T) {
	at := time.Now().Add(-time.Hour)
	snapshot := lastQuota{At: at, Q: SubscriptionQuota{Provider: "claude", Windows: []QuotaWindow{{Name: "5 hours", Used: 40}}}}
	out, card := claudeUsageCards(t, subscriptionNotice, &snapshot)
	if q := card(); q.AsOf == nil {
		t.Fatalf("first reading should be dated: %+v", q)
	}
	out.Store("Current session: 25% used")
	retryClaudeUsage()
	if q := card(); q.Error != "" || q.AsOf != nil || len(q.Windows) != 1 || q.Windows[0].Used != 25 {
		t.Fatalf("new reading should replace the snapshot: %+v", q)
	}
	out.Store(subscriptionNotice)
	retryClaudeUsage()
	if q := card(); q.Error != "" || q.AsOf == nil || !q.AsOf.After(at) || len(q.Windows) != 1 || q.Windows[0].Used != 25 {
		t.Fatalf("next failure should keep the recovered reading: %+v", q)
	}
}
