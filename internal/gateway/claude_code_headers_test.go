package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A relay that serves only Claude Code (#179: packy's "only accessible via
// the official Claude CLI") gets Claude Code's own headers and body as it
// sent them, the provider's key in place of magpie's
func TestClaudeCodeHeadersReachAnthropicRelay(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"msg","type":"message","content":[]}`}
	up := setup(t, provider.Anthropic, f)
	var query string
	up.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		f.ServeHTTP(w, r)
	})
	claude := http.Header{
		"User-Agent":        {"claude-cli/2.1.0 (external, cli)"},
		"X-App":             {"cli"},
		"Anthropic-Version": {"2023-06-01"},
		"Anthropic-Beta":    {"claude-code-20250219,interleaved-thinking-2025-05-14"},
		"Anthropic-Dangerous-Direct-Browser-Access": {"true"},
		"X-Stainless-Lang":                          {"js"},
		"X-Stainless-Package-Version":               {"0.60.0"},
		"X-Stainless-Retry-Count":                   {"0"},
		"X-Claude-Code-Session-Id":                  {"ses-1"},
		"Authorization":                             {"Bearer " + Token},
		"X-Api-Key":                                 {Token},
	}
	body := `{"model":"m1","max_tokens":5,"system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}],"metadata":{"user_id":"user_abc_account__session_123"},"messages":[{"role":"user","content":"hi"}]}`
	send := func(path string, h http.Header) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		for k, v := range h {
			req.Header[k] = v
		}
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	}

	send("/v1/messages?beta=true", claude)
	for k, v := range claude {
		if k == "Authorization" || k == "X-Api-Key" {
			continue
		}
		if got := f.head.Get(k); got != v[0] {
			t.Errorf("%s: got %q, want %q", k, got, v[0])
		}
	}
	if a, k := f.head.Get("Authorization"), f.head.Get("X-Api-Key"); a != "Bearer k" || k != "k" {
		t.Errorf("auth: %q %q", a, k)
	}
	if query != "beta=true" {
		t.Errorf("query %q", query)
	}
	for _, want := range []string{`"You are Claude Code, Anthropic's official CLI for Claude."`, `"metadata":{"user_id":"user_abc_account__session_123"}`} {
		if !bytes.Contains(f.got, []byte(want)) {
			t.Errorf("body lost %s: %s", want, f.got)
		}
	}

	// a provider with no key of its own still never sees magpie's
	p, _ := provider.Find("fake")
	p.Key = ""
	provider.Save(*p)
	send("/v1/messages", claude)
	if a, k := f.head.Get("Authorization"), f.head.Get("X-Api-Key"); a != "" || k != "" {
		t.Errorf("magpie's key leaked: %q %q", a, k)
	}
	if f.head.Get("X-App") != "cli" {
		t.Errorf("x-app: %v", f.head)
	}

	// another agent's request goes as magpie's
	send("/v1/messages?beta=true", http.Header{"User-Agent": {"opencode/1.0"}, "X-App": {"other"}, "X-Stainless-Lang": {"js"}})
	if ua := f.head.Get("User-Agent"); !strings.HasPrefix(ua, "magpie/") || f.head.Get("X-App") != "" || f.head.Get("X-Stainless-Lang") != "" || query != "" {
		t.Errorf("other agent: %q %v %q", ua, f.head, query)
	}
}
