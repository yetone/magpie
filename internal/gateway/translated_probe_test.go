package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// An Antigravity account's models can be tested (lc on Discord:
// "antigravity的测试按钮不可用，别的供应商正常"): Test models was off for
// every Google sign-in, which speaks Code Assist alone, an API the gateway
// builds each request for. The test is now that request, the smallest
// "hi" built by the translator in the account's envelope, and says what
// the vendor answered — a word, or its refusal.
func TestAntigravityModelTest(t *testing.T) {
	fresh(t)
	dir := filepath.Dir(provider.Path())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	logins := []map[string]any{{"agent": "antigravity", "user": "u@example.com", "on": true,
		"auth": map[string]any{"access_token": "tok", "refresh_token": "ref", "project": "p1", "expiry_date": time.Now().Add(time.Hour).UnixMilli()}}}
	if err := os.WriteFile(filepath.Join(dir, "logins.json"), mustJSON(logins), 0o600); err != nil {
		t.Fatal(err)
	}
	old := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		var body string
		switch {
		case strings.HasSuffix(r.URL.Path, "latest-arm64-mac.yml"):
			body = "version: 2.9.1\n"
		case strings.Contains(r.URL.Path, ":loadCodeAssist"):
			body = `{"currentTier":{"id":"standard-tier"},"cloudaicompanionProject":"p1"}`
		case strings.Contains(r.URL.Path, ":fetchAvailableModels"):
			body = `{"models":{"gemini-3-flash":{}}}`
		default:
			return nil, fmt.Errorf("unexpected request to %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = old })

	var asked []string
	var say func(w http.ResponseWriter)
	oldServer := probeServer
	probeServer = func() *Server {
		s := New()
		s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
			b, _ := io.ReadAll(r.Body)
			asked = append(asked, r.URL.Path+" "+string(b))
			rec := httptest.NewRecorder()
			say(rec)
			return rec.Result(), nil
		})}
		return s
	}
	t.Cleanup(func() { probeServer = oldServer })

	p, err := provider.Find("antigravity")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(t.Context()); err != nil {
		t.Fatal(err)
	}
	if why := p.ModelTest(); why != "" {
		t.Fatalf("Antigravity's models can't be tested: %q", why)
	}

	say = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"Hi\"}]}}]}}\n\n")
	}
	r := p.TestModels(t.Context(), []string{"gemini-3-flash"})[0]
	if !r.OK || r.Protocol != provider.CodeAssist || r.Account != "u@example.com" {
		t.Fatalf("test: %+v", r)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], ":streamGenerateContent") || !strings.Contains(asked[0], `"project":"p1"`) || !strings.Contains(asked[0], `"hi"`) {
		t.Errorf("asked: %q", asked)
	}
	// the provider's own Test, as the TUI's
	if rs := p.Test(t.Context()); len(rs) != 1 || !rs[0].OK || rs[0].Model != "gemini-3-flash" {
		t.Errorf("provider test: %+v", rs)
	}

	say = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":{"code":403,"message":"The caller does not have permission","status":"PERMISSION_DENIED"}}`)
	}
	r = p.TestModels(t.Context(), []string{"gemini-3-flash"})[0]
	if r.OK || r.Status != 403 || !strings.Contains(r.Error, "does not have permission") {
		t.Errorf("refused: %+v", r)
	}

	// an account Google wants verified: the test says Google's link whole
	const link = "https://accounts.google.com/signin/continue?sarp=1&scc=1&continue=https://developers.google.com/gemini-code-assist/auth/auth_success_gemini&plt=AKgnsbvK-3x_Yq9Zt0lPq2vN7m8w&flowName=GlifWebSignIn&authuser=0"
	say = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":{"code":403,"message":"Verify your account to continue.","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"VALIDATION_REQUIRED","domain":"cloudcode-pa.googleapis.com","metadata":{"validation_url":"`+strings.ReplaceAll(link, "&", `&`)+`"}}]}}`)
	}
	r = p.TestModels(t.Context(), []string{"gemini-3-flash"})[0]
	if r.OK || r.Status != 403 || !strings.Contains(r.Error, " open "+link+" in a browser") {
		t.Errorf("verify: %+v", r)
	}
}
