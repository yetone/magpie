package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func importStatuses(rs []ImportedAccount) string {
	var s []string
	for _, r := range rs {
		s = append(s, r.User+":"+r.Status)
	}
	return strings.Join(s, ",")
}

// CLIProxyAPI's Codex auth files and Codex CLI's own auth.json come in as
// ChatGPT accounts, each refreshed first; a token already held, another
// app's file and a used-up token are said so.
func TestImportCodexLogins(t *testing.T) {
	home := claudeHome(t)
	var mu sync.Mutex
	asked := map[string]int{}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		rt := body["refresh_token"]
		mu.Lock()
		asked[rt]++
		mu.Unlock()
		if body["client_id"] != codexClientID || body["grant_type"] != "refresh_token" || !strings.HasPrefix(rt, "r-") || rt == "r-used" {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
			return
		}
		who := strings.TrimPrefix(rt, "r-")
		json.NewEncoder(w).Encode(map[string]any{
			"id_token": fakeJWT(map[string]any{"email": who + "@example.com",
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro", "chatgpt_account_id": "acct-" + who}}),
			"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": "n-" + who,
		})
	}))
	defer fake.Close()
	old := codexTokenURL
	t.Cleanup(func() { codexTokenURL = old })
	codexTokenURL = fake.URL

	// signed in to Codex already
	codexSignIn(t, home, "live@example.com", "r-live")

	cpa := `{"type":"codex","id_token":"x","access_token":"y","refresh_token":"r-cpa","account_id":"acct-cpa","email":"cpa@example.com","expired":"2026-01-01T00:00:00Z"}`
	native := `{"OPENAI_API_KEY":null,"auth_mode":"chatgpt","tokens":{"id_token":"x","access_token":"y","refresh_token":"r-native","account_id":"acct-native"}}`
	list := `[{"type":"claude","refresh_token":"r-claude","email":"c@example.com"},
		{"type":"codex","refresh_token":"r-live","email":"live@example.com"},
		{"type":"codex","refresh_token":"r-used","email":"used@example.com"},
		{"type":"codex","refresh_token":"r-cpa","email":"cpa@example.com"}]`
	rs, err := ImportLogins(context.Background(), "codex", []string{cpa, native, list})
	if err != nil {
		t.Fatal(err)
	}
	want := "cpa@example.com:added,native@example.com:added,c@example.com:failed,live@example.com:exists,used@example.com:failed,cpa@example.com:failed"
	if got := importStatuses(rs); got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
	if !strings.Contains(rs[2].Error, "not Codex's") || !strings.Contains(rs[4].Error, "used since") || !strings.Contains(rs[5].Error, "twice") {
		t.Fatalf("errors %+v", rs)
	}
	if asked["r-live"] != 0 || asked["r-claude"] != 0 || asked["r-cpa"] != 1 {
		t.Fatalf("asked %v", asked)
	}
	for _, r := range rs {
		if strings.Contains(r.Error, "r-") || strings.Contains(r.User, "r-") {
			t.Fatalf("a token in the result: %+v", r)
		}
	}
	users, active := loginUsers(Logins("codex"))
	if active != "live@example.com" || len(users) != 3 {
		t.Fatalf("users %v active %q", users, active)
	}
	// the live account is left as it was; the new ones hold the new tokens
	var a codexAuth
	readJSON(filepath.Join(home, ".codex", "auth.json"), &a)
	if a.Tokens.RefreshToken != "r-live" {
		t.Fatalf("live auth.json changed: %s", a.Tokens.RefreshToken)
	}
	loginsMu.Lock()
	ls := readLogins()
	loginsMu.Unlock()
	for _, l := range ls {
		if l.User == "cpa@example.com" {
			if json.Unmarshal(l.Auth, &a) != nil || a.Tokens.RefreshToken != "n-cpa" || a.Tokens.AccountID != "acct-cpa" || a.AuthMode != "chatgpt" || !l.On || l.Plan != "pro" {
				t.Fatalf("kept %+v %+v", l, a)
			}
		}
	}

	// the same file again: the tokens it holds are spent, and said so
	rs, _ = ImportLogins(context.Background(), "codex", []string{`{"type":"codex","refresh_token":"n-cpa"}`})
	if importStatuses(rs) != "cpa@example.com:exists" {
		t.Fatalf("again %+v", rs)
	}
	if _, err := ImportLogins(context.Background(), "codex", []string{"{not json"}); err == nil {
		t.Fatal("garbage imported")
	}
}

// With no Claude sign-in, the first imported account becomes Claude Code's,
// kept as the file has it: nothing is asked of Anthropic to try it.
func TestImportClaudeLogins(t *testing.T) {
	claudeHome(t)
	asked := false
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = true
		w.WriteHeader(500)
	}))
	defer fake.Close()
	oldBase := claudeBase
	t.Cleanup(func() { claudeBase = oldBase })
	claudeBase = fake.URL

	cpa := `{"type":"claude","access_token":"sk-ant-oat01-old","refresh_token":"sk-ant-ort01-cpa","email":"max@example.com","expired":"2026-01-01T00:00:00Z"}`
	creds := `{"claudeAiOauth":{"accessToken":"a","refreshToken":"sk-ant-ort01-gone","expiresAt":1}}`
	rs, err := ImportLogins(context.Background(), "claude", []string{cpa + "\n", creds, `{"type":"codex","refresh_token":"r-x"}`})
	if err != nil {
		t.Fatal(err)
	}
	if got := importStatuses(rs); got != "max@example.com:added,#2:failed,#3:failed" || asked {
		t.Fatalf("got %s %+v asked Anthropic: %v", got, rs, asked)
	}
	c, _, ok := claudeCredential()
	if !ok || c.OAuth.RefreshToken != "sk-ant-ort01-cpa" || c.OAuth.AccessToken != "sk-ant-oat01-old" || len(c.OAuth.Scopes) == 0 {
		t.Fatalf("Claude Code's credentials %+v", c.OAuth)
	}
	if _, active := loginUsers(Logins("claude")); active != "max@example.com" {
		t.Fatalf("active %q", active)
	}
}
