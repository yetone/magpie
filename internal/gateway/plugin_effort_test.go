package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A plugin's fetch is told the effort a translated request asked
// (EffortHeader): Antigravity's plugin picks a model's level variant by it,
// as the body it gets says only what Gemini's thinkingConfig can and loses
// minimal and xhigh. The header reaches the plugin and never the vendor,
// and a request that asks no effort has none.
func TestPluginToldTheEffort(t *testing.T) {
	var mu sync.Mutex
	var got []http.Header
	pid := besideFake(t, "fakeco", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		got = append(got, r.Header.Clone())
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range []string{
			`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"fake-claude","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`{"type":"message_stop"}`,
		} {
			io.WriteString(w, "data: "+e+"\n\n")
		}
	}))
	for _, tc := range []struct{ name, field, want string }{
		{"xhigh", `,"reasoning_effort":"xhigh"`, "xhigh"},
		{"low", `,"reasoning_effort":"low"`, "low"},
		{"none asked", "", ""},
	} {
		mu.Lock()
		got = nil
		mu.Unlock()
		rec := httptest.NewRecorder()
		// Chat Completions to a model on Anthropic Messages: translated
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"`+pid+`/fake-claude","stream":false,"messages":[{"role":"user","content":"hi"}]`+tc.field+`}`))
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", tc.name, rec.Code, rec.Body)
		}
		mu.Lock()
		sent := slices.Clone(got)
		mu.Unlock()
		if len(sent) == 0 {
			t.Fatalf("%s: the vendor got nothing", tc.name)
		}
		for _, h := range sent {
			if v := h.Values(provider.EffortHeader); len(v) > 0 {
				t.Errorf("%s: the vendor got %s: %q", tc.name, provider.EffortHeader, v)
			}
			if e := h.Get("X-Saw-Effort"); e != tc.want {
				t.Errorf("%s: the plugin was told effort %q, want %q", tc.name, e, tc.want)
			}
		}
	}
}
