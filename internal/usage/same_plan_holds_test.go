package usage

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// samePlanLogins writes logins.json in the test's home: each entry is an
// agent, a name and the ChatGPT account and user its tokens name ("" for
// a token that names none).
func samePlanLogins(t *testing.T, ls ...[4]string) {
	t.Helper()
	jwt := func(email, account, user string) string {
		auth := map[string]string{"chatgpt_plan_type": "plus"}
		if account != "" {
			auth["chatgpt_account_id"] = account
		}
		if user != "" {
			auth["chatgpt_user_id"] = user
		}
		b, _ := json.Marshal(map[string]any{"sub": "auth0|" + user, "email": email, "https://api.openai.com/auth": auth})
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
	}
	var out []map[string]any
	for _, l := range ls {
		agent, name, account, user := l[0], l[1], l[2], l[3]
		tok := jwt(name, account, user)
		var auth map[string]any
		switch agent {
		case "codex": // auth.json as `codex login` writes it
			auth = map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"id_token": tok, "access_token": tok, "refresh_token": "r", "account_id": account}}
		case provider.ChatGPTAPIID: // what Sign in with ChatGPT keeps
			auth = map[string]any{"clientId": "app_1", "hostId": "urn:uuid:1", "sub": "auth0|" + user, "email": name, "idToken": tok, "access": tok, "refresh": "r", "expires": time.Now().Add(time.Hour)}
		}
		out = append(out, map[string]any{"agent": agent, "user": name, "auth": auth})
	}
	b, _ := json.Marshal(out)
	path := filepath.Join(filepath.Dir(provider.Path()), "logins.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// jctaoo on Discord: a ChatGPT account signed in through Sign in with
// ChatGPT (chatgpt-api) and to Codex draws on one plan's five hours and
// week, which only the Codex card reads. The card's whole-window estimate
// counts the chatgpt-api account's calls too, and only when the tokens
// say it is the same ChatGPT account: two Team seats of one email share a
// name and a user, never an account id.
func TestWindowHoldsCountsTheSameChatGPTAccountThroughTheAPI(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	reset := now.Add(3 * time.Hour)
	card := func(user string) provider.SubscriptionQuota {
		return provider.SubscriptionQuota{Provider: "codex", User: user, Windows: []provider.QuotaWindow{
			{Name: "5 hours", Used: 40, ResetsAt: &reset, Span: 5 * time.Hour}}}
	}
	call := func(prov, who string, tokens int) Record {
		return Record{Time: now.Add(-time.Hour), Agent: "codex", Provider: prov, ProviderAccount: who, Model: "gpt-5.5", Input: tokens, Output: 0, Status: 200}
	}
	holds := func(qs []provider.SubscriptionQuota, recs ...Record) []provider.SubscriptionQuota {
		return windowHolds(qs, now, now.Add(-30*24*time.Hour), func(Record) *catalog.Price { return nil }, provider.Renames{},
			provider.SamePlanAccounts(), nil, func(fn func(Record)) {
				for _, r := range recs {
					fn(r)
				}
			})
	}
	tokens := func(q provider.SubscriptionQuota) int64 {
		if h := q.Windows[0].Holds; h != nil {
			return h.Tokens
		}
		return 0
	}

	t.Run("one account, both ways in", func(t *testing.T) {
		holdsHome(t)
		samePlanLogins(t,
			[4]string{"codex", "dee@example.com", "acct-1", "user-1"},
			[4]string{provider.ChatGPTAPIID, "dee@example.com", "acct-1", "user-1"})
		got := holds([]provider.SubscriptionQuota{card("dee@example.com")},
			call("codex", "dee@example.com", 1000), call(provider.ChatGPTAPIID, "dee@example.com", 3000))
		// 4000 routed at 40% used: 10000 the whole window
		if n := tokens(got[0]); n != 10000 {
			t.Fatalf("whole 5 hours %d tokens, want 10000 (codex's 1000 and chatgpt-api's 3000 over 40%%)", n)
		}
	})

	t.Run("two seats of one email stay apart", func(t *testing.T) {
		holdsHome(t)
		samePlanLogins(t,
			[4]string{"codex", "dee@example.com · Team", "acct-team", "user-1"},
			[4]string{"codex", "dee@example.com", "acct-own", "user-1"},
			[4]string{provider.ChatGPTAPIID, "dee@example.com", "acct-own", "user-1"})
		got := holds([]provider.SubscriptionQuota{card("dee@example.com · Team"), card("dee@example.com")},
			call("codex", "dee@example.com · Team", 2000), call("codex", "dee@example.com", 1000),
			call(provider.ChatGPTAPIID, "dee@example.com", 3000))
		if n := tokens(got[0]); n != 5000 {
			t.Fatalf("the Team seat holds %d tokens, want 5000: its own 2000 only, not the personal account's chatgpt-api calls", n)
		}
		if n := tokens(got[1]); n != 10000 {
			t.Fatalf("the personal account holds %d tokens, want 10000 with its chatgpt-api calls", n)
		}
	})

	t.Run("a token that names no account matches none", func(t *testing.T) {
		holdsHome(t)
		samePlanLogins(t,
			[4]string{"codex", "dee@example.com", "acct-1", "user-1"},
			[4]string{provider.ChatGPTAPIID, "dee@example.com", "", "user-1"})
		got := holds([]provider.SubscriptionQuota{card("dee@example.com")},
			call("codex", "dee@example.com", 1000), call(provider.ChatGPTAPIID, "dee@example.com", 3000))
		if n := tokens(got[0]); n != 2500 {
			t.Fatalf("whole 5 hours %d tokens, want 2500: an account the tokens don't name isn't taken for the same by its email", n)
		}
	})

	t.Run("chatgpt-api alone", func(t *testing.T) {
		holdsHome(t)
		samePlanLogins(t, [4]string{provider.ChatGPTAPIID, "dee@example.com", "acct-1", "user-1"})
		if m := provider.SamePlanAccounts(); len(m) != 0 {
			t.Fatalf("matched with no Codex account: %v", m)
		}
		// its card reads no windows (OpenAI's API tells none): nothing to reckon
		api := provider.SubscriptionQuota{Provider: provider.ChatGPTAPIID, User: "dee@example.com", Windows: []provider.QuotaWindow{}, Error: "no usage"}
		got := holds([]provider.SubscriptionQuota{api}, call(provider.ChatGPTAPIID, "dee@example.com", 3000))
		if len(got) != 1 || len(got[0].Windows) != 0 {
			t.Fatalf("got %+v", got)
		}
	})
}
