package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// staleEditor is #1238's machine: Copilot's editors keep a GitHub token
// for octocat in apps.json that GitHub answers 401 Bad credentials, and
// the Copilot CLI is signed in to cliUser with a token that works.
// It returns the editors' apps.json, how many requests reached the API
// with the CLI's token and how many trades the stale token was refused.
func staleEditor(t *testing.T, cliUser string) (string, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	home := signIn(t) // apps.json: octocat, gho_x
	cli := filepath.Join(home, "copilot-home")
	t.Setenv("COPILOT_HOME", cli)
	os.MkdirAll(cli, 0o700)
	os.WriteFile(filepath.Join(cli, "config.json"), []byte(`{"lastLoggedInUser":{"host":"https://github.com","login":"`+cliUser+`"},"loggedInUsers":[{"host":"https://github.com","login":"`+cliUser+`"}],"copilotTokens":{"https://github.com:`+cliUser+`":"gho_cli"}}`), 0o600)

	var served, refused atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Header.Get("Authorization") == "Bearer tid_magpie" && r.Header.Get("Copilot-Integration-Id") == "vscode-chat":
		case r.Header.Get("Authorization") == "Bearer gho_cli" && r.Header.Get("Copilot-Integration-Id") == "copilot-developer-cli":
			served.Add(1)
		default:
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`{"data":[{"id":"claude-sonnet-5","name":"Claude Sonnet 5","model_picker_enabled":true,"capabilities":{"type":"chat"}}]}`))
	}))
	t.Cleanup(api.Close)
	// the editors' exchange: the stale token is refused as GitHub refuses it
	exchange := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "token gho_magpie" {
			json.NewEncoder(w).Encode(map[string]any{"token": "tid_magpie", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
			return
		}
		refused.Add(1)
		w.WriteHeader(401)
		w.Write([]byte(`{"message":"Bad credentials","documentation_url":"https://docs.github.com/rest","status":"401"}`))
	}))
	t.Cleanup(exchange.Close)
	user := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_cli" {
			w.WriteHeader(401)
			w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"endpoints": map[string]string{"api": api.URL}})
	}))
	t.Cleanup(user.Close)
	oldUser, oldToken := CopilotUserURL, CopilotTokenURL
	CopilotUserURL, CopilotTokenURL = user.URL, exchange.URL
	t.Cleanup(func() { CopilotUserURL, CopilotTokenURL = oldUser, oldToken })
	copilotSessions = map[string]copilotSession{}
	copilotRefusals.Lock()
	copilotRefusals.at = map[string]time.Time{}
	copilotRefusals.Unlock()
	return filepath.Join(home, ".config", "github-copilot", "apps.json"), &served, &refused
}

// The editors' token refused, the CLI's sign-in of the same account is
// used, as the CLI sends it, from the first request on (#1238).
func TestCopilotStaleEditorFallsBackToCLI(t *testing.T) {
	_, _, refused := staleEditor(t, "octocat")
	p, ok := find(All(), "copilot")
	if !ok || p.Account.User != "octocat" {
		t.Fatalf("copilot: %+v", p)
	}
	ms, err := p.Fetch(context.Background())
	if err != nil || len(ms) != 2 || ms[0].ID != "claude-sonnet-5" {
		t.Fatalf("models: %+v %v", ms, err)
	}
	req, _ := http.NewRequest("POST", p.Chat+"/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, []byte(`{"messages":[{"role":"user","content":"hi"}]}`)); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "Bearer gho_cli" || req.Header.Get("Copilot-Integration-Id") != "copilot-developer-cli" {
		t.Fatalf("signed: %v", req.Header)
	}
	if refused.Load() != 1 {
		t.Fatalf("the stale token was traded %d times; once is enough", refused.Load())
	}
	// known refused, the account is the CLI's sign-in from then on
	if app, ok := copilotLogin(copilotConfigDir()); !ok || !app.cli || app.Token != "gho_cli" || app.User != "octocat" {
		t.Fatalf("login after the refusal: %+v", app)
	}
}

// The CLI signed in to another account, it is not used in the refused
// token's place; the error names the editors' file.
func TestCopilotStaleEditorNotAnotherAccount(t *testing.T) {
	apps, served, _ := staleEditor(t, "hubot")
	p, ok := find(All(), "copilot")
	if !ok || p.Account.User != "octocat" {
		t.Fatalf("copilot: %+v", p)
	}
	_, err := p.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), apps) || !strings.Contains(err.Error(), "Bad credentials") || !strings.Contains(err.Error(), "hubot, another account") {
		t.Fatalf("fetch: %v", err)
	}
	if served.Load() != 0 {
		t.Fatalf("the CLI's token for hubot reached the API %d times", served.Load())
	}
	if app, ok := copilotLogin(copilotConfigDir()); !ok || app.cli || app.Token != "gho_x" {
		t.Fatalf("login after the refusal: %+v", app)
	}
}

// Signed in again from magpie, the same account as the editors' refused
// sign-in is kept and used, not let go as the editors' own (#1238: "Repeating
// the Magpie device login still leaves the editor credential selected").
func TestCopilotSignInKeptOverStaleEditor(t *testing.T) {
	staleEditor(t, "nobody")
	os.RemoveAll(copilotCLIHome()) // no CLI sign-in
	copilotLoginList()             // the editors' own octocat, remembered
	copilotProbeEditor(context.Background(), "octocat", "")
	if err := addCopilotLogin("octocat", "Business", "gho_magpie", ""); err != nil {
		t.Fatal(err)
	}
	ls := copilotLogins(copilotConfigDir())
	if len(ls) != 1 || ls[0].User != "octocat" || ls[0].app.Token != "gho_magpie" {
		t.Fatalf("accounts: %+v", ls)
	}
	p, ok := find(All(), "copilot")
	if !ok {
		t.Fatal("no copilot")
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatalf("fetch with magpie's sign-in: %v", err)
	}
}

// The editors' sign-in still taken, a sign-in from magpie of the same
// account is let go as before: the editors' own stands for it.
func TestCopilotSignInSameAsWorkingEditor(t *testing.T) {
	staleEditor(t, "nobody")
	os.RemoveAll(copilotCLIHome()) // no CLI sign-in
	exchange := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"token": "tid", "expires_at": time.Now().Add(time.Hour).Unix()})
	}))
	defer exchange.Close()
	CopilotTokenURL = exchange.URL
	copilotLoginList()
	copilotProbeEditor(context.Background(), "octocat", "")
	if err := addCopilotLogin("octocat", "Business", "gho_magpie", ""); err != nil {
		t.Fatal(err)
	}
	ls := copilotLogins(copilotConfigDir())
	if len(ls) != 1 || ls[0].app.Token != "gho_x" {
		t.Fatalf("accounts: %+v", ls)
	}
}

// The Usage card reads the same account: the editors' stale token refused,
// the CLI's sign-in of the same account is read in its place.
func TestCopilotStaleEditorUsageReadsCLI(t *testing.T) {
	staleEditor(t, "octocat")
	q := loginQuota(context.Background(), Login{Agent: "copilot", User: "octocat"})
	if q.Error != "" {
		t.Fatalf("usage: %+v", q)
	}
}

// A 403 on the trade can be an account without Copilot, not a stale token:
// it gives way to the CLI's sign-in of the same account, but the error
// doesn't call the editors' token refused.
func TestCopilotForbiddenIsNotCalledStale(t *testing.T) {
	copilotRefuse("gho_forbidden", http.StatusForbidden)
	copilotRefuse("gho_unauthorized", http.StatusUnauthorized)
	if !copilotRefusedToken("gho_forbidden") || copilotStaleToken("gho_forbidden") {
		t.Fatal("a 403 is refused (the CLI stands in) but not stale")
	}
	if !copilotStaleToken("gho_unauthorized") {
		t.Fatal("a 401 is stale")
	}
}
