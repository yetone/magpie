package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// An international preset reaches the selected Chat endpoint with that
// account's key, for ordinary and streamed requests from the local gateway.
func TestSiliconFlowRegionalChat(t *testing.T) {
	for _, c := range []struct{ region, host string }{
		{"cn", "api.siliconflow.cn"}, {"intl", "api.siliconflow.com"},
	} {
		t.Run(c.region, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model  string `json:"model"`
					Stream bool   `json:"stream"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if r.Header.Get("X-Test-Host") != c.host || r.Header.Get("Authorization") != "Bearer sk-"+c.region || r.URL.Path != "/v1/chat/completions" || body.Model != "Qwen/Qwen2.5-7B-Instruct" {
					t.Errorf("wrong upstream: %s %v %+v", r.URL, r.Header, body)
					http.Error(w, "wrong regional request", http.StatusBadRequest)
					return
				}
				if body.Stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"id\":\"chat-test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"chat-test","object":"chat.completion","model":"Qwen/Qwen2.5-7B-Instruct","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
			}))
			defer up.Close()
			at, _ := url.Parse(up.URL)
			s := New()
			s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
				r = r.Clone(r.Context())
				u := *r.URL
				r.Header.Set("X-Test-Host", u.Host)
				u.Scheme, u.Host = at.Scheme, at.Host
				r.URL, r.Host = &u, ""
				return up.Client().Transport.RoundTrip(r)
			})}
			p, err := provider.ParseImport("magpie://import?preset=siliconflow&region=" + c.region + "&key=sk-" + c.region)
			if err != nil {
				t.Fatal(err)
			}
			p.Models = []string{"Qwen/Qwen2.5-7B-Instruct"}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			for _, stream := range []bool{false, true} {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"siliconflow/Qwen/Qwen2.5-7B-Instruct","messages":[{"role":"user","content":"hi"}],"stream":%t}`, stream)))
				s.Handler().ServeHTTP(rec, req)
				code, body := rec.Code, rec.Body.String()
				if code != http.StatusOK || !strings.Contains(body, "hello") || stream && !strings.Contains(body, "[DONE]") {
					t.Fatalf("stream=%v: %d %s", stream, code, body)
				}
			}
		})
	}
}
