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

// A plugin's fetch is told the session the agent named and the Claude Code
// subagent a request is of (SessionToPluginHeader, SubagentToPluginHeader):
// the agent's own headers never reach a plugin, so Devin's cache keepalive
// keyed Claude Code Auto's permission check by its text alone, and a check
// whose history grew kept its older pings running beside the new ones
// (magpie-community/plugins#620, zelong77). The headers reach the plugin
// and never the vendor; a request that names no session has neither.
func TestPluginToldTheSession(t *testing.T) {
	var mu sync.Mutex
	var got []http.Header
	pid := besideFake(t, "fakeco", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		got = append(got, r.Header.Clone())
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if r.URL.Path != "/v1/messages" {
			io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}`+"\n\n")
			io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
			return
		}
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
	// to a model on Anthropic Messages, relayed, and to one on Chat,
	// translated, unstreamed as Claude Code's permission check asks
	for _, model := range []string{"fake-claude", "fake-1"} {
		for _, tc := range []struct {
			name              string
			headers           map[string]string
			session, subagent string // "" for not told
		}{
			{"claude code's lead", map[string]string{"X-Claude-Code-Session-Id": "cc-s1"}, "cc-s1", ""},
			{"claude code's subagent", map[string]string{"X-Claude-Code-Session-Id": "cc-s1", "X-Claude-Code-Agent-Id": "agent-7"}, "cc-s1", "agent-7"},
			{"magpie's own session header first", map[string]string{"X-Magpie-Session": "mine", "X-Claude-Code-Session-Id": "cc-s1"}, "mine", ""},
			{"opencode", map[string]string{"X-Opencode-Session": "ses_1"}, "ses_1", ""},
			{"no session named", nil, "", ""},
		} {
			tc.name = model + ", " + tc.name
			mu.Lock()
			got = nil
			mu.Unlock()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"`+pid+`/`+model+`","max_tokens":64,"stream":false,"messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set("anthropic-version", "2023-06-01")
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
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
				for _, k := range []string{provider.SessionToPluginHeader, provider.SubagentToPluginHeader} {
					if v := h.Values(k); len(v) > 0 {
						t.Errorf("%s: the vendor got %s: %q", tc.name, k, v)
					}
				}
				if s, a := h.Get("X-Saw-Session"), h.Get("X-Saw-Subagent"); s != tc.session || a != tc.subagent {
					t.Errorf("%s: the plugin was told session %q, subagent %q; want %q, %q", tc.name, s, a, tc.session, tc.subagent)
				}
			}
		}
	}
}
