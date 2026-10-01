package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Codex signed in to an account out of its allowance is signed in to the
// next account that is on and has room; while none has, it stays.
func TestCodexSwitchedWhenUsedUp(t *testing.T) {
	home := signIn(t) // me@example.com, acct-1
	rememberLogins(true)
	codexSignIn(t, home, "spare@example.com", "r-spare")
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	used := map[string]float64{"acct-work@example.com": 100, "acct-1": 100, "acct-spare@example.com": 20}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"plan_type": "plus", "rate_limit": map[string]any{
			"primary_window": map[string]any{"used_percent": used[r.Header.Get("chatgpt-account-id")], "limit_window_seconds": 18000}}})
	}))
	defer fake.Close()
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })
	fresh := func() {
		loginUsageCache.Lock()
		loginUsageCache.m = nil
		loginUsageCache.Unlock()
	}
	switched := func() string {
		t.Helper()
		fresh()
		to, err := SwitchWhenSpent(context.Background(), "codex")
		if err != nil {
			t.Fatal(err)
		}
		return to
	}

	// no other account on: nothing to go to
	if to := switched(); to != "" {
		t.Fatalf("switched to %s with none on", to)
	}
	// the one on is out too
	if err := SetLoginOn("codex", "me@example.com", true); err != nil {
		t.Fatal(err)
	}
	if to := switched(); to != "" {
		t.Fatalf("switched to %s, out as well", to)
	}
	// one on with room: the first of those, not one that is off
	if err := SetLoginOn("codex", "spare@example.com", true); err != nil {
		t.Fatal(err)
	}
	if to := switched(); to != "spare@example.com" {
		t.Fatalf("switched to %q", to)
	}
	var live codexAuth
	readJSON(filepath.Join(home, ".codex", "auth.json"), &live)
	if live.Tokens.RefreshToken != "r-spare" {
		t.Fatalf("Codex is signed in with %+v", live.Tokens)
	}
	for _, l := range Logins("codex") {
		if l.User == "work@example.com" && (!l.On || l.Active) || l.User == "spare@example.com" && !l.Active {
			t.Fatalf("after: %+v", l)
		}
	}
	// on one with room it stays
	if to := switched(); to != "" {
		t.Fatalf("switched again, to %s", to)
	}
	// an allowance not known is not taken for used up
	used["acct-spare@example.com"] = 100
	used["acct-work@example.com"] = 0
	CodexBase = "http://127.0.0.1:1/backend-api/codex"
	if to := switched(); to != "" {
		t.Fatalf("switched to %s, not knowing", to)
	}
}

// Claude Code signed in to an account Smart counts spent (98%) is signed
// in to the next account that is on and has room — its credentials and
// .claude.json's account both — and not before (#208, #209). What it
// goes by is what Claude Code told as it answered: Anthropic's usage
// endpoint isn't read for it.
func TestClaudeSwitchedWhenSpent(t *testing.T) {
	home := claudeHome(t)
	cred := claudeSignIn(t, home, time.Now().Add(time.Hour)) // sk-ant-oat01-old
	profile := filepath.Join(home, ".claude.json")
	writeFile(t, profile, map[string]any{"oauthAccount": map[string]any{"emailAddress": "a@example.com", "accountUuid": "u-a"}})
	rememberLogins(true)
	loginsMu.Lock()
	ls := upsertLogin(readLogins(), savedLogin{Agent: "claude", User: "b@example.com", Plan: "max", On: true, Seen: time.Now().UTC(),
		Auth: mustJSONRaw(t, map[string]any{"claudeAiOauth": map[string]any{"accessToken": "sk-ant-oat01-b", "refreshToken": "sk-ant-ort01-b",
			"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": "max", "scopes": []string{"user:inference", "user:profile"}}}),
		Profile: mustJSONRaw(t, map[string]any{"emailAddress": "b@example.com", "accountUuid": "u-b"})})
	if err := writeLogins(ls); err != nil {
		t.Fatal(err)
	}
	loginsMu.Unlock()

	used := map[string]float64{"a@example.com": 97, "b@example.com": 10}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("usage endpoint read: %s", r.URL)
	}))
	defer fake.Close()
	claudeBase = fake.URL // isolate puts it back
	switched := func() string {
		t.Helper()
		loginUsageCache.Lock()
		loginUsageCache.m = nil
		loginUsageCache.Unlock()
		claudeUsage.Lock()
		claudeUsage.m = nil
		claudeUsage.Unlock()
		for user, u := range used {
			NoteClaudeLimits(user, []ClaudeLimit{{Kind: "five_hour", Used: u / 100, ResetsAt: time.Now().Add(time.Hour).Unix()}})
		}
		to, err := SwitchWhenSpent(context.Background(), "claude")
		if err != nil {
			t.Fatal(err)
		}
		return to
	}

	// 97%: Smart still counts it low, not spent
	if to := switched(); to != "" {
		t.Fatalf("switched to %s at 97%%", to)
	}
	used["a@example.com"] = 98
	if to := switched(); to != "b@example.com" {
		t.Fatalf("switched to %q at 98%%", to)
	}
	var c map[string]any
	readJSON(cred, &c)
	if o, _ := c["claudeAiOauth"].(map[string]any); o["refreshToken"] != "sk-ant-ort01-b" {
		t.Fatalf("Claude Code is signed in with %v", o)
	}
	b, _ := os.ReadFile(profile)
	if !strings.Contains(string(b), "b@example.com") {
		t.Fatalf(".claude.json: %s", b)
	}
	for _, l := range Logins("claude") {
		if l.User == "a@example.com" && (!l.On || l.Active) || l.User == "b@example.com" && !l.Active {
			t.Fatalf("after: %+v", l)
		}
	}
	// on one with room it stays; with none left, too
	if to := switched(); to != "" {
		t.Fatalf("switched again, to %s", to)
	}
	used["b@example.com"] = 99
	if to := switched(); to != "" {
		t.Fatalf("switched to %s, spent as well", to)
	}
}

func mustJSONRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
