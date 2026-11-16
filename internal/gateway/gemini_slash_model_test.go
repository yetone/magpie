package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A custom Gemini compatible provider's namespaced model id
// (example/model-demo) is asked at /models/example/model-demo, its slash
// kept, as a relay that routes by path segments takes it: escaped whole
// (example%2Fmodel-demo) such a relay answered 403 Invalid URL (#6695,
// LuoRuiyao on X). The raw RequestURI is checked, since the decoded path
// is the same either way.
func TestGeminiNamespacedModelKeepsItsSlash(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	const want = "/v1beta/models/example/model-demo:streamGenerateContent?alt=sse"
	var asked []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		asked = append(asked, r.RequestURI)
		if r.RequestURI != want {
			http.Error(w, `{"error":{"code":403,"message":"Invalid URL"}}`, http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"demo ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`,
		))
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Gemini: up.URL + "/v1beta", Models: []string{"example/model-demo"}}); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/messages", `{"model":"relay/example/model-demo","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "demo ok") {
		t.Fatalf("status %d, asked %v: %s", code, asked, body)
	}
	if len(asked) != 1 || asked[0] != want {
		t.Errorf("asked %v, want %s", asked, want)
	}
}
