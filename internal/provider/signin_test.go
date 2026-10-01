package provider

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// finishInBrowser does what the browser does after the vendor's page:
// come back to magpie's callback with a code.
func finishInBrowser(t *testing.T, st SignInState, code string) string {
	t.Helper()
	u, err := url.Parse(st.URL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	back := q.Get("redirect_uri") + "?" + url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
	resp, err := http.Get(strings.Replace(back, "localhost", "127.0.0.1", 1))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func waitDone(t *testing.T, id string) SignInState {
	t.Helper()
	for i := 0; i < 100; i++ {
		st, _ := SignInStatus(id)
		if st.State != "waiting" {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("sign-in never finished")
	return SignInState{}
}

func TestClaudeSignInAddsAnAccount(t *testing.T) {
	home := claudeHome(t)
	noAnthropic(t)
	fakeClaudeLogin(t, fakeClaudeAccount{email: "new@example.com", name: "New", plan: "max", refresh: "sk-ant-ort01-new"}, "", true)

	// signed in already, to someone else: the new account is added beside it
	cred := claudeSignIn(t, home, time.Now().Add(time.Hour))
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{"oauthAccount": map[string]any{"emailAddress": "old@example.com"}})

	st, err := StartSignIn("claude")
	if err != nil {
		t.Fatal(err)
	}
	// the page Claude Code itself opens, coming back to it
	if !strings.HasPrefix(st.URL, "https://claude.com/cai/oauth/authorize?") || !st.PasteCallback {
		t.Fatalf("state %+v", st)
	}
	page := finishInBrowser(t, st, "the-code")
	if !strings.Contains(page, "signed in") {
		t.Fatalf("page %s", page)
	}
	st = waitDone(t, st.ID)
	if st.State != "done" || st.User != "new@example.com" || st.Plan != "max" || st.Using {
		t.Fatalf("state %+v", st)
	}
	users, active := loginUsers(Logins("claude"))
	if strings.Join(users, ",") != "new@example.com,old@example.com" || active != "old@example.com" {
		t.Fatalf("logins %v, active %q", users, active)
	}
	// and it's one click away
	if err := SwitchLogin("claude", "new@example.com"); err != nil {
		t.Fatal(err)
	}
	var c map[string]map[string]any
	readJSON(cred, &c)
	if c["claudeAiOauth"]["refreshToken"] != "sk-ant-ort01-new" || c["claudeAiOauth"]["subscriptionType"] != "max" {
		t.Fatalf("credentials %v", c["claudeAiOauth"])
	}
	b, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	if !strings.Contains(string(b), `"displayName": "New"`) {
		t.Fatalf(".claude.json %s", b)
	}
}

func TestCodexSignInSignsInWhenSignedOut(t *testing.T) {
	home := claudeHome(t)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("code") != "cx" || r.Form.Get("client_id") != codexClientID || r.Form.Get("code_verifier") == "" {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant", "error_description": "bad code"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id_token": fakeJWT(map[string]any{"email": "cx@example.com",
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": "acct-cx"}}),
			"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": "r-cx",
		})
	}))
	defer fake.Close()
	oldTok, oldAddr := codexTokenURL, codexCallbackAddr
	t.Cleanup(func() { codexTokenURL, codexCallbackAddr = oldTok, oldAddr })
	codexTokenURL = fake.URL
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	codexCallbackAddr = ln.Addr().String()
	ln.Close()

	// a bad code fails, and says why
	st, err := StartSignIn("codex")
	if err != nil {
		t.Fatal(err)
	}
	finishInBrowser(t, st, "wrong")
	if st = waitDone(t, st.ID); st.State != "failed" || !strings.Contains(st.Error, "bad code") {
		t.Fatalf("state %+v", st)
	}
	time.Sleep(1200 * time.Millisecond) // the callback port is let go

	st, err = StartSignIn("codex")
	if err != nil {
		t.Fatal(err)
	}
	finishInBrowser(t, st, "cx")
	if st = waitDone(t, st.ID); st.State != "done" || st.User != "cx@example.com" || !st.Using {
		t.Fatalf("state %+v", st)
	}
	var a codexAuth
	readJSON(filepath.Join(home, ".codex", "auth.json"), &a)
	if a.Tokens.RefreshToken != "r-cx" || a.Tokens.AccountID != "acct-cx" {
		t.Fatalf("auth.json %+v", a)
	}
	if _, active := loginUsers(Logins("codex")); active != "cx@example.com" {
		t.Fatalf("active %q", active)
	}
}

func TestSignInIgnoresAForeignCallback(t *testing.T) {
	claudeHome(t)
	oldAddr := codexCallbackAddr
	t.Cleanup(func() { codexCallbackAddr = oldAddr })
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	codexCallbackAddr = ln.Addr().String()
	ln.Close()
	st, err := StartSignIn("codex")
	if err != nil {
		t.Fatal(err)
	}
	defer CancelSignIn(st.ID)
	u, _ := url.Parse(st.URL)
	back := strings.Replace(u.Query().Get("redirect_uri"), "localhost", "127.0.0.1", 1)
	resp, err := http.Get(back + "?code=x&state=not-ours")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if st, _ := SignInStatus(st.ID); st.State != "waiting" {
		t.Fatalf("state %+v", st)
	}
	CancelSignIn(st.ID)
	if st, _ := SignInStatus(st.ID); st.State != "canceled" {
		t.Fatalf("state %+v", st)
	}
}
