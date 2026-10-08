package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A relay that admits only its official client must see the same client
// headers after both gateways, on Messages, Responses and Chat Completions.
func TestRemoteMagpieClientHeaders(t *testing.T) {
	for _, tc := range []struct {
		name, path, body, agent, originator, reply string
		proto                                      provider.Protocol
	}{
		{"claude", "/v1/messages", `{"model":"office/relay/m1","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`,
			"claude-cli/2.1.0 (external, cli)", "", `{"id":"m","type":"message","role":"assistant","model":"m1","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`, provider.Anthropic},
		{"codex-responses", "/v1/responses", `{"model":"office/relay/m1","input":"hi","stream":false}`,
			"codex_cli_rs/0.159.2 (Mac OS 26.6.0; arm64) kitty", "codex_cli_rs", `{"id":"r","object":"response","status":"completed","output":[]}`, provider.Responses},
		{"codex-chat", "/v1/chat/completions", `{"model":"office/relay/m1","messages":[{"role":"user","content":"hi"}],"stream":false}`,
			"Codex Desktop/0.162.3 (Windows 10.0.26100; x86_64) unknown", "Codex Desktop", `{"id":"c","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`, provider.Chat},
		{"codex-originator", "/v1/chat/completions", `{"model":"office/relay/m1","messages":[{"role":"user","content":"hi"}],"stream":false}`,
			"client/1.0", "codex_exec", `{"id":"c","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`, provider.Chat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			headers := make(chan http.Header, 1)
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case headers <- r.Header.Clone():
				default:
				}
				if r.Header.Get("User-Agent") != tc.agent || r.Header.Get("originator") != tc.originator {
					http.Error(w, "official client required", http.StatusForbidden)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, tc.reply)
			}))
			t.Cleanup(relay.Close)
			p := provider.Provider{ID: "relay", Key: "relay-key", Models: []string{"m1"}}
			switch tc.proto {
			case provider.Anthropic:
				p.Anthropic = relay.URL
			case provider.Responses:
				p.Responses = relay.URL + "/v1"
			case provider.Chat:
				p.Chat = relay.URL + "/v1"
			}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			remote := httptest.NewServer(New().Handler())
			t.Cleanup(remote.Close)
			if err := provider.Save(provider.Provider{ID: "office", Preset: provider.RemoteMagpiePreset, Chat: remote.URL, Key: "remote-key"}); err != nil {
				t.Fatal(err)
			}
			office, _ := provider.Find("office")
			if _, err := office.Fetch(context.Background()); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
			req.Header.Set("User-Agent", tc.agent)
			req.Header.Set("originator", tc.originator)
			req.Header.Set("x-app", "cli")
			req.Header.Set("session_id", "client-session")
			rec := httptest.NewRecorder()
			New().Handler().ServeHTTP(rec, req)
			select {
			case h := <-headers:
				if h.Get("User-Agent") != tc.agent || h.Get("originator") != tc.originator {
					t.Errorf("relay client headers: %v", h)
				}
				if h.Get("Authorization") != "Bearer relay-key" || h.Get(AgentHeader) != "" || h.Get(ViaHeader) != "" {
					t.Errorf("relay authentication or caller headers: %v", h)
				}
			default:
				t.Error("relay received no request")
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: %d %s", tc.path, rec.Code, rec.Body.String())
			}
		})
	}
}
