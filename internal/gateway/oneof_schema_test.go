package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A tool schema's oneOf goes to Cline, Kilo and OpenRouter as anyOf: the
// grammar compiler some of their models are served behind refuses oneOf
// (ModelRun, 400 "unsupported schema keyword: oneOf", H20 on Discord with
// Codex's automation_update tool) and takes anyOf. A property called oneOf,
// a description that says it, and the rest of the body are left as they
// were; anyone else is sent oneOf.
func TestToolOneOfAsAnyOf(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cline, err := provider.FromPreset("clinepass")
	if err != nil {
		t.Fatal(err)
	}
	cline.ID, cline.Key, cline.Models = "cline", "k", []string{"m"}
	for _, p := range []provider.Provider{cline,
		{ID: "or", Name: "OpenRouter", Key: "k", Chat: "https://openrouter.ai/api/v1", Models: []string{"m"}},
		{ID: "other", Name: "Other", Key: "k", Chat: "https://other.test/v1", Models: []string{"m"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string]string{}
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		got[r.URL.Host] = string(b)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"id":"c","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))}, nil
	})}
	tools := `"tools":[{"type":"function","function":{"name":"automation_update","description":"takes \"oneOf\": a change","parameters":{"type":"object","properties":{` +
		`"oneOf":{"type":"string"},` +
		`"change":{"oneOf" : [{"type":"object","properties":{"paused":{"type":"boolean"}}},{"type":"object","properties":{"rule":{"oneOf":[{"type":"string"},{"type":"null"}]}}}]},` +
		`"list":{"type":"array","items":{"oneOf":[{"type":"string"},{"type":"integer"}]}}}}}}]`
	for _, id := range []string{"cline", "or", "other"} {
		body := `{"model":"` + id + `/m","messages":[{"role":"user","content":"hi"}],` + tools + `}`
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", id, rec.Code, rec.Body)
		}
	}
	want := strings.NewReplacer(`"change":{"oneOf"`, `"change":{"anyOf"`, `"rule":{"oneOf"`, `"rule":{"anyOf"`, `"items":{"oneOf"`, `"items":{"anyOf"`).Replace(tools)
	for _, host := range []string{"api.cline.bot", "openrouter.ai"} {
		if b := got[host]; !strings.Contains(b, want) {
			t.Errorf("%s was sent:\n%s\nwant its tools:\n%s", host, b, want)
		}
	}
	if b := got["other.test"]; !strings.Contains(b, tools) {
		t.Errorf("another provider was sent:\n%s", b)
	}
}
