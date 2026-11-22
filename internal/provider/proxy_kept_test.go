package provider

import (
	"context"
	"encoding/json"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// codexVendor answers Codex's token exchange and refresh as OpenAI would,
// directly or as a proxy: a code or refresh token "who~ws" is the account
// who@example.com in workspace acct-ws (acct-who when no ~ws), so two
// workspaces of one email can sign in.
func codexVendor(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var code string
		if strings.Contains(r.Header.Get("Content-Type"), "json") {
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			code = strings.TrimPrefix(body["refresh_token"], "r-")
		} else {
			r.ParseForm()
			code = r.Form.Get("code")
			if code == "" {
				code = strings.TrimPrefix(r.Form.Get("refresh_token"), "r-")
			}
		}
		who, ws, _ := strings.Cut(code, "~")
		if ws == "" {
			ws = who
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id_token": fakeJWT(map[string]any{"email": who + "@example.com",
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": "acct-" + ws}}),
			"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": "r-" + code,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// @zzstar101 on X (新登录的ChatGPT账号导进去，会让旧设置的代理清空): a
// ChatGPT account coming in, by a sign-in (a proxy picked for it or not)
// or by an import, leaves the subscription's proxy and every other
// account's own as they were, a second workspace of one email included.
func TestNewAccountKeepsTheProxiesSet(t *testing.T) {
	claudeHome(t)
	dialed := dialsOut(t)
	sub, own1, own2, picked := codexVendor(t), codexVendor(t), codexVendor(t), codexVendor(t)
	oldTok, oldAddr := codexTokenURL, codexCallbackAddr
	t.Cleanup(func() { codexTokenURL, codexCallbackAddr = oldTok, oldAddr })
	codexTokenURL = "http://auth.openai.invalid/oauth/token"
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	codexCallbackAddr = ln.Addr().String()
	ln.Close()

	signIn := func(o SignInOptions, code string) SignInState {
		t.Helper()
		st, err := StartSignInWith("codex", o)
		if err != nil {
			t.Fatal(err)
		}
		finishInBrowser(t, st, code)
		if st = waitDone(t, st.ID); st.State != "done" || st.Error != "" {
			t.Fatalf("state %+v", st)
		}
		time.Sleep(1200 * time.Millisecond) // the callback port is let go
		return st
	}
	pick := sub.URL
	signIn(SignInOptions{Proxy: &pick}, "a")
	signIn(SignInOptions{}, "b")
	want := map[string]string{"a@example.com": own1.URL, "b@example.com": own2.URL}
	setProxies(t, "codex", sub.URL, want)
	kept := func(step string, more map[string]string) {
		t.Helper()
		p, _ := storedPicks("codex")
		w := maps.Clone(want)
		maps.Copy(w, more)
		if p.Proxy != sub.URL || !maps.Equal(p.AccountProxies, w) {
			t.Fatalf("%s: kept proxy %q, accounts' %v; want %q, %v", step, p.Proxy, p.AccountProxies, sub.URL, w)
		}
		for u, v := range want {
			if got := ProxyOfLogin("codex", u); got != v {
				t.Fatalf("%s: %s goes through %q, not %q", step, u, got, v)
			}
		}
	}

	signIn(SignInOptions{}, "c")
	kept("a new account signed in", nil)

	// another workspace of a's email, its sign-in through the
	// subscription's proxy picked: it follows the subscription's, a's own
	// left as it was
	pick = sub.URL
	st := signIn(SignInOptions{Proxy: &pick}, "a~ws2")
	kept("a's email in another workspace, the subscription's picked", nil)
	if st.User == "a@example.com" {
		t.Fatalf("the sign-in says it is a's: %+v", st)
	}
	// and one more through a proxy of its own: its own, by its own name
	pick = picked.URL
	st = signIn(SignInOptions{Proxy: &pick}, "a~ws3")
	kept("a's email in a third workspace, its own picked", map[string]string{strings.ToLower(st.User): picked.URL})
	if got := ProxyOfLogin("codex", st.User); got != picked.URL {
		t.Fatalf("%s goes through %q", st.User, got)
	}

	codexTokenURL = sub.URL
	rs, err := ImportLogins(context.Background(), "codex", []string{
		`{"type":"codex","refresh_token":"r-d","email":"d@example.com"}`,
		`{"type":"codex","refresh_token":"r-b~ws4","email":"b@example.com"}`,
	})
	if err != nil || importStatuses(rs) != "d@example.com:added,b@example.com:added" {
		t.Fatalf("imported %+v, %v", rs, err)
	}
	kept("accounts imported", map[string]string{strings.ToLower(st.User): picked.URL})
	if d := dialed(); len(d) != 0 {
		t.Fatalf("dialed %v past the proxies", d)
	}
}
