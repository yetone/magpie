package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// OpenCode Zen's free models answer 403 "OpenCode's free tier can only be
// used from within OpenCode" unless the request says it is OpenCode's: its
// User-Agent (1.18.0 or newer) and a session id of its form, as OpenCode
// sends them to a provider whose id starts with "opencode". Requests to
// OpenCode's gateway carry them, those to anyone else don't, and the body
// goes as the caller sent it either way.
func TestOpenCodeClientHeaders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, p := range []provider.Provider{
		{ID: "zen", Name: "Zen", Key: "public", Chat: "https://opencode.ai/zen/v1", Models: []string{"mimo-v2.6-flash-free"}},
		{ID: "other", Name: "Other", Key: "k", Chat: "https://other.test/v1", Models: []string{"mimo-v2.6-flash-free"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	type seen struct {
		h    http.Header
		body string
	}
	got := map[string][]seen{}
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		got[r.URL.Host] = append(got[r.URL.Host], seen{r.Header.Clone(), string(b)})
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"id":"c","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`))}, nil
	})}
	send := func(model, affinity string) {
		t.Helper()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"Say pong"}],`+
			`"tools":[{"type":"function","function":{"name":"bash","parameters":{"type":"object"}}},{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}]}`))
		req.Header.Set("User-Agent", "some-agent/1.0")
		if affinity != "" {
			req.Header.Set("x-session-affinity", affinity)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", model, rec.Code, rec.Body)
		}
	}
	send("zen/mimo-v2.6-flash-free", "")
	send("zen/mimo-v2.6-flash-free", "")
	send("zen/mimo-v2.6-flash-free", "ses_0f9e921e8001MExXod3RWVP3aU") // OpenCode's own goes as it is
	send("other/mimo-v2.6-flash-free", "")

	zen, other := got["opencode.ai"], got["other.test"]
	if len(zen) != 3 || len(other) != 1 {
		t.Fatalf("requests: %v", got)
	}
	id := regexp.MustCompile(`^(ses|msg)_[0-9a-f]{12}[0-9A-Za-z]{14}$`)
	for i, r := range zen {
		if ua := r.h.Get("User-Agent"); ua != "opencode/"+provider.OpenCodeVersion {
			t.Errorf("zen %d: User-Agent %q", i, ua)
		}
		if !id.MatchString(r.h.Get("x-opencode-session")) || !id.MatchString(r.h.Get("x-opencode-request")) ||
			!strings.HasPrefix(r.h.Get("x-opencode-session"), "ses_") || !strings.HasPrefix(r.h.Get("x-opencode-request"), "msg_") {
			t.Errorf("zen %d: session %q request %q", i, r.h.Get("x-opencode-session"), r.h.Get("x-opencode-request"))
		}
		if r.h.Get("x-opencode-client") != "cli" || r.h.Get("x-opencode-project") != "global" {
			t.Errorf("zen %d: client %q project %q", i, r.h.Get("x-opencode-client"), r.h.Get("x-opencode-project"))
		}
		if r.h.Get("Authorization") != "Bearer public" {
			t.Errorf("zen %d: Authorization %q", i, r.h.Get("Authorization"))
		}
		if r.body != other[0].body {
			t.Errorf("zen %d: body changed:\n%s\nvs\n%s", i, r.body, other[0].body)
		}
	}
	// one conversation, one session; each request its own id
	if zen[0].h.Get("x-opencode-session") != zen[1].h.Get("x-opencode-session") || zen[0].h.Get("x-opencode-request") == zen[1].h.Get("x-opencode-request") {
		t.Errorf("session %q %q, request %q %q", zen[0].h.Get("x-opencode-session"), zen[1].h.Get("x-opencode-session"),
			zen[0].h.Get("x-opencode-request"), zen[1].h.Get("x-opencode-request"))
	}
	if s := zen[2].h.Get("x-opencode-session"); s != "ses_0f9e921e8001MExXod3RWVP3aU" {
		t.Errorf("OpenCode's own session became %q", s)
	}
	for k := range other[0].h {
		if strings.HasPrefix(strings.ToLower(k), "x-opencode-") {
			t.Errorf("other provider got %s", k)
		}
	}
	if ua := other[0].h.Get("User-Agent"); strings.HasPrefix(ua, "opencode/") {
		t.Errorf("other provider got User-Agent %q", ua)
	}
}
