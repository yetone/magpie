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

// #1453 (wlj521): Cockpit Tools' exports come in. Its Sub2API export holds
// its accounts under credentials; an API key, another vendor's account and
// an access token with nothing to renew it are each refused, said why; two
// Team seats of one email stay two accounts; a file among several that
// holds no account is listed by its place, never passed over. (That the
// files are only read is TestAccountsImport's, in package main.)
func TestImportCockpitExports(t *testing.T) {
	claudeHome(t)
	seats := map[string][2]string{
		"r-sub": {"sub@example.com", "acct-sub"}, "r-cockpit": {"cockpit@example.com", "acct-cockpit"},
		"r-seat1": {"team@example.com", "acct-t1"}, "r-seat2": {"team@example.com", "acct-t2"},
	}
	var mu sync.Mutex
	asked := map[string]int{}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		rt := body["refresh_token"]
		mu.Lock()
		asked[rt]++
		mu.Unlock()
		who, ok := seats[rt]
		if !ok {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id_token": fakeJWT(map[string]any{"email": who[0],
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "team", "chatgpt_account_id": who[1]}}),
			"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": "n" + rt,
		})
	}))
	defer fake.Close()
	t.Cleanup(CodexTokenURLForTest(fake.URL))

	sub2api := `{"exported_at":"2026-10-09T00:00:00Z","proxies":[],"type":"sub2api-data","version":1,"accounts":[
		{"name":"sub@example.com","platform":"openai","type":"oauth","credentials":{"access_token":"a","refresh_token":"r-sub","id_token":"x","chatgpt_account_id":"acct-sub"}},
		{"name":"key","platform":"openai","type":"apikey","credentials":{"api_key":"sk-1"}},
		{"name":"c@example.com","platform":"anthropic","type":"oauth","credentials":{"access_token":"a","refresh_token":"r-c"}},
		{"name":"web@example.com","platform":"openai","type":"oauth","credentials":{"access_token":"a-only"}}]}`
	cockpit := `[{"id":"1","type":"codex","email":"cockpit@example.com","id_token":"x","access_token":"y","refresh_token":"r-cockpit","account_id":"acct-cockpit"},
		{"id":"2","type":"codex","email":"session@example.com","access_token":"y"}]`
	seat1 := `{"OPENAI_API_KEY":null,"tokens":{"id_token":"x","access_token":"y","refresh_token":"r-seat1","account_id":"acct-t1"},"type":"codex"}`
	seat2 := `{"OPENAI_API_KEY":null,"tokens":{"id_token":"x","access_token":"y","refresh_token":"r-seat2","account_id":"acct-t2"},"type":"codex"}`
	rs, err := ImportLogins(context.Background(), "codex", []string{sub2api, "{not json", cockpit, seat1, seat2})
	if err != nil {
		t.Fatal(err)
	}
	want := "sub@example.com · Team:added,#2:failed,c@example.com:failed,web@example.com:failed,:failed,cockpit@example.com · Team:added,session@example.com:failed,team@example.com · Team:added,team@example.com · Team:added"
	if got := importStatuses(rs); got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
	for i, frag := range map[int]string{1: "API key", 2: "anthropic account, not Codex's", 3: "only an access token", 6: "only an access token"} {
		if !strings.Contains(rs[i].Error, frag) {
			t.Errorf("row %d: %q, want %q", i, rs[i].Error, frag)
		}
	}
	if rs[4].File != 2 || rs[4].Error == "" {
		t.Errorf("the garbage file: %+v", rs[4])
	}
	for i, r := range rs {
		if i != 4 && r.File != 0 {
			t.Errorf("row %d names a file: %+v", i, r)
		}
	}
	if asked["r-c"] != 0 || asked["r-sub"] != 1 {
		t.Errorf("asked %v", asked)
	}
	// the two seats are kept apart, by workspace, not merged by email
	loginsMu.Lock()
	ls := readLogins()
	loginsMu.Unlock()
	ws := map[string]bool{}
	for _, l := range ls {
		if e, w := codexWho(l.Auth); e == "team@example.com" {
			ws[w] = true
		}
	}
	if !ws["acct-t1"] || !ws["acct-t2"] {
		t.Errorf("team seats kept: %v (%d logins)", ws, len(ls))
	}
}

// liukaybee on X: the token `claude setup-token` prints, pasted into the
// Claude import, is added as a setup-token account (AddClaudeSetupToken),
// never taken for a refresh token; beside it in one paste a refresh token
// a line is read as before. Nothing is asked of Anthropic, the token is
// never said back, and a ChatGPT import says it is Claude's without
// sending it to ChatGPT.
func TestImportClaudeSetupToken(t *testing.T) {
	claudeHome(t)
	asked := 0
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		w.WriteHeader(500)
	}))
	defer fake.Close()
	oldBase, oldToken := claudeBase, codexTokenURL
	t.Cleanup(func() { claudeBase, codexTokenURL = oldBase, oldToken })
	claudeBase, codexTokenURL = fake.URL, fake.URL

	paste := "# from claude setup-token\nexport CLAUDE_CODE_OAUTH_TOKEN=\"" + testSetupToken + "\"\nsk-ant-ort01-bare\n" + testSetupToken + "\nsk-ant-api03-key\n"
	cpa := `{"type":"claude","access_token":"sk-ant-oat01-old","refresh_token":"sk-ant-ort01-cpa","email":"max@example.com"}`
	rs, err := ImportLogins(context.Background(), "claude", []string{cpa, paste})
	if err != nil {
		t.Fatal(err)
	}
	name := claudeTokenName(testSetupToken)
	if got := importStatuses(rs); got != "max@example.com:added,"+name+":added,#3:failed,"+name+":failed,#5:failed" || asked != 0 {
		t.Fatalf("got %s %+v, asked %d", got, rs, asked)
	}
	if !strings.Contains(rs[4].Error, "API key") {
		t.Fatalf("an API key pasted: %q", rs[4].Error)
	}
	for _, r := range rs {
		if strings.Contains(r.User+r.Error, "sk-ant-oat01-setup") {
			t.Fatalf("the token said back: %+v", r)
		}
	}
	if tok := ClaudeDirToken(claudeAccountDir(name)); tok != testSetupToken {
		t.Fatalf("the setup-token account's token %q", tok)
	}
	if !claudeTokenAccount(name) {
		t.Fatal("not kept as a setup-token account")
	}
	// the same token again is the account magpie has
	rs, err = ImportLogins(context.Background(), "claude", []string{testSetupToken})
	if err != nil || importStatuses(rs) != name+":exists" {
		t.Fatalf("again: %+v %v", rs, err)
	}

	rs, err = ImportLogins(context.Background(), "codex", []string{testSetupToken})
	if err != nil || len(rs) != 1 || rs[0].Status != "failed" || !strings.Contains(rs[0].Error, "Claude setup-token") || asked != 0 {
		t.Fatalf("a setup-token in the ChatGPT import: %+v %v, asked %d", rs, err, asked)
	}
}
