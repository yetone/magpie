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

// A custom provider's Gemini API (#1346, NagaseMinato), behind a relay's
// path prefix: each client's request, streamed or not, goes to
// models/{model}:streamGenerateContent?alt=sse under the base, with the
// key in x-goog-api-key and no model in the body, and the client is
// answered in its own API.
func TestCustomGeminiUpstream(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	type got struct {
		uri, key, auth string
		body           []byte
	}
	var seen []got
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, got{r.URL.RequestURI(), r.Header.Get("x-goog-api-key"), r.Header.Get("Authorization"), b})
		if !strings.HasPrefix(r.URL.Path, "/proxy/v1beta/models/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"hello-"}]}}],"modelVersion":"gemini-2.5-pro"}`,
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"from-gemini"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10}}`,
		))
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "gem", Name: "Gem", Key: "AIza-test", Gemini: up.URL + "/proxy/v1beta", Models: []string{"gemini-2.5-pro"}}); err != nil {
		t.Fatal(err)
	}
	check := func(what string) {
		t.Helper()
		if len(seen) == 0 {
			t.Fatalf("%s: nothing sent upstream", what)
		}
		g := seen[len(seen)-1]
		if g.uri != "/proxy/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse" {
			t.Errorf("%s: asked %s", what, g.uri)
		}
		if g.key != "AIza-test" || g.auth != "" {
			t.Errorf("%s: signed key=%q auth=%q", what, g.key, g.auth)
		}
		if gjson.GetBytes(g.body, "model").Exists() || gjson.GetBytes(g.body, "stream").Exists() {
			t.Errorf("%s: body %s", what, g.body)
		}
		if !strings.Contains(gjson.GetBytes(g.body, "contents").Raw, "hi there") {
			t.Errorf("%s: contents %s", what, g.body)
		}
	}

	code, body := post(t, "/v1/chat/completions", `{"model":"gem/gemini-2.5-pro","messages":[{"role":"user","content":"hi there"}]}`)
	if code != 200 || gjson.Get(body, "choices.0.message.content").String() != "hello-from-gemini" || gjson.Get(body, "usage.prompt_tokens").Int() != 7 {
		t.Fatalf("chat: %d %s", code, body)
	}
	check("chat")

	code, body = post(t, "/v1/chat/completions", `{"model":"gem/gemini-2.5-pro","stream":true,"messages":[{"role":"user","content":"hi there"}]}`)
	if code != 200 || !strings.Contains(body, `"content":"hello-"`) || !strings.Contains(body, `"content":"from-gemini"`) || !strings.Contains(body, "[DONE]") {
		t.Fatalf("chat stream: %d %s", code, body)
	}
	check("chat stream")

	code, body = post(t, "/v1/messages", `{"model":"gem/gemini-2.5-pro","max_tokens":64,"messages":[{"role":"user","content":"hi there"}]}`)
	if code != 200 || !strings.Contains(body, "hello-from-gemini") {
		t.Fatalf("messages: %d %s", code, body)
	}
	check("messages")

	// a Gemini client's own request is relayed as it is
	gem := `{"contents":[{"role":"user","parts":[{"text":"hi there"}]}]}`
	code, body = post(t, "/v1beta/models/gem/gemini-2.5-pro:streamGenerateContent?alt=sse", gem)
	if code != 200 || !strings.Contains(body, "data:") || !strings.Contains(body, "from-gemini") {
		t.Fatalf("gemini stream: %d %s", code, body)
	}
	check("gemini stream")

	code, body = post(t, "/v1beta/models/gem/gemini-2.5-pro:generateContent", gem)
	if code != 200 || strings.HasPrefix(body, "data:") || !strings.Contains(body, "hello-from-gemini") {
		t.Fatalf("gemini json: %d %s", code, body)
	}
	check("gemini json")
}
