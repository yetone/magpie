package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A ClinePass reply that names a route of its own (vmc/…-contributor-
// fallbacks) and no provider leaves its try saying what was asked for, so
// the Routing page can tell "asked for, not said who answered" from
// "nothing asked for" (zRain on Discord, 10-24). Since 2026-11-06 Cline
// refuses providerOptions (#5442), so a DeepSeek request asks for nothing
// with the Upstream tick or without it, and the try doesn't say it asked
// DeepSeek; glm-5.3-flash, pinned by OpenRouter's provider.only, which
// Cline still takes, says z-ai with the tick. On a Chat client's path and
// on the Responses one Codex takes.
func TestClinePassTryKeepsWhatWasAsked(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
		for _, c := range []struct {
			model string
			tick  bool
			want  string
		}{
			{"cline-pass/deepseek-v4.1-flash-1017", false, ""},
			{"cline-pass/deepseek-v4.1-flash-1017", true, ""},
			{"cline-pass/glm-5.3-flash", false, ""},
			{"cline-pass/glm-5.3-flash", true, "z-ai"},
		} {
			model, tick, want := c.model, c.tick, c.want
			name := path + "/" + model + "/tick-off"
			if tick {
				name = path + "/" + model + "/tick-on"
			}
			t.Run(name, func(t *testing.T) {
				fresh(t)
				var sent string
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					b, _ := io.ReadAll(r.Body)
					sent = string(b)
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, `data: {"id":"g1","object":"chat.completion.chunk","model":"vmc/deepseek-v4p1-flash-1017-contributor-fallbacks","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}]}`+"\n\n")
					io.WriteString(w, `data: {"id":"g1","object":"chat.completion.chunk","model":"vmc/deepseek-v4p1-flash-1017-contributor-fallbacks","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`+"\n\n")
					io.WriteString(w, "data: [DONE]\n\n")
				}))
				t.Cleanup(up.Close)
				p := provider.Provider{ID: "clinepass", Name: "ClinePass", Preset: "clinepass", Key: "k", Chat: up.URL + "/v1",
					Models: []string{model}, PinUpstream: tick}
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
				body := `{"model":"clinepass/` + model + `","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
				if path == "/v1/responses" {
					body = `{"model":"clinepass/` + model + `","stream":true,"input":"hi"}`
				}
				s := New()
				rec := httptest.NewRecorder()
				s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
				if rec.Code != 200 {
					t.Fatalf("%d %s", rec.Code, rec.Body)
				}
				if strings.Contains(sent, `providerOptions`) {
					t.Errorf("providerOptions sent with the tick %v: %s", tick, sent)
				}
				if pinned := strings.Contains(sent, `"provider":{"only":["z-ai"]}`); pinned != (want != "") {
					t.Errorf("provider.only sent %v, want %v: %s", pinned, want != "", sent)
				}
				r := lastRoute(s)
				if len(r.Tries) != 1 {
					t.Fatalf("tries %+v", r.Tries)
				}
				tr := r.Tries[0]
				if tr.Pin != want || tr.Upstream != "" || !strings.HasPrefix(tr.Served, "vmc/") {
					t.Errorf("try pin %q (want %q), upstream %q, served %q", tr.Pin, want, tr.Upstream, tr.Served)
				}
			})
		}
	}
}
