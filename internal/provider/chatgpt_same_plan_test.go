package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// tmr11235 on X: a routing group of two ChatGPT accounts, each added both
// as Codex and through Sign in with ChatGPT (chatgpt-api). The Codex rows
// read their week and reset; the chatgpt-api rows, the same accounts, read
// "what's left not known yet" and Smart put them after every known one.
// Routing now weighs a chatgpt-api account by the same ChatGPT account's
// Codex reading, matched on the tokens' account and user ids as
// SamePlanAccounts matches them, never on the name.
func TestChatGPTAPIAllowanceIsTheSameAccountsCodexReading(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/.config")
	t.Setenv("XDG_CACHE_HOME", home+"/.cache")

	tok := func(email, account, user string) string {
		return fakeJWT(map[string]any{"email": email, "https://api.openai.com/auth": map[string]any{
			"chatgpt_plan_type": "pro", "chatgpt_account_id": account, "chatgpt_user_id": user}})
	}
	codex := func(name, account, user string) savedLogin {
		jwt := tok(name, account, user)
		b, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"id_token": jwt, "access_token": jwt, "refresh_token": "r", "account_id": account}})
		return savedLogin{Agent: "codex", User: name, Auth: b}
	}
	api := func(name, account, user string) savedLogin {
		jwt := tok(name, account, user)
		b, _ := json.Marshal(map[string]any{"clientId": "app_1", "hostId": "urn:uuid:1", "sub": "auth0|" + user, "email": name, "idToken": jwt, "access": jwt, "refresh": "r", "expires": time.Now().Add(time.Hour)})
		return savedLogin{Agent: ChatGPTAPIID, User: name, Auth: b}
	}
	saveLogins(t,
		codex("Dee@Example.com", "acct-1", "user-1"), api("dee@example.com", "acct-1", "user-1"),
		// two seats of one email: the Team seat's Codex reading is not the
		// personal account's
		codex("team@example.com · Team", "acct-team", "user-2"), api("team@example.com", "acct-own", "user-2"),
		// no Codex account of its own: still not known
		api("solo@example.com", "acct-3", "user-3"))

	week := Allowance{{Used: 40, Span: 7 * 24 * time.Hour}}
	team := Allowance{{Used: 90, Span: 7 * 24 * time.Hour}}
	usedCache.Lock()
	usedCache.m, usedCache.at, usedCache.loading = map[string]map[string]Allowance{}, map[string]time.Time{}, map[string]chan struct{}{}
	usedCache.stale = nil
	usedCache.m["codex"] = map[string]Allowance{"Dee@Example.com": week, "team@example.com · Team": team}
	usedCache.m[ChatGPTAPIID] = map[string]Allowance{}
	usedCache.at["codex"], usedCache.at[ChatGPTAPIID] = time.Now(), time.Now()
	handed := usedCache.m[ChatGPTAPIID]
	usedCache.Unlock()
	t.Cleanup(func() {
		usedCache.Lock()
		usedCache.m, usedCache.at, usedCache.loading = map[string]map[string]Allowance{}, map[string]time.Time{}, map[string]chan struct{}{}
		usedCache.stale = nil
		usedCache.Unlock()
	})

	got := Allowances(ChatGPTAPIID)
	if a, ok := got["dee@example.com"]; !ok || len(a) != 1 || a[0].Used != 40 {
		t.Fatalf("the chatgpt-api account reads %v (known %v), want its Codex account's week at 40%%", a, ok)
	}
	if a, ok := got["team@example.com"]; ok {
		t.Fatalf("a personal account took the Team seat's reading of one email: %v", a)
	}
	if a, ok := got["solo@example.com"]; ok {
		t.Fatalf("an account with no Codex sign-in is known: %v", a)
	}
	if len(handed) != 0 {
		t.Fatalf("the cached chatgpt-api readings were changed: %v", handed)
	}
	if _, ok := Allowances("codex")["dee@example.com"]; ok {
		t.Fatal("codex's own readings took the chatgpt-api name")
	}
}

// tmr11235 on X: an account signed in both to Codex and through Sign in
// with ChatGPT, only for the Codex row to show its five hours and week.
// The ChatGPT API account's row (LoginUsageShown, the editor's list and
// `magpie accounts`) now shows the same ChatGPT account's Codex reading,
// from Codex's own cache: matched on the tokens' account and user ids,
// never the name, so the Team seat of one email shows nothing of the
// personal account's, and one with no Codex sign-in says what it said.
func TestChatGPTAPIRowShowsTheSameAccountsCodexWindows(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/.config")
	t.Setenv("XDG_CACHE_HOME", home+"/.cache")
	t.Setenv("CODEX_HOME", home+"/.codex")

	tok := func(email, account, user string) string {
		return fakeJWT(map[string]any{"email": email, "https://api.openai.com/auth": map[string]any{
			"chatgpt_plan_type": "plus", "chatgpt_account_id": account, "chatgpt_user_id": user}})
	}
	codex := func(name, account, user string) savedLogin {
		jwt := tok(name, account, user)
		b, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"id_token": jwt, "access_token": jwt, "refresh_token": "r", "account_id": account}})
		return savedLogin{Agent: "codex", User: name, Auth: b}
	}
	api := func(name, account, user string) savedLogin {
		jwt := tok(name, account, user)
		b, _ := json.Marshal(map[string]any{"clientId": "app_1", "hostId": "urn:uuid:1", "sub": "auth0|" + user, "email": name, "idToken": jwt, "access": jwt, "refresh": "r", "expires": time.Now().Add(time.Hour)})
		return savedLogin{Agent: ChatGPTAPIID, User: name, Plan: "plus", Auth: b}
	}
	saveLogins(t,
		codex("Dee@Example.com", "acct-1", "user-1"), api("dee@example.com", "acct-1", "user-1"),
		codex("team@example.com · Team", "acct-team", "user-2"), api("team@example.com", "acct-own", "user-2"),
		api("solo@example.com", "acct-3", "user-3"))

	// the Codex sign-ins' app lists, asked for the two with no twin by
	// their tokens: neither holds app_1
	apps := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage/chatpass/apps" {
			t.Errorf("asked %s: the Codex rows' readings are cached", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"items":[]}`))
	}))
	t.Cleanup(apps.Close)
	oldBase := CodexBase
	CodexBase = apps.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = oldBase })

	reset := time.Now().Add(3 * time.Hour)
	dee := SubscriptionQuota{Provider: "codex", Name: "Codex", User: "Dee@Example.com", Plan: "plus",
		Windows: []QuotaWindow{{Name: "5 hours", Used: 30, ResetsAt: &reset}, {Name: "Weekly", Used: 40}},
		Resets:  &ResetCredits{Count: 1}, Held: true}
	team := SubscriptionQuota{Provider: "codex", User: "team@example.com · Team", Windows: []QuotaWindow{{Name: "Weekly", Used: 90}}}
	loginUsageCache.Lock()
	old := loginUsageCache.m
	loginUsageCache.m = map[string]loginUsageEntry{
		"codex/dee@example.com":         {at: time.Now(), q: dee, read: dee},
		"codex/team@example.com · team": {at: time.Now(), q: team, read: team},
	}
	loginUsageCache.Unlock()
	t.Cleanup(func() {
		loginUsageCache.Lock()
		loginUsageCache.m = old
		loginUsageCache.Unlock()
	})

	got := LoginUsageShown(context.Background(), ChatGPTAPIID)
	q := got["dee@example.com"]
	if q.Error != "" || len(q.Windows) != 2 || q.Windows[0].Used != 30 || q.Windows[1].Used != 40 {
		t.Fatalf("the chatgpt-api row reads %+v, want its Codex account's 5 hours at 30%% and week at 40%%", q)
	}
	if q.SamePlan != "Dee@Example.com" || q.Provider != ChatGPTAPIID || q.SeeAt != "" {
		t.Fatalf("the row is told as %q of %q (seeAt %q), want the Codex account named on chatgpt-api's row", q.SamePlan, q.Provider, q.SeeAt)
	}
	if q.Resets != nil || q.Held {
		t.Fatalf("Codex's own resets or hold went on the chatgpt-api row: %+v", q)
	}
	if q := got["team@example.com"]; q.Error != siwcNoUsage || len(q.Windows) != 0 || q.SamePlan != "" {
		t.Fatalf("a personal account took the Team seat's reading of one email: %+v", q)
	}
	if q := got["solo@example.com"]; q.Error != siwcNoUsage || len(q.Windows) != 0 {
		t.Fatalf("an account with no Codex sign-in reads %+v", q)
	}
	// Codex's own row is as it was
	if c := LoginUsageShown(context.Background(), "codex")["Dee@Example.com"]; c.SamePlan != "" || c.Resets == nil {
		t.Fatalf("the Codex row changed: %+v", c)
	}
	loginUsageCache.Lock()
	cached := loginUsageCache.m["codex/dee@example.com"]
	loginUsageCache.Unlock()
	if cached.q.SamePlan != "" || cached.q.Provider != "codex" {
		t.Fatalf("the cached Codex reading was changed: %+v", cached.q)
	}
}
