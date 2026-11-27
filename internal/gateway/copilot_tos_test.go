package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// copilotToSBody is GitHub's 403 on the trade for a session token, word
// for word as yidasanqian's log has it.
const copilotToSBody = `{"message":"Resource not accessible by integration. For more on scraping GitHub and how it may affect your rights, please review our Terms of Service (https://docs.github.com/en/site-policy/github-terms/github-terms-of-service)","documentation_url":"https://docs.github.com/rest","status":"403"}`

// yidasanqian on X: GitHub turned a Copilot account away under its terms
// (copilotToSBody), and the gateway asked the same one again as a busy
// 502, trading the token anew each time: 15 trades and 37s a request,
// the account marked signed out. Now one request trades once, isn't asked
// again in place, says GitHub's words with magpie's hint, rests the
// account as restricted, and the next request asks GitHub nothing.
func TestCopilotToSRefusalAsksGitHubOnce(t *testing.T) {
	fresh(t)
	cfg := os.Getenv("XDG_CONFIG_HOME")
	os.MkdirAll(filepath.Join(cfg, "github-copilot"), 0o755)
	os.WriteFile(filepath.Join(cfg, "github-copilot", "apps.json"), mustJSON(map[string]any{
		"github.com:Iv1.x": map[string]any{"user": "yida", "oauth_token": "gho_yida_tos"},
	}), 0o600)
	var trades, sent atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		io.WriteString(w, `{"data":[]}`)
	}))
	defer api.Close()
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			trades.Add(1)
		}
		w.WriteHeader(403)
		io.WriteString(w, copilotToSBody)
	}))
	defer gh.Close()
	oldTok, oldUser := provider.CopilotTokenURL, provider.CopilotUserURL
	provider.CopilotTokenURL, provider.CopilotUserURL = gh.URL+"/token", gh.URL+"/user"
	defer func() { provider.CopilotTokenURL, provider.CopilotUserURL = oldTok, oldUser }()

	s := New()
	start := time.Now()
	code, body := postAs(t, s, "", `{"model":"copilot/claude-haiku-4.5","messages":[{"role":"user","content":"hi"}]}`)
	took := time.Since(start)
	if code < 400 {
		t.Fatalf("refused: %d %s", code, body)
	}
	if n := trades.Load(); n != 1 {
		t.Fatalf("one request traded the token %d times", n)
	}
	// asked again in place, it waited 1s, 2s… before each
	if took > 900*time.Millisecond {
		t.Fatalf("the request took %s", took)
	}
	var said struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &said); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	if !strings.Contains(said.Error.Message, "For more on scraping GitHub") || !strings.HasSuffix(said.Error.Message, provider.CopilotToSHint) {
		t.Fatalf("said: %s", body)
	}
	if strings.Contains(said.Error.Message, "sign in to Copilot again") {
		t.Fatalf("told to sign in again: %s", body)
	}
	for _, l := range provider.Logins("copilot") {
		if l.Lapsed != "" {
			t.Fatalf("marked signed out: %+v", l)
		}
	}
	code, body = postAs(t, s, "", `{"model":"copilot/claude-haiku-4.5","messages":[{"role":"user","content":"hi"}]}`)
	if code < 400 || !strings.Contains(body, "Terms of Service") {
		t.Fatalf("again: %d %s", code, body)
	}
	if n := trades.Load(); n != 1 {
		t.Fatalf("the next request traded the token again: %d in all", n)
	}
	if n := sent.Load(); n != 0 {
		t.Fatalf("%d requests reached Copilot's API", n)
	}
}

// The hint is magpie's reading of an account GitHub restricted: it isn't
// asked again in place as busy, and the provider's other accounts are
// still asked, as GitHub refused the account's token before the request
// was sent.
func TestCopilotToSHintRestsAsRestricted(t *testing.T) {
	body := []byte(`{"error":{"message":"copilot: GitHub refused this Copilot account (` + strings.ReplaceAll(`Resource not accessible by integration. For more on scraping GitHub and how it may affect your rights, please review our Terms of Service (https://docs.github.com/en/site-policy/github-terms/github-terms-of-service)`, `"`, `\"`) + `) — ` + provider.CopilotToSHint + `","type":"api_error"}}`)
	if _, ok := passing(502, nil, body, 0); ok {
		t.Fatal("asked again in place")
	}
	c := candidate{p: provider.Provider{ID: "copilot"}}
	if why := failureOf(c, 502, body); why != failRestricted {
		t.Fatalf("failed as %q", why)
	}
	if riskRefused(c, 502, nil, body) {
		t.Fatal("the provider's other accounts walled off")
	}
	if _, ok := passing(502, nil, []byte(`{"error":{"message":"upstream busy"}}`), 0); !ok {
		t.Fatal("a busy 502 isn't asked again")
	}
}
