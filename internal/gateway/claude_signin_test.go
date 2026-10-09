package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Claude Code signed in to claude.ai, sending that sign-in as its key, is
// told a provider's 401 for magpie's credential as a 502 in magpie's words,
// so it doesn't renew a sign-in nobody refused, and a 403 the same where it
// says the OAuth token was revoked; nothing it is told mentions x-api-key,
// which it reads as "Not logged in" at any status. Recent calls keep the
// provider's status. Any other error keeps its status, and a request with
// magpie's key still gets the 401 as the provider said it.
func TestClaudeSignInNotToldItsSignInFailed(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"msg","type":"message","role":"assistant","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`}
	setup(t, provider.Anthropic, f)
	signIn := "Bearer sk-ant-oat01-" + strings.Repeat("a", 24) // made up, of a sign-in's shape
	s := New()
	send := func(path, auth string) *httptest.ResponseRecorder {
		t.Helper()
		restingUntil.Lock()
		restingUntil.m = map[string]time.Time{} // the provider is asked each time
		restingUntil.Unlock()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(`{"model":"m1","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("User-Agent", "claude-cli/2.1.290 (external, cli)")
		req.Header.Set("Anthropic-Version", "2023-06-01")
		req.Header.Set("Authorization", auth)
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	const (
		badKey  = `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`
		revoked = `{"type":"error","error":{"type":"permission_error","message":"OAuth token has been revoked. Please obtain a new token."}}`
		denied  = `{"type":"error","error":{"type":"permission_error","message":"This organization has been disabled."}}`
		// as yetone's run of Claude Code 2.1.293 had them (#1218)
		keyDenied = `{"type":"error","error":{"type":"permission_error","message":"invalid x-api-key"}}`
		noKey     = `{"type":"error","error":{"type":"invalid_request_error","message":"x-api-key header is required"}}`
		limited   = `{"type":"error","error":{"type":"rate_limit_error","message":"rate limited for this x-api-key"}}`
	)
	for _, c := range []struct {
		name, path, auth string
		code             int
		reply, said      string
		want, recorded   int
	}{
		{"401", "/v1/messages", signIn, 401, badKey, "Fake refused magpie's credential (HTTP 401): invalid API key", 502, 401},
		{"401 without v1", "/messages", signIn, 401, badKey, "Fake refused magpie's credential (HTTP 401): invalid API key", 502, 401},
		{"revoked", "/v1/messages", signIn, 403, revoked, "Fake refused magpie's credential (HTTP 403): OAuth token has been revoked", 502, 403},
		{"another 403", "/v1/messages", signIn, 403, denied, "has been disabled", 403, 403},
		{"403 naming the key", "/v1/messages", signIn, 403, keyDenied, "invalid API key", 403, 403},
		{"400 naming the key", "/v1/messages", signIn, 400, noKey, "API key header is required", 400, 400},
		{"429 naming the key", "/v1/messages", signIn, 429, limited, "rate limited for this API key", 429, 429},
		{"401 counting", "/v1/messages/count_tokens", signIn, 401, badKey, "refused magpie's credential (HTTP 401): invalid API key", 502, 0},
		{"magpie's key", "/v1/messages", "Bearer " + Token, 401, badKey, "invalid x-api-key", 401, 401},
		{"an API key", "/v1/messages", "Bearer sk-ant-api03-" + strings.Repeat("b", 24), 401, badKey, "invalid x-api-key", 401, 401},
	} {
		code, reply := c.code, c.reply
		f.refuse = func([]byte) (int, string) { return code, reply }
		rec := send(c.path, c.auth)
		if rec.Code != c.want || !strings.Contains(rec.Body.String(), c.said) {
			t.Errorf("%s: %d %s, want %d saying %q", c.name, rec.Code, rec.Body.String(), c.want, c.said)
		}
		if c.auth == signIn && strings.Contains(strings.ToLower(rec.Body.String()), "x-api-key") {
			t.Errorf("%s: told %s, which Claude Code reads as its sign-in refused", c.name, rec.Body.String())
		}
		if c.want == 502 {
			var e struct {
				Type  string
				Error struct{ Type, Message string }
			}
			if json.Unmarshal(rec.Body.Bytes(), &e) != nil || e.Type != "error" || e.Error.Type != "api_error" || strings.Contains(strings.ToLower(e.Error.Message), "x-api-key") {
				t.Errorf("%s: told %s, want an api_error that doesn't mention x-api-key", c.name, rec.Body.String())
			}
		}
		if c.recorded != 0 {
			if got := s.Recent()[0].Status; got != c.recorded {
				t.Errorf("%s: Recent calls has %d, want the provider's %d", c.name, got, c.recorded)
			}
		}
	}

	// a reply goes as it came, a stream as one
	f.refuse = nil
	if rec := send("/v1/messages", signIn); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"msg"`) {
		t.Errorf("reply: %d %s", rec.Code, rec.Body.String())
	}
	f.ctype, f.reply = "", sse(`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg","type":"message","role":"assistant","content":[],"usage":{"input_tokens":1}}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)
	if rec := send("/v1/messages", signIn); rec.Code != 200 || !strings.Contains(rec.Body.String(), "message_stop") {
		t.Errorf("stream: %d %s", rec.Code, rec.Body.String())
	}
}

// A 403 written with nothing after it still goes, once the handler is
// done, and a 401 as a 502; a status the handler flushes goes at once. A
// stream's error that mentions x-api-key, after its 200, says "API key".
func TestSignInWriterReleasesA403(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	r.Header.Set("Authorization", "Bearer sk-ant-oat01-x")
	rec := httptest.NewRecorder()
	keepsSignIn(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(403) })(rec, r)
	if rec.Code != 403 {
		t.Fatalf("%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	keepsSignIn(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) })(rec, r)
	if rec.Code != 502 {
		t.Fatalf("401 with no body: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	keepsSignIn(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("event: ping\ndata: {\"type\":\"ping\"}\n\n"))
		w.Write([]byte(`event: error` + "\n" + `data: {"type":"error","error":{"type":"api_error","message":"Fake: invalid X-Api-Key"}}` + "\n\n"))
	})(rec, r)
	if body := rec.Body.String(); rec.Code != 200 || strings.Contains(strings.ToLower(body), "x-api-key") || !strings.Contains(body, "Fake: invalid API key") || !strings.Contains(body, "ping") {
		t.Errorf("stream: %d %q", rec.Code, body)
	}
	rec = httptest.NewRecorder()
	keepsSignIn(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		w.(http.Flusher).Flush()
		if rec.Code != 403 || !rec.Flushed {
			t.Errorf("not sent on Flush: %d %v", rec.Code, rec.Flushed)
		}
		w.Write([]byte("OAuth token has been revoked"))
	})(rec, r)
	if rec.Code != 403 {
		t.Fatalf("%d", rec.Code)
	}
}
