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

// lapsedCopilot is a machine with the editors signed in to octocat (gho_x,
// taken) and magpie to hubot (gho_kept), whose trade for a session token
// answers what status says.
func lapsedCopilot(t *testing.T) *atomic.Int32 {
	t.Helper()
	signIn(t)
	os.RemoveAll(copilotCLIHome()) // no CLI sign-in
	var status atomic.Int32
	status.Store(200)
	exchange := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "token gho_kept" && status.Load() != 200 {
			w.WriteHeader(int(status.Load()))
			w.Write([]byte(`{"message":"Bad credentials","documentation_url":"https://docs.github.com/rest","status":"401"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"token": "tid", "expires_at": time.Now().Add(time.Hour).Unix()})
	}))
	t.Cleanup(exchange.Close)
	old := CopilotTokenURL
	CopilotTokenURL = exchange.URL
	t.Cleanup(func() { CopilotTokenURL = old })
	resetCopilotState()
	t.Cleanup(resetCopilotState)
	copilotLoginList()
	if err := addCopilotLogin("hubot", "Pro", "gho_kept", ""); err != nil {
		t.Fatal(err)
	}
	return &status
}

func resetCopilotState() {
	copilotMu.Lock()
	copilotSessions = map[string]copilotSession{}
	copilotMu.Unlock()
	copilotRefusals.Lock()
	copilotRefusals.at, copilotRefusals.status, copilotRefusals.said = map[string]time.Time{}, map[string]int{}, map[string]error{}
	copilotRefusals.Unlock()
	copilotToSHolds.Lock()
	copilotToSHolds.m = map[string]copilotHold{}
	copilotToSHolds.Unlock()
	copilotLapsedMu.Lock()
	copilotLapsedSeen = map[string]bool{}
	copilotLapsedMu.Unlock()
}

func copilotLapsedOf(t *testing.T, user string) string {
	t.Helper()
	for _, l := range Logins("copilot") {
		if strings.EqualFold(l.User, user) {
			return l.Lapsed
		}
	}
	t.Fatalf("no copilot account %s in %+v", user, Logins("copilot"))
	return ""
}

// copilotTrade sends a request on user's account the way the gateway
// does, and says what it got.
func copilotTrade(t *testing.T, user string) error {
	t.Helper()
	for _, c := range copilotLogins(copilotConfigDir()) {
		if strings.EqualFold(c.User, user) {
			copilotMu.Lock()
			copilotSessions = map[string]copilotSession{}
			copilotMu.Unlock()
			p := copilotProvider(c.app, c.Plan)
			req, _ := http.NewRequest("POST", "https://api.githubcopilot.com/chat/completions", nil)
			return p.Sign(context.Background(), req, Chat, []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
		}
	}
	t.Fatalf("no copilot account %s", user)
	return nil
}

// echo_ts on Discord: GitHub revoked a Copilot account's token, and the
// account still read as signed in while every request failed. Refused
// with 401 (or 403), it is signed out on its own row; signed in again in
// place it is in use as it was, its mark gone.
func TestCopilotRefusedTokenSignsTheAccountOut(t *testing.T) {
	for _, code := range []int{401, 403} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			status := lapsedCopilot(t)
			if err := copilotTrade(t, "hubot"); err != nil {
				t.Fatalf("taken: %v", err)
			}
			if l := copilotLapsedOf(t, "hubot"); l != "" {
				t.Fatalf("lapsed while taken: %q", l)
			}
			status.Store(int32(code))
			if err := copilotTrade(t, "hubot"); err == nil || !strings.Contains(err.Error(), "signed out") {
				t.Fatalf("refused: %v", err)
			}
			if l := copilotLapsedOf(t, "hubot"); l != copilotLapsedText {
				t.Fatalf("refused %d, the account reads %q", code, l)
			}
			if l := copilotLapsedOf(t, "octocat"); l != "" {
				t.Fatalf("the editors' octocat, still taken, reads %q", l)
			}
			// marked in logins.json: it reads so after a restart as well
			resetCopilotState()
			if l := copilotLapsedOf(t, "hubot"); l != copilotLapsedText {
				t.Fatalf("after a restart the account reads %q", l)
			}
			var before Login
			for _, l := range Logins("copilot") {
				if l.User == "hubot" {
					before = l
				}
			}
			// signed in again from its row: the new token, in use as before
			if err := addCopilotLogin("hubot", "Pro", "gho_new", ""); err != nil {
				t.Fatal(err)
			}
			if l := copilotLapsedOf(t, "hubot"); l != "" {
				t.Fatalf("signed in again, the account reads %q", l)
			}
			// nothing of the old token's mark is kept in logins.json
			for _, l := range readLogins() {
				if l.Agent == "copilot" && l.User == "hubot" && (l.Lapsed != "" || l.Refused != "") {
					t.Fatalf("signed in again, saved %+v", l)
				}
			}
			n := 0
			for _, l := range Logins("copilot") {
				if l.User == "hubot" {
					n++
					if l.On != before.On || l.Active != before.Active || l.Plan != before.Plan {
						t.Fatalf("signed in again: %+v, was %+v", l, before)
					}
				}
			}
			if n != 1 {
				t.Fatalf("hubot listed %d times after signing in again", n)
			}
			if err := copilotTrade(t, "hubot"); err != nil {
				t.Fatalf("the new sign-in: %v", err)
			}
		})
	}
}

// Only GitHub's refusal of the token signs an account out: a 5xx, a 429 or
// a network error leave it as it was.
func TestCopilotTransientErrorsDontSignOut(t *testing.T) {
	status := lapsedCopilot(t)
	for _, code := range []int32{500, 502, 503, 429} {
		status.Store(code)
		if err := copilotTrade(t, "hubot"); err == nil {
			t.Fatalf("%d: no error", code)
		}
		if l := copilotLapsedOf(t, "hubot"); l != "" {
			t.Fatalf("%d signed the account out: %q", code, l)
		}
	}
	old := CopilotTokenURL
	dead := httptest.NewServer(http.NotFoundHandler())
	CopilotTokenURL = dead.URL
	dead.Close()
	if err := copilotTrade(t, "hubot"); err == nil {
		t.Fatal("unreachable: no error")
	}
	CopilotTokenURL = old
	if l := copilotLapsedOf(t, "hubot"); l != "" {
		t.Fatalf("a network error signed the account out: %q", l)
	}
}

// Marked signed out, an account GitHub takes again (a request served on
// the same token) is in use again without a sign-in.
func TestCopilotServedAgainClearsTheMark(t *testing.T) {
	status := lapsedCopilot(t)
	status.Store(401)
	copilotTrade(t, "hubot")
	if copilotLapsedOf(t, "hubot") == "" {
		t.Fatal("not marked")
	}
	resetCopilotState()
	status.Store(200)
	if err := copilotTrade(t, "hubot"); err != nil {
		t.Fatal(err)
	}
	if l := copilotLapsedOf(t, "hubot"); l != "" {
		t.Fatalf("served again, the account reads %q", l)
	}
}

// The editors' token refused while the Copilot CLI's sign-in of the same
// account stands in for it (#1238): served, so not signed out.
func TestCopilotStandInIsNotSignedOut(t *testing.T) {
	staleEditor(t, "octocat")
	t.Cleanup(resetCopilotState)
	copilotLapsedMu.Lock()
	copilotLapsedSeen = map[string]bool{}
	copilotLapsedMu.Unlock()
	p, ok := find(All(), "copilot")
	if !ok {
		t.Fatal("no copilot")
	}
	req, _ := http.NewRequest("POST", p.Chat+"/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if l := copilotLapsedOf(t, "octocat"); l != "" {
		t.Fatalf("served by the CLI's sign-in, the account reads %q", l)
	}
}

// The editors' token refused with the CLI signed in to another account:
// nothing stands in, so the editors' own account is signed out, and
// signed in again from magpie it is kept and in use.
func TestCopilotEditorRefusedSignsOut(t *testing.T) {
	staleEditor(t, "hubot")
	t.Cleanup(resetCopilotState)
	copilotLapsedMu.Lock()
	copilotLapsedSeen = map[string]bool{}
	copilotLapsedMu.Unlock()
	p, ok := find(All(), "copilot")
	if !ok {
		t.Fatal("no copilot")
	}
	if _, err := p.Fetch(context.Background()); err == nil {
		t.Fatal("no error")
	}
	if l := copilotLapsedOf(t, "octocat"); l != copilotLapsedText {
		t.Fatalf("refused, the editors' account reads %q", l)
	}
	if err := addCopilotLogin("octocat", "Business", "gho_magpie", ""); err != nil {
		t.Fatal(err)
	}
	if l := copilotLapsedOf(t, "octocat"); l != "" {
		t.Fatalf("signed in again, the account reads %q", l)
	}
	p, _ = find(All(), "copilot")
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatalf("with magpie's sign-in: %v", err)
	}
}

// The account's allowance read is GitHub's answer too: 401 there, nothing
// in its place, signs it out and says so on its row; a 503 doesn't; read
// again, it is signed in again.
func TestCopilotUsageRefusedSignsOut(t *testing.T) {
	lapsedCopilot(t)
	var status atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "token gho_kept" && status.Load() != 200 {
			w.WriteHeader(int(status.Load()))
			return
		}
		w.Write([]byte(`{"copilot_plan":"individual","quota_snapshots":{"chat":{"unlimited":true},"completions":{"unlimited":true},"premium_interactions":{"has_quota":true,"entitlement":300,"quota_remaining":300}}}`))
	}))
	defer srv.Close()
	old := CopilotUserURL
	CopilotUserURL = srv.URL
	defer func() { CopilotUserURL = old }()

	status.Store(503)
	if q := loginQuota(context.Background(), Login{Agent: "copilot", User: "hubot"}); q.Error == "" || q.Error == copilotLapsedText {
		t.Fatalf("503: %+v", q)
	}
	if l := copilotLapsedOf(t, "hubot"); l != "" {
		t.Fatalf("503 signed the account out: %q", l)
	}
	status.Store(401)
	if q := loginQuota(context.Background(), Login{Agent: "copilot", User: "hubot"}); q.Error != copilotLapsedText {
		t.Fatalf("401: %+v", q)
	}
	if l := copilotLapsedOf(t, "hubot"); l != copilotLapsedText {
		t.Fatalf("401, the account reads %q", l)
	}
	if l := copilotLapsedOf(t, "octocat"); l != "" {
		t.Fatalf("the editors' octocat reads %q", l)
	}
	status.Store(200)
	if q := loginQuota(context.Background(), Login{Agent: "copilot", User: "hubot"}); q.Error != "" {
		t.Fatalf("200: %+v", q)
	}
	if l := copilotLapsedOf(t, "hubot"); l != "" {
		t.Fatalf("read again, the account reads %q", l)
	}
}

// The Copilot CLI's own sign-in revoked (401 from GitHub): its account is
// signed out, and signed in again from magpie the new token is kept and
// used in its place, the account listed once.
func TestCopilotCLIRefusedSignsOut(t *testing.T) {
	home := signIn(t)
	os.Remove(filepath.Join(home, ".config", "github-copilot", "apps.json"))
	cli := filepath.Join(home, "copilot-home")
	t.Setenv("COPILOT_HOME", cli)
	os.MkdirAll(cli, 0o700)
	os.WriteFile(filepath.Join(cli, "config.json"), []byte(`{"lastLoggedInUser":{"host":"https://github.com","login":"octocat"},"copilotTokens":{"https://github.com:octocat":"gho_cli"}}`), 0o600)
	var refused atomic.Bool
	user := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "token gho_cli" && refused.Load() {
			w.WriteHeader(401)
			w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"endpoints": map[string]string{"api": "https://api.githubcopilot.com"}})
	}))
	defer user.Close()
	exchange := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"token": "tid", "expires_at": time.Now().Add(time.Hour).Unix()})
	}))
	defer exchange.Close()
	oldUser, oldToken := CopilotUserURL, CopilotTokenURL
	CopilotUserURL, CopilotTokenURL = user.URL, exchange.URL
	defer func() { CopilotUserURL, CopilotTokenURL = oldUser, oldToken }()
	resetCopilotState()
	t.Cleanup(resetCopilotState)

	if err := copilotTrade(t, "octocat"); err != nil {
		t.Fatal(err)
	}
	refused.Store(true)
	if err := copilotTrade(t, "octocat"); err == nil {
		t.Fatal("refused: no error")
	}
	if l := copilotLapsedOf(t, "octocat"); l != copilotLapsedText {
		t.Fatalf("refused, the CLI's account reads %q", l)
	}
	// a restart forgets the refusal; the sign-in asks GitHub again
	resetCopilotState()
	copilotProbeEditor(context.Background(), "octocat", "")
	if err := addCopilotLogin("octocat", "Pro", "gho_magpie", ""); err != nil {
		t.Fatal(err)
	}
	if l := copilotLapsedOf(t, "octocat"); l != "" {
		t.Fatalf("signed in again, the account reads %q", l)
	}
	n := 0
	for _, c := range copilotLogins(copilotConfigDir()) {
		if strings.EqualFold(c.User, "octocat") {
			n++
			if c.app.Token != "gho_magpie" {
				t.Fatalf("signed in again, the account sends %q", c.app.Token)
			}
		}
	}
	if n != 1 {
		t.Fatalf("octocat listed %d times", n)
	}
	if err := copilotTrade(t, "octocat"); err != nil {
		t.Fatalf("the new sign-in: %v", err)
	}
}
