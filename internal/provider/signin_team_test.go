package provider

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A Team seat on the email of a personal subscription is an account of its
// own: added beside the personal one, which stays signed in; and signing in
// brings back a Claude account that had been removed from magpie.
func TestClaudeTeamSeatBesidePersonal(t *testing.T) {
	home := claudeHome(t)
	noAnthropic(t)
	fakeClaudeLogin(t, fakeClaudeAccount{email: "same@example.com", org: "Acme", orgUUID: "o-team", plan: "team", refresh: "sk-ant-ort01-team"}, "", true)

	cred := claudeSignIn(t, home, time.Now().Add(time.Hour))
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{"oauthAccount": map[string]any{
		"emailAddress": "same@example.com", "organizationUuid": "o-personal", "organizationName": "same@example.com's Organization"}})
	if err := Delete("claude"); err != nil {
		t.Fatal(err)
	}
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("removed account still listed")
	}

	st, err := StartSignIn("claude")
	if err != nil {
		t.Fatal(err)
	}
	finishInBrowser(t, st, "the-code")
	st = waitDone(t, st.ID)
	if st.State != "done" || st.User != "same@example.com · Acme" || st.Plan != "team" || st.Using {
		t.Fatalf("state %+v", st)
	}
	users, active := loginUsers(Logins("claude"))
	if strings.Join(users, ",") != "same@example.com,same@example.com · Acme" || active != "same@example.com" {
		t.Fatalf("logins %v, active %q", users, active)
	}
	var c map[string]map[string]any
	readJSON(cred, &c)
	if c["claudeAiOauth"]["refreshToken"] == "sk-ant-ort01-team" {
		t.Fatal("the personal sign-in was replaced")
	}
	p, ok := find(All(), "claude")
	if !ok || len(p.AlsoOn()) != 1 || p.AlsoOn()[0].Account.User != "same@example.com · Acme" {
		t.Fatalf("account after sign-in: %v %+v", ok, p.AlsoOn())
	}
}

func TestSaveKeepsAccountIDs(t *testing.T) {
	claudeHome(t)
	if err := Save(Provider{Name: "Claude", Anthropic: "https://relay.example.com", Key: "k"}); err == nil {
		t.Fatal("a relay took the Claude subscription's id")
	}
	if id := freeID("claude"); id == "claude" {
		t.Fatal("import took the Claude subscription's id")
	}
}

// A ChatGPT Team workspace on the email of a personal plan is an account of
// its own: added beside the personal one, and either can be switched to.
func TestCodexTeamWorkspaceBesidePersonal(t *testing.T) {
	home := signIn(t) // me@example.com, personal Pro, workspace acct-1
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id_token": fakeJWT(map[string]any{"email": "me@example.com",
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "team", "chatgpt_account_id": "acct-team"}}),
			"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": "r-team",
		})
	}))
	defer fake.Close()
	oldTok, oldAddr := codexTokenURL, codexCallbackAddr
	t.Cleanup(func() { codexTokenURL, codexCallbackAddr = oldTok, oldAddr })
	codexTokenURL = fake.URL
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	codexCallbackAddr = ln.Addr().String()
	ln.Close()

	st, err := StartSignIn("codex")
	if err != nil {
		t.Fatal(err)
	}
	finishInBrowser(t, st, "code")
	if st = waitDone(t, st.ID); st.State != "done" || st.User != "me@example.com · Team" || st.Using {
		t.Fatalf("state %+v", st)
	}
	users, active := loginUsers(Logins("codex"))
	if strings.Join(users, ",") != "me@example.com,me@example.com · Team" || active != "me@example.com" {
		t.Fatalf("logins %v, active %q", users, active)
	}
	auth := filepath.Join(home, ".codex", "auth.json")
	var a codexAuth
	if readJSON(auth, &a); a.Tokens.AccountID != "acct-1" {
		t.Fatal("the personal sign-in was replaced")
	}
	if err := SwitchLogin("codex", "me@example.com · Team"); err != nil {
		t.Fatal(err)
	}
	if readJSON(auth, &a); a.Tokens.AccountID != "acct-team" {
		t.Fatalf("switched to %q", a.Tokens.AccountID)
	}
	if users, active = loginUsers(Logins("codex")); len(users) != 2 || active != "me@example.com · Team" {
		t.Fatalf("after the switch: %v, active %q", users, active)
	}
}

// A workspace saved under its bare email, before workspaces were told
// apart, is the same account as the one named with its plan.
func TestCodexLoginSavedBeforeWorkspaces(t *testing.T) {
	auth := func(plan, ws string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"tokens": map[string]any{"account_id": ws,
			"id_token": fakeJWT(map[string]any{"email": "me@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": plan}})}})
		return b
	}
	old := savedLogin{Agent: "codex", User: "me@example.com", Auth: auth("team", "acct-team")}
	if !sameLogin(old, savedLogin{Agent: "codex", User: "me@example.com · Team", Auth: auth("team", "acct-team")}) {
		t.Error("the Team workspace saved before is another account")
	}
	if sameLogin(old, savedLogin{Agent: "codex", User: "me@example.com", Auth: auth("plus", "acct-1")}) {
		t.Error("the personal plan is the Team workspace")
	}
}
