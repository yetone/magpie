package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
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

// A request to a model whose server locks temperature and top_p — Kimi
// Code's kimi-for-coding and k3*, kimi-k2.5 and later on either Kimi
// platform — goes without them: the whitelist refuses the ones clients
// send (Copilot's temperature 0.1, top_p 1, 400 "invalid temperature:
// only 1 is allowed for this model"), and left out the server fills in
// what the current model and mode take. A request that never named them
// stays without them; a model that takes a range (moonshot-v1-8k,
// kimi-k2-0905-preview, kimi-k2-thinking) keeps the user's own
// temperature; a relay's vendor-prefixed name (moonshotai/kimi-k2.6)
// still names the locked model; and anyone else is sent the body as it
// was.
func TestKimiLockedSampling(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	kimi, err := provider.FromPreset("kimi-code-cn")
	if err != nil {
		t.Fatal(err)
	}
	kimi.ID, kimi.Key, kimi.Models = "kimi", "k", []string{"kimi-for-coding", "k3"}
	moonshot, err := provider.FromPreset("moonshot-cn")
	if err != nil {
		t.Fatal(err)
	}
	moonshot.ID, moonshot.Key = "moonshot", "k"
	moonshot.Models = []string{"moonshot-v1-8k", "kimi-k2.5", "kimi-k2.6", "kimi-k2-0905-preview", "kimi-k2-thinking", "moonshotai/kimi-k2.6"}
	other := provider.Provider{ID: "other", Name: "Other", Key: "k", Chat: "https://other.test/v1", Models: []string{"m"}}
	for _, p := range []provider.Provider{kimi, moonshot, other} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	type sentBody struct{ host, path, body string }
	var sent []sentBody
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		sent = append(sent, sentBody{r.URL.Host, r.URL.Path, string(b)})
		if r.URL.Path == "/v1/messages" {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(`{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"id":"c","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))}, nil
	})}
	post := func(t *testing.T, path, body string) sentBody {
		t.Helper()
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return sent[len(sent)-1]
	}
	// the request VS Code Copilot's chat sends, refused 400 by Kimi Code
	t.Run("refused values are dropped", func(t *testing.T) {
		got := post(t, "/v1/chat/completions", `{"model":"kimi/kimi-for-coding","messages":[{"role":"user","content":"hi"}],"temperature":0.1,"top_p":1,"max_tokens":5}`)
		if gjson.Get(got.body, "temperature").Exists() || gjson.Get(got.body, "top_p").Exists() {
			t.Errorf("Kimi Code was sent the sampling fields: %s", got.body)
		}
		if !json.Valid([]byte(got.body)) {
			t.Errorf("Kimi Code was sent invalid JSON: %s", got.body)
		}
	})
	t.Run("absent stays absent", func(t *testing.T) {
		got := post(t, "/v1/chat/completions", `{"model":"kimi/kimi-for-coding","messages":[{"role":"user","content":"hi"}],"max_tokens":5}`)
		if gjson.Get(got.body, "temperature").Exists() || gjson.Get(got.body, "top_p").Exists() {
			t.Errorf("sampling fields were added: %s", got.body)
		}
	})
	t.Run("accepted values are dropped too", func(t *testing.T) {
		got := post(t, "/v1/chat/completions", `{"model":"kimi/kimi-for-coding","messages":[{"role":"user","content":"hi"}],"temperature":1,"top_p":0.95}`)
		if gjson.Get(got.body, "temperature").Exists() || gjson.Get(got.body, "top_p").Exists() {
			t.Errorf("Kimi Code was sent the sampling fields: %s", got.body)
		}
	})
	t.Run("kimi code's k3 drops the fields", func(t *testing.T) {
		got := post(t, "/v1/chat/completions", `{"model":"kimi/k3","messages":[{"role":"user","content":"hi"}],"temperature":0.1,"top_p":1}`)
		if got.host != "api.kimi.com" {
			t.Fatalf("the request went to %s", got.host)
		}
		if gjson.Get(got.body, "temperature").Exists() || gjson.Get(got.body, "top_p").Exists() {
			t.Errorf("k3 was sent the sampling fields: %s", got.body)
		}
	})
	t.Run("anthropic endpoint", func(t *testing.T) {
		got := post(t, "/v1/messages", `{"model":"kimi/kimi-for-coding","messages":[{"role":"user","content":"hi"}],"temperature":0.1,"top_p":1,"max_tokens":5}`)
		if got.host != "api.kimi.com" {
			t.Fatalf("the request went to %s", got.host)
		}
		if gjson.Get(got.body, "temperature").Exists() || gjson.Get(got.body, "top_p").Exists() {
			t.Errorf("Kimi Code was sent the sampling fields: %s", got.body)
		}
	})
	t.Run("moonshot pay-as-you-go kimi-k2.6 drops the fields", func(t *testing.T) {
		got := post(t, "/v1/chat/completions", `{"model":"moonshot/kimi-k2.6","messages":[{"role":"user","content":"hi"}],"temperature":0.1,"top_p":1}`)
		if got.host != "api.moonshot.cn" {
			t.Fatalf("the request went to %s", got.host)
		}
		if gjson.Get(got.body, "temperature").Exists() || gjson.Get(got.body, "top_p").Exists() {
			t.Errorf("kimi-k2.6 was sent the sampling fields: %s", got.body)
		}
	})
	t.Run("moonshot kimi-k2.5, the first locked version, drops the fields", func(t *testing.T) {
		got := post(t, "/v1/chat/completions", `{"model":"moonshot/kimi-k2.5","messages":[{"role":"user","content":"hi"}],"temperature":0.1,"top_p":1}`)
		if got.host != "api.moonshot.cn" {
			t.Fatalf("the request went to %s", got.host)
		}
		if gjson.Get(got.body, "temperature").Exists() || gjson.Get(got.body, "top_p").Exists() {
			t.Errorf("kimi-k2.5 was sent the sampling fields: %s", got.body)
		}
	})
	t.Run("a relay's vendor-prefixed name still names the locked model", func(t *testing.T) {
		got := post(t, "/v1/chat/completions", `{"model":"moonshot/moonshotai/kimi-k2.6","messages":[{"role":"user","content":"hi"}],"temperature":0.1,"top_p":1}`)
		if got.host != "api.moonshot.cn" {
			t.Fatalf("the request went to %s", got.host)
		}
		if gjson.Get(got.body, "temperature").Exists() || gjson.Get(got.body, "top_p").Exists() {
			t.Errorf("moonshotai/kimi-k2.6 was sent the sampling fields: %s", got.body)
		}
	})
	t.Run("moonshot pay-as-you-go keeps the user's temperature", func(t *testing.T) {
		got := post(t, "/v1/chat/completions", `{"model":"moonshot/moonshot-v1-8k","messages":[{"role":"user","content":"hi"}],"temperature":0.1,"top_p":1}`)
		if got.host != "api.moonshot.cn" {
			t.Fatalf("the request went to %s", got.host)
		}
		if v := gjson.Get(got.body, "temperature"); !v.Exists() || v.Raw != "0.1" {
			t.Errorf("Moonshot's own temperature was not kept: %s", got.body)
		}
	})
	t.Run("moonshot kimi-k2-0905-preview keeps the user's temperature", func(t *testing.T) {
		got := post(t, "/v1/chat/completions", `{"model":"moonshot/kimi-k2-0905-preview","messages":[{"role":"user","content":"hi"}],"temperature":0.1}`)
		if got.host != "api.moonshot.cn" {
			t.Fatalf("the request went to %s", got.host)
		}
		if v := gjson.Get(got.body, "temperature"); !v.Exists() || v.Raw != "0.1" {
			t.Errorf("kimi-k2-0905-preview's temperature was not kept: %s", got.body)
		}
	})
	t.Run("moonshot kimi-k2-thinking keeps the user's temperature", func(t *testing.T) {
		got := post(t, "/v1/chat/completions", `{"model":"moonshot/kimi-k2-thinking","messages":[{"role":"user","content":"hi"}],"temperature":0.1}`)
		if got.host != "api.moonshot.cn" {
			t.Fatalf("the request went to %s", got.host)
		}
		if v := gjson.Get(got.body, "temperature"); !v.Exists() || v.Raw != "0.1" {
			t.Errorf("kimi-k2-thinking's temperature was not kept: %s", got.body)
		}
	})
	t.Run("another provider is sent the body as it was", func(t *testing.T) {
		got := post(t, "/v1/chat/completions", `{"model":"other/m","messages":[{"role":"user","content":"hi"}],"temperature":0.1,"top_p":1}`)
		if !strings.Contains(got.body, `"temperature":0.1,"top_p":1`) {
			t.Errorf("another provider was sent: %s", got.body)
		}
	})
}
