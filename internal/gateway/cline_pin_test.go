package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// The Upstream tick no longer writes providerOptions to the Cline API:
// since 2026-11-06 Cline answers a request carrying it with 403
// "client-supplied \"providerOptions\" model routing is not supported"
// (#5442, kvjxhjwentnwm and HK-bruc1: the same request and key, 403 with
// the field and 200 without). With the tick on, a ClinePass DeepSeek or
// GLM request goes as sent, on Chat and when an Anthropic or Responses
// request is built into one. glm-5.3-flash, which Cline sends through
// OpenRouter, keeps OpenRouter's top-level provider.only ["z-ai"], which
// Cline still takes (sdytman, 10-20). Its other makers' models, Cline's
// free ones, a provider that doesn't pin, and a body naming provider or
// providerOptions itself go as they were.
func TestClineTickSendsNoProviderOptions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cline, err := provider.FromPreset("clinepass")
	if err != nil {
		t.Fatal(err)
	}
	cline.ID, cline.Key, cline.PinUpstream = "cline", "clp_key", true
	cline.Models = []string{"deepseek/deepseek-v4-pro", "zai/glm-5.3", "cline-pass/deepseek-v4-pro", "cline-pass/glm-5.3", "cline-pass/glm-5.3-flash", "cline-pass/deepseek-v4.1-flash", "cline-pass/deepseek-v4.1-flash-1017", "cline-pass/kimi-k3", "cline-pass/qwen3.8-max", "cline-free/deepseek-v4.1-flash", "cline-free/glm-5.3-flash"}
	plain := cline
	plain.ID, plain.Name, plain.PinUpstream = "cline2", "ClinePass 2", false
	for _, p := range []provider.Provider{cline, plain} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	var sent []byte
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		sent, _ = io.ReadAll(r.Body)
		if gjson.GetBytes(sent, "stream").Bool() {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
				Body: io.NopCloser(strings.NewReader("data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"pong\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"id":"c","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`))}, nil
	})}
	ask := func(path, body string) string {
		t.Helper()
		sent = nil
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s %s: %d %s", path, body, rec.Code, rec.Body)
		}
		return gjson.GetBytes(sent, "providerOptions").Raw
	}
	orPin := func() string { return gjson.GetBytes(sent, "provider").Raw }
	chat := func(model, extra string) string {
		return ask("/v1/chat/completions", `{"model":"`+model+`","messages":[{"role":"user","content":"Say pong"}]`+extra+`}`)
	}
	// ClinePass's and the usage-billed models alike
	for _, m := range []string{"cline/deepseek/deepseek-v4-pro", "cline/zai/glm-5.3", "cline/cline-pass/deepseek-v4-pro", "cline/cline-pass/deepseek-v4.1-flash", "cline/cline-pass/deepseek-v4.1-flash-1017", "cline/cline-pass/glm-5.3"} {
		if got := chat(m, ""); got != "" || orPin() != "" {
			t.Errorf("%s with the tick: providerOptions %s provider %s, want neither (sent %s)", m, got, orPin(), sent)
		}
	}
	if got := ask("/v1/chat/completions", `{"model":"cline/cline-pass/deepseek-v4.1-flash","messages":[{"role":"user","content":[{"type":"text","text":"What is this?"},{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}]}]}`); got != "" {
		t.Errorf("an image: providerOptions %s, want none", got)
	}
	for _, m := range []string{"cline/cline-pass/deepseek-v4-pro", "cline/cline-pass/glm-5.3"} {
		if got := ask("/v1/messages", `{"model":"`+m+`","max_tokens":64,"messages":[{"role":"user","content":"Say pong"}]}`); got != "" || orPin() != "" {
			t.Errorf("messages, %s: providerOptions %s provider %s, want neither", m, got, orPin())
		}
		if got := ask("/v1/responses", `{"model":"`+m+`","input":"Say pong","stream":false}`); got != "" || orPin() != "" {
			t.Errorf("responses, %s: providerOptions %s provider %s, want neither", m, got, orPin())
		}
	}
	const zaiOR = `{"only":["z-ai"]}`
	const flash = `{"model":"cline-pass/glm-5.3-flash","messages":[{"role":"user","content":"Say pong"}]`
	if got := chat("cline/cline-pass/glm-5.3-flash", ""); got != "" || orPin() != zaiOR {
		t.Errorf("glm-5.3-flash: providerOptions %s provider %s, want provider %s alone (sent %s)", got, orPin(), zaiOR, sent)
	} else if !strings.HasPrefix(string(sent), flash) {
		// the pin goes after everything the client sent, so a prompt
		// cache keyed on the body's start still matches
		t.Errorf("glm-5.3-flash: the body's start changed: %s", sent)
	}
	if ask("/v1/messages", `{"model":"cline/cline-pass/glm-5.3-flash","max_tokens":64,"messages":[{"role":"user","content":"Say pong"}]}`); orPin() != zaiOR {
		t.Errorf("messages, glm-5.3-flash: provider %s, want %s (sent %s)", orPin(), zaiOR, sent)
	}
	for _, m := range []string{"cline/cline-pass/kimi-k3", "cline/cline-pass/qwen3.8-max", "cline/cline-free/deepseek-v4.1-flash", "cline/cline-free/glm-5.3-flash", "cline2/cline-pass/deepseek-v4-pro", "cline2/cline-pass/glm-5.3"} {
		if got := chat(m, ""); got != "" || orPin() != "" {
			t.Errorf("%s: providerOptions %s provider %s, want none", m, got, orPin())
		}
	}
	for _, m := range []string{"cline/cline-pass/deepseek-v4-pro", "cline/cline-pass/glm-5.3"} {
		// a second providerOptions after the client's would be the one a
		// JSON reader keeps, so the body must hold the client's alone
		if got := chat(m, `,"providerOptions":{"gateway":{"order":["novita"]}}`); got != `{"gateway":{"order":["novita"]}}` || strings.Count(string(sent), `"providerOptions"`) != 1 {
			t.Errorf("%s: the client's own providerOptions became %s (sent %s)", m, got, sent)
		}
	}
	// a client's own provider is kept as sent, and nothing is added beside it
	for _, m := range []string{"cline/cline-pass/glm-5.3-flash", "cline/cline-pass/deepseek-v4-pro"} {
		if got := chat(m, `,"provider":{"order":["novita"]}`); got != "" || orPin() != `{"order":["novita"]}` || strings.Count(string(sent), `"provider"`) != 1 {
			t.Errorf("%s: the client's own provider became %s, providerOptions %s (sent %s)", m, orPin(), got, sent)
		}
	}
	if got := chat("cline/cline-pass/glm-5.3-flash", `,"providerOptions":{"gateway":{"order":["novita"]}}`); got != `{"gateway":{"order":["novita"]}}` || orPin() != "" {
		t.Errorf("glm-5.3-flash: the client's own providerOptions became %s, provider %s (sent %s)", got, orPin(), sent)
	}
}
