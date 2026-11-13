package provider

import (
	"encoding/json"
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
