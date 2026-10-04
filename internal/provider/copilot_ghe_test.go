package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCopilotHost(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "github.com": "", "https://github.com": "", "https://github.com/": "", "GitHub.com": "",
		"acme.ghe.com": "acme.ghe.com", "https://acme.ghe.com": "acme.ghe.com", "https://Acme.GHE.com/": "acme.ghe.com",
		" octo-corp.ghe.com ": "octo-corp.ghe.com",
	} {
		if got, err := CopilotHost(in); err != nil || got != want {
			t.Errorf("CopilotHost(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"evil.com", "ghe.com", ".ghe.com", "a.b.ghe.com", "acme.ghe.com.evil.com", "acme.ghe.com:8443",
		"https://acme.ghe.com:8443", "https://user@acme.ghe.com", "https://acme.ghe.com/x", "acme.ghe.com/x",
		"-acme.ghe.com", "acme-.ghe.com", "api.github.com", "github.example.com", "ftp://acme.ghe.com", "acme_x.ghe.com",
	} {
		if got, err := CopilotHost(in); err == nil {
			t.Errorf("CopilotHost(%q) = %q, want an error", in, got)
		}
	}
}

// gheServer stands in for an enterprise's hosts: a request to
// https://<sub>.acme.ghe.com/<path> arrives as /<sub>.acme.ghe.com/<path>.
// github.com's own URLs point at a path it refuses.
func gheServer(t *testing.T, token string) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		auth := r.Header.Get("Authorization")
		switch r.Method + " " + r.URL.Path {
		case "POST /acme.ghe.com/login/device/code":
			r.ParseForm()
			if r.Form.Get("client_id") != copilotClientID {
				w.WriteHeader(400)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"device_code": "dc", "user_code": "ABCD-1234", "verification_uri": "https://acme.ghe.com/login/device", "interval": 1})
		case "POST /acme.ghe.com/login/oauth/access_token":
			r.ParseForm()
			if r.Form.Get("device_code") != "dc" || r.Form.Get("client_id") != copilotClientID {
				w.WriteHeader(400)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": token})
		case "GET /api.acme.ghe.com/user":
			if auth != "token "+token {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"login": "mona_acme"})
		case "GET /api.acme.ghe.com/copilot_internal/user":
			if auth != "token "+token {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"copilot_plan": "enterprise", "endpoints": map[string]string{"api": srv.URL + "/copilot-api.acme.ghe.com"},
				"quota_snapshots": map[string]any{"premium_interactions": map[string]any{"has_quota": true, "entitlement": 1000, "quota_remaining": 750}}})
		case "GET /api.acme.ghe.com/copilot_internal/v2/token":
			if auth != "token "+token {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"token": "tid=ent", "expires_at": time.Now().Add(time.Hour).Unix(),
				"endpoints": map[string]string{"api": srv.URL + "/copilot-api.acme.ghe.com"}})
		case "GET /copilot-api.acme.ghe.com/models":
			if auth != "Bearer tid=ent" && auth != "Bearer "+token {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte(`{"data":[{"id":"gpt-5.5","name":"GPT-5.5","model_picker_enabled":true,"capabilities":{"type":"chat"},"supported_endpoints":["/responses"]}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	oldOrigin := copilotGHEOrigin
	copilotGHEOrigin = func(host, sub string) string {
		if sub != "" {
			host = sub + "." + host
		}
		return srv.URL + "/" + host
	}
	never := srv.URL + "/github.com"
	oldDev, oldTok, oldCT, oldCU, oldGU := gitHubDeviceURL, gitHubTokenURL, CopilotTokenURL, CopilotUserURL, GitHubUserURL
	gitHubDeviceURL, gitHubTokenURL, CopilotTokenURL, CopilotUserURL, GitHubUserURL = never, never, never, never, never
	t.Cleanup(func() {
		copilotGHEOrigin = oldOrigin
		gitHubDeviceURL, gitHubTokenURL, CopilotTokenURL, CopilotUserURL, GitHubUserURL = oldDev, oldTok, oldCT, oldCU, oldGU
	})
	copilotMu.Lock()
	copilotSessions = map[string]copilotSession{}
	copilotMu.Unlock()
	return srv, &seen
}

// An account on an enterprise's <name>.ghe.com signs in there, remembers
// it, and is looked up, listed, served and metered there (#723).
func TestCopilotGHESignIn(t *testing.T) {
	signIn(t)
	srv, seen := gheServer(t, "ghu_ent")

	if _, err := StartSignInAt("copilot", "evil.example.com"); err == nil {
		t.Fatal("a sign-in at a host that isn't github.com or <name>.ghe.com started")
	}
	st, err := StartSignInAt("copilot", "https://acme.ghe.com/")
	if err != nil {
		t.Fatal(err)
	}
	if st.Code != "ABCD-1234" || st.URL != "https://acme.ghe.com/login/device" {
		t.Fatalf("sign-in: %+v", st)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	st, err = WaitSignIn(ctx, st.ID)
	if err != nil || st.State != "done" || st.User != "mona_acme" || st.Plan != "Enterprise" || st.Using {
		t.Fatalf("signed in: %+v %v", st, err)
	}
	var saved *savedLogin
	for _, l := range readLogins() {
		if l.Agent == "copilot" && l.User == "mona_acme" {
			saved = &l
		}
	}
	var auth map[string]string
	if saved == nil || json.Unmarshal(saved.Auth, &auth) != nil || len(auth) != 2 || auth["host"] != "acme.ghe.com" || auth["oauth_token"] != "ghu_ent" {
		t.Fatalf("kept: %+v", saved)
	}

	var app copilotApp
	for _, c := range copilotLogins(copilotConfigDir()) {
		if c.User == "mona_acme" {
			app = c.app
		}
	}
	if app.Host != "acme.ghe.com" || app.Token != "ghu_ent" {
		t.Fatalf("listed: %+v", app)
	}
	p := copilotProvider(app, "Enterprise")
	ms, err := p.Fetch(context.Background())
	if err != nil || len(ms) != 2 || ms[0].ID != "gpt-5.5" {
		t.Fatalf("models: %+v %v", ms, err)
	}
	if p.Responses != srv.URL+"/copilot-api.acme.ghe.com" {
		t.Fatalf("base: %s", p.Responses)
	}
	// the gateway asks the provider's base; the test's base has a path of its own
	req, _ := http.NewRequest("POST", "https://copilot-api.acme.ghe.com/responses", nil)
	if err := p.Sign(context.Background(), req, Responses, []byte(`{"model":"gpt-5.5","input":"hi"}`)); err != nil {
		t.Fatal(err)
	}
	if req.URL.String() != srv.URL+"/copilot-api.acme.ghe.com/responses" || req.Header.Get("Authorization") != "Bearer tid=ent" {
		t.Fatalf("signed: %s %v", req.URL, req.Header)
	}
	q := copilotSubscriptionUsage(context.Background(), app.Token, app.Host)
	if q.Error != "" || q.Plan != "Enterprise" || len(q.Windows) != 1 || q.Windows[0].Used != 25 {
		t.Fatalf("usage: %+v", q)
	}
	for _, s := range *seen {
		if strings.Contains(s, "/github.com") {
			t.Errorf("an enterprise account asked github.com: %s", s)
		}
	}

	// a host edited into logins.json isn't sent the token
	if _, ok := copilotSaved(savedLogin{Agent: "copilot", User: "x", Auth: []byte(`{"oauth_token":"t","host":"evil.example.com"}`)}); ok {
		t.Fatal("an account at evil.example.com was taken")
	}
}

// `copilot login --host acme.ghe.com` is read, and served at its
// enterprise; a github.com sign-in beside it still comes first, and a
// GitHub Enterprise Server's is left out.
func TestCopilotCLIGHELogin(t *testing.T) {
	home := signIn(t)
	os.Remove(filepath.Join(home, ".config", "github-copilot", "apps.json"))
	cli := filepath.Join(home, "copilot-home")
	t.Setenv("COPILOT_HOME", cli)
	os.MkdirAll(cli, 0o700)
	srv, _ := gheServer(t, "gho_ent")
	oldSecret := copilotCLISecret
	copilotCLISecret = func(string) string { return "" }
	defer func() { copilotCLISecret = oldSecret }()

	write := func(s string) { os.WriteFile(filepath.Join(cli, "config.json"), []byte(s), 0o600) }
	write(`{"lastLoggedInUser":{"host":"https://acme.ghe.com","login":"mona_acme"},
		"loggedInUsers":[{"host":"https://acme.ghe.com","login":"mona_acme"}],
		"copilotTokens":{"https://acme.ghe.com:mona_acme":"gho_ent"}}`)
	p, ok := find(All(), "copilot")
	if !ok || p.Account.User != "mona_acme" {
		t.Fatalf("copilot: %+v", p)
	}
	ms, err := p.Fetch(context.Background())
	if err != nil || len(ms) != 2 || ms[0].ID != "gpt-5.5" {
		t.Fatalf("models: %+v %v", ms, err)
	}
	req, _ := http.NewRequest("POST", "https://copilot-api.acme.ghe.com/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, []byte(`{"messages":[{"role":"user","content":"hi"}]}`)); err != nil {
		t.Fatal(err)
	}
	if req.URL.String() != srv.URL+"/copilot-api.acme.ghe.com/chat/completions" || req.Header.Get("Authorization") != "Bearer gho_ent" {
		t.Fatalf("signed: %s %v", req.URL, req.Header)
	}

	write(`{"lastLoggedInUser":{"host":"https://acme.ghe.com","login":"mona_acme"},
		"loggedInUsers":[{"host":"https://acme.ghe.com","login":"mona_acme"},{"host":"https://github.com","login":"octocat"}],
		"copilotTokens":{"https://acme.ghe.com:mona_acme":"gho_ent","https://github.com:octocat":"gho_dot"}}`)
	if app, ok := copilotCLILogin(); !ok || app.User != "octocat" || app.Host != "" || app.Token != "gho_dot" {
		t.Fatalf("github.com first: %+v", app)
	}
	write(`{"lastLoggedInUser":{"host":"https://github.example.com","login":"ghes"},"copilotTokens":{"https://github.example.com:ghes":"gho_ghes"}}`)
	if app, ok := copilotCLILogin(); ok {
		t.Fatalf("a GitHub Enterprise Server sign-in was taken: %+v", app)
	}
}
