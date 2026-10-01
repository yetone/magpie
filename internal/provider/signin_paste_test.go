package provider

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeCodexTokens answers Codex's token endpoint for the code "cx", counting
// the trades; hold, when set, keeps each answer until it is closed.
func fakeCodexTokens(t *testing.T, hold chan struct{}) *atomic.Int32 {
	t.Helper()
	var trades atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		trades.Add(1)
		if hold != nil {
			<-hold
		}
		if r.Form.Get("code") != "cx" || r.Form.Get("code_verifier") == "" ||
			!strings.HasSuffix(r.Form.Get("redirect_uri"), "/auth/callback") {
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
	t.Cleanup(fake.Close)
	oldTok, oldAddr := codexTokenURL, codexCallbackAddr
	t.Cleanup(func() { codexTokenURL, codexCallbackAddr = oldTok, oldAddr })
	codexTokenURL = fake.URL
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	codexCallbackAddr = ln.Addr().String()
	ln.Close()
	return &trades
}

// callbackAddress is the address the vendor's page sends the browser back
// to, as the browser shows it: localhost, which from a server or Docker
// container is not where magpie is.
func callbackAddress(t *testing.T, st SignInState, code, state string) string {
	t.Helper()
	u, err := url.Parse(st.URL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if state == "" {
		state = q.Get("state")
	}
	return q.Get("redirect_uri") + "?" + url.Values{"code": {code}, "state": {state}}.Encode()
}

// Jorben on Discord: magpie in Docker on a cloud server, its web UI in his
// browser; ChatGPT sends the browser back to localhost:1455, which is his
// own machine, and the sign-in never finished. The address pasted finishes it.
func TestCodexSignInFromPastedAddress(t *testing.T) {
	home := claudeHome(t)
	trades := fakeCodexTokens(t, nil)

	st, err := StartSignIn("codex")
	if err != nil {
		t.Fatal(err)
	}
	defer CancelSignIn(st.ID)
	if !st.PasteCallback {
		t.Fatal("ChatGPT's sign-in doesn't take a pasted address")
	}
	good := callbackAddress(t, st, "cx", "")

	// another sign-in's address, or one that isn't the callback: refused,
	// nothing traded, still waiting
	for _, bad := range []string{
		callbackAddress(t, st, "cx", "not-ours"),
		strings.Replace(good, "/auth/callback", "/elsewhere", 1),
		strings.Replace(good, "localhost", "evil.example", 1),
		"cx",
		"",
	} {
		if err := SubmitSignInCallback(st.ID, bad); err == nil {
			t.Fatalf("%q was taken", bad)
		}
	}
	if n := trades.Load(); n != 0 {
		t.Fatalf("%d trades for refused addresses", n)
	}
	if cur, _ := SignInStatus(st.ID); cur.State != "waiting" {
		t.Fatalf("state %+v", cur)
	}

	if err := SubmitSignInCallback(st.ID, "  "+good+"\n"); err != nil {
		t.Fatal(err)
	}
	st, _ = SignInStatus(st.ID)
	if st.State != "done" || st.User != "cx@example.com" || !st.Using {
		t.Fatalf("state %+v", st)
	}
	var a codexAuth
	readJSON(filepath.Join(home, ".codex", "auth.json"), &a)
	if a.Tokens.RefreshToken != "r-cx" || a.Tokens.AccountID != "acct-cx" {
		t.Fatalf("auth.json %+v", a)
	}
	// pasted again: over, and the code isn't traded twice
	if err := SubmitSignInCallback(st.ID, good); err == nil {
		t.Fatal("a finished sign-in took its address again")
	}
	if n := trades.Load(); n != 1 {
		t.Fatalf("%d trades", n)
	}
}

// The browser's own callback and the pasted address, both at once: one
// trades the code, the other waits for how that went.
func TestPastedAddressAndCallbackFinishOnce(t *testing.T) {
	claudeHome(t)
	hold := make(chan struct{})
	trades := fakeCodexTokens(t, hold)

	st, err := StartSignIn("codex")
	if err != nil {
		t.Fatal(err)
	}
	defer CancelSignIn(st.ID)
	good := callbackAddress(t, st, "cx", "")
	pasted := make(chan error, 1)
	go func() { pasted <- SubmitSignInCallback(st.ID, good) }()
	for i := 0; trades.Load() == 0; i++ {
		if i > 200 {
			t.Fatal("the pasted address was never traded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// a second trade would wait on hold: it must not start
	browser := http.Client{Timeout: 3 * time.Second}
	resp, err := browser.Get(strings.Replace(good, "localhost", "127.0.0.1", 1))
	if err != nil {
		close(hold)
		t.Fatalf("the browser's callback traded the code again: %v", err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), "already finishing") {
		t.Fatalf("second callback's page: %s", page)
	}
	close(hold)
	if err := <-pasted; err != nil {
		t.Fatal(err)
	}
	if st, _ = SignInStatus(st.ID); st.State != "done" || st.User != "cx@example.com" {
		t.Fatalf("state %+v", st)
	}
	if n := trades.Load(); n != 1 {
		t.Fatalf("the code was traded %d times", n)
	}
}

// Every built-in sign-in that comes back to a port here takes the address
// pasted instead; one that doesn't come back through the browser's address
// bar doesn't offer it. Claude's is Claude Code's own (signin_claude_test.go).
func TestLoopbackSignInsTakePastedAddress(t *testing.T) {
	claudeHome(t)
	fakeCodexTokens(t, nil)
	for _, agent := range []string{"codex", "gemini", "antigravity", "kiro", "zed"} {
		st, err := StartSignIn(agent)
		if err != nil {
			t.Fatalf("%s: %v", agent, err)
		}
		CancelSignIn(st.ID)
		if !st.PasteCallback {
			t.Errorf("%s: no pasted address", agent)
		}
	}
	st, err := StartSignIn(CommandCodePlanID)
	if err != nil {
		t.Fatal(err)
	}
	CancelSignIn(st.ID)
	if st.PasteCallback {
		t.Error("Command Code's key is posted, never in an address to paste")
	}
}
