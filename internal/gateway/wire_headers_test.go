package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A recent call keeps its HTTP headers both ways (usrname1833 on Discord:
// 查看原始请求和原始响应的http头): the client's, what magpie sent upstream,
// and the head of what came back, every secret taken out by what the
// header is, the rest as it went.
func TestRecentCallHeadersBothWays(t *testing.T) {
	fresh(t)
	const upKey = "sk-upstream-0123456789abcdefghij"
	vendor := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Anthropic-Ratelimit-Tokens-Remaining", "9000")
		w.Header().Set("X-Request-Id", "req-vendor-77")
		archiveVendor{}.ServeHTTP(w, r) // and its Set-Cookie
	})
	serveOn(t, "fake", upKey, []string{"m1"}, vendor)
	s := New()
	archivePost(t, s)
	c := s.Recent()[0]
	h := c.Headers()
	if h == nil {
		t.Fatal("no headers kept")
	}
	data, _ := json.Marshal(h)
	for _, secret := range []string{upKey, "magpie-gateway-key", "another-secret", "cookie-secret", "abc123"} {
		if strings.Contains(string(data), secret) {
			t.Errorf("%s shown:\n%s", secret, data)
		}
	}
	// the client's, as it reached magpie
	if got := h.Request["User-Agent"]; len(got) != 1 || got[0] != "claude-cli/2.1.0" {
		t.Errorf("client's User-Agent %v", got)
	}
	if got := h.Request["Authorization"]; len(got) != 1 || got[0] == "" {
		t.Errorf("client's Authorization %v: masked, not gone", got)
	}
	if len(h.Upstream) != 1 {
		t.Fatalf("upstream %+v", h.Upstream)
	}
	u := h.Upstream[0]
	if u.Method != "POST" || !strings.HasSuffix(u.URL, "/v1/chat/completions") || u.Plugin != "" {
		t.Errorf("sent %s %s (plugin %q)", u.Method, u.URL, u.Plugin)
	}
	if got := u.Request["Authorization"]; len(got) != 1 || strings.Contains(got[0], upKey) {
		t.Errorf("upstream Authorization %v", got)
	}
	if http.Header(u.Request).Get("Content-Type") == "" {
		t.Errorf("upstream headers %v", u.Request)
	}
	if u.Status != 200 || u.Proto != "HTTP/1.1" || u.Error != "" {
		t.Errorf("answered %d %s %q", u.Status, u.Proto, u.Error)
	}
	resp := http.Header(u.Response)
	if resp.Get("X-Request-Id") != "req-vendor-77" || resp.Get("Anthropic-Ratelimit-Tokens-Remaining") != "9000" ||
		resp.Get("Content-Type") != "application/json" || resp.Get("Set-Cookie") == "" {
		t.Errorf("upstream response headers %v", resp)
	}
}

// Each upstream try is kept in the order it went, a retry on the next key
// too.
func TestRecentCallHeadersEachTry(t *testing.T) {
	fresh(t)
	k := &keyed{fail: map[string]int{"ka-first-secret": 429}}
	serveOn(t, "fake", "ka-first-secret", []string{"m1"}, k, "kb-second-secret")
	s := New()
	if code, body := postAs(t, s, "", `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	h := s.Recent()[0].Headers()
	if h == nil || len(h.Upstream) != 2 || h.Upstream[0].Status != 429 || h.Upstream[1].Status != 200 {
		t.Fatalf("tries %+v (vendor saw %v)", h, k.tried)
	}
	data, _ := json.Marshal(h)
	if strings.Contains(string(data), "first-secret") || strings.Contains(string(data), "second-secret") {
		t.Errorf("a key shown:\n%s", data)
	}
}

// A request handed to a plugin is kept as handed, under the plugin's name;
// past headerTriesMost, only counted; and a call whose context keeps no
// log (magpie's own) keeps nothing.
func TestPluginHandOffHeaders(t *testing.T) {
	l := &headerLog{in: http.Header{"Cookie": {"a=b"}}}
	ctx := t.Context()
	req := httptest.NewRequest("POST", "https://plugin.invalid/v1/messages", nil).WithContext(context.WithValue(ctx, headerLogKey{}, l))
	req.Header.Set("X-Api-Key", "plugin-key-secret-123")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	for range headerTriesMost + 3 {
		provider.SendsThroughPlugin("cursor", req)(&http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}}, nil)
	}
	h := l.masked()
	if len(h.Upstream) != headerTriesMost || h.More != 3 {
		t.Fatalf("%d kept, %d more", len(h.Upstream), h.More)
	}
	u := h.Upstream[0]
	if u.Plugin != "cursor" || u.Request["X-Api-Key"][0] == "plugin-key-secret-123" || u.Request["Anthropic-Version"][0] != "2023-06-01" ||
		u.Response["Content-Type"][0] != "text/event-stream" || h.Request["Cookie"][0] == "a=b" {
		t.Errorf("%+v", h)
	}
	// none kept for a request with no log
	bare := httptest.NewRequest("POST", "https://plugin.invalid/v1/messages", nil)
	provider.SendsThroughPlugin("cursor", bare)(nil, io.EOF)
	if (Call{}).Headers() != nil {
		t.Error("a call with no log has headers")
	}
}

// An address keeps no secret: a password, a key in its query or path.
func TestScrubURL(t *testing.T) {
	u, _ := url.Parse("https://user:pass-secret@generativelanguage.googleapis.com/v1beta/models/g:streamGenerateContent?alt=sse&key=AIzaSyA-0123456789abcdefghijklmnopqrstu#frag")
	got := scrubURL(u)
	for _, secret := range []string{"pass-secret", "user:", "AIzaSyA-0123456789abcdefghijklmnopqrstu", "frag"} {
		if strings.Contains(got, secret) {
			t.Errorf("%s in %s", secret, got)
		}
	}
	if !strings.Contains(got, "generativelanguage.googleapis.com/v1beta/models/g:streamGenerateContent") || !strings.Contains(got, "alt=sse") {
		t.Errorf("address lost: %s", got)
	}
}
