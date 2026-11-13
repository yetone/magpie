package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A Gemini compatible URL saved as a relay's bare host is kept as typed
// (cc0d9c8b0, JNDliuliuM on X), and asked as Google's SDKs ask one, at
// its /v1beta (equinox0253 on X: WandAPI's https://api.wandapi.ai under
// "Gemini 兼容", Claude Code's /v1/messages for gemini-3.8-flash went to
// /models/gemini-3.8-flash:streamGenerateContent with no version). A base
// with a path of its own is asked as typed.
func TestGeminiBareHostAskedAtV1beta(t *testing.T) {
	for _, c := range []struct{ name, base, want string }{
		{"bare host", "", "/v1beta/models/gemini-3.8-flash:streamGenerateContent?alt=sse"},
		{"bare host with a slash", "/", "/v1beta/models/gemini-3.8-flash:streamGenerateContent?alt=sse"},
		{"versioned", "/v1beta", "/v1beta/models/gemini-3.8-flash:streamGenerateContent?alt=sse"},
		{"a path of its own", "/gemini", "/gemini/models/gemini-3.8-flash:streamGenerateContent?alt=sse"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			var asked []string
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				asked = append(asked, r.URL.RequestURI())
				if r.URL.RequestURI() != c.want {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, sse(
					`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"flash ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`,
				))
			}))
			t.Cleanup(up.Close)
			if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Gemini: up.URL + c.base, Models: []string{"gemini-3.8-flash"}}); err != nil {
				t.Fatal(err)
			}
			code, body := post(t, "/v1/messages", `{"model":"relay/gemini-3.8-flash","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
			if code != 200 || !strings.Contains(body, "flash ok") {
				t.Fatalf("status %d, asked %v: %s", code, asked, body)
			}
			if len(asked) != 1 || asked[0] != c.want {
				t.Errorf("asked %v, want %s", asked, c.want)
			}
		})
	}
}
