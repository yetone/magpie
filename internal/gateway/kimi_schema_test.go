package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A tool schema with an enum and no type goes to Kimi with the type its
// values have: Kimi Code refused cua-driver's parse_visual_regions, 400
// "not a valid moonshot flavored json schema … At path
// 'properties.options.properties.kinds.anyOf.items': type is not defined"
// (#886), and the whole request failed. Values of mixed types, a schema
// with a type, a property called enum and the rest of the body are left as
// they were; anyone else is sent the tools unchanged.
func TestKimiToolEnumTypes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	kimi, err := provider.FromPreset("kimi-code-cn")
	if err != nil {
		t.Fatal(err)
	}
	kimi.ID, kimi.Key, kimi.Models = "kimi", "k", []string{"m"}
	for _, p := range []provider.Provider{kimi,
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
	tools := `"tools":[{"type":"function","function":{"name":"parse_visual_regions","parameters":{"type":"object","properties":{"options":{"type":"object","properties":{` +
		`"kinds":{"anyOf":[{"items":{"enum":["text","icon"]},"maxItems":2,"type":"array"},{"type":"null"}]},` +
		`"level":{"enum":[1,2,3]},"scale":{"enum":[1,1.5]},"on":{"const":true},` +
		`"mixed":{"enum":["a",1]},"maybe":{"enum":["a",null]},"typed":{"type":"string","enum":["x"]},` +
		`"enum":{"type":"string"}}}},"$defs":{"mode":{ "enum" : ["fast","slow"]}}}}}]`
	for _, id := range []string{"kimi", "other"} {
		body := `{"model":"` + id + `/m","messages":[{"role":"user","content":"hi"}],` + tools + `}`
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", id, rec.Code, rec.Body)
		}
	}
	want := strings.NewReplacer(
		`"items":{"enum"`, `"items":{"type":"string","enum"`,
		`"level":{"enum"`, `"level":{"type":"integer","enum"`,
		`"scale":{"enum"`, `"scale":{"type":"number","enum"`,
		`"on":{"const"`, `"on":{"type":"boolean","const"`,
		`"mode":{ "enum"`, `"mode":{"type":"string", "enum"`,
	).Replace(tools)
	b := got["api.kimi.com"]
	if !strings.Contains(b, want) {
		t.Errorf("Kimi was sent:\n%s\nwant its tools:\n%s", b, want)
	}
	if !json.Valid([]byte(b)) {
		t.Errorf("Kimi was sent invalid JSON:\n%s", b)
	}
	if b := got["other.test"]; !strings.Contains(b, tools) {
		t.Errorf("another provider was sent:\n%s", b)
	}
}

// The sampling parameters a chat client sends that Kimi Code's whitelist
// refuses (Copilot's temperature 0.1, top_p 1) are sent as the ones it
// accepts (1, 0.95); a request without them gets them; another provider is
// sent the body as it was.
func TestKimiSamplingParams(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	kimi, err := provider.FromPreset("kimi-code-cn")
	if err != nil {
		t.Fatal(err)
	}
	kimi.ID, kimi.Key, kimi.Models = "kimi", "k", []string{"m"}
	other := provider.Provider{ID: "other", Name: "Other", Key: "k", Chat: "https://other.test/v1", Models: []string{"m"}}
	for _, p := range []provider.Provider{kimi, other} {
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
	bodies := map[string]string{
		"refused":  `{"model":"kimi/m","messages":[{"role":"user","content":"hi"}],"temperature":0.1,"top_p":1}`,
		"absent":   `{"model":"kimi/m","messages":[{"role":"user","content":"hi"}]}`,
		"accepted": `{"model":"kimi/m","messages":[{"role":"user","content":"hi"}],"temperature":1,"top_p":0.95}`,
		"other":    `{"model":"other/m","messages":[{"role":"user","content":"hi"}],"temperature":0.1,"top_p":1}`,
	}
	for name, body := range bodies {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	b := got["api.kimi.com"]
	for _, want := range []string{`"temperature":1`, `"top_p":0.95`} {
		if !strings.Contains(b, want) {
			t.Errorf("Kimi was sent %s without %s", b, want)
		}
	}
	if strings.Contains(b, `"temperature":0.1`) || strings.Contains(b, `"top_p":1`) {
		t.Errorf("Kimi was sent the refused values: %s", b)
	}
	if !json.Valid([]byte(b)) {
		t.Errorf("Kimi was sent invalid JSON: %s", b)
	}
	if b := got["other.test"]; !strings.Contains(b, `"temperature":0.1,"top_p":1`) {
		t.Errorf("another provider was sent: %s", b)
	}
}
