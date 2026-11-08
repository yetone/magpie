package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// Claude Code's and Codex's requests, through this magpie, reach a Remote
// magpie as magpie's own (linkaww: #179's copy of Claude Code's headers put
// claude-cli/… over magpie/…, so the remote, which takes X-Magpie-* from a
// magpie alone, dropped the cache lifetime, the agent, the computer, the
// hops and the agent's User-Agent). The remote takes each of them, and
// still sends its relay the agent's own client headers (#179) and ?beta=true.
func TestRemoteMagpieTakesClientsHeaders(t *testing.T) {
	for _, tc := range []struct {
		name, path, body, ua, originator, agent, ttl, reply string
		proto                                               provider.Protocol
	}{
		{"claude", "/v1/messages?beta=true", `{"model":"office/relay/m1","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`,
			"claude-cli/2.1.319 (external, cli)", "", "claude", "5m",
			`{"id":"m","type":"message","role":"assistant","model":"m1","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`, provider.Anthropic},
		{"codex", "/v1/responses", `{"model":"office/relay/m1","input":"hi","stream":false}`,
			"codex_cli_rs/0.159.2 (Mac OS 26.6.0; arm64) kitty", "codex_cli_rs", "codex", "",
			`{"id":"r","object":"response","model":"m1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`, provider.Responses},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			config := t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", config)
			t.Setenv("CLAUDE_CODE_PROMPT_CACHE_TTL", "")
			t.Setenv("FORCE_PROMPT_CACHING_5M", "")
			if err := os.WriteFile(filepath.Join(config, "settings.json"), []byte(`{"promptCacheTtl":"5m"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var atRelay, atRemote *http.Request
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				mu.Lock()
				atRelay = r.Clone(context.Background())
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, tc.reply)
			}))
			t.Cleanup(relay.Close)
			p := provider.Provider{ID: "relay", Key: "relay-key", Models: []string{"m1"}}
			if tc.proto == provider.Anthropic {
				p.Anthropic = relay.URL
			} else {
				p.Responses = relay.URL + "/v1"
			}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			there := New().Handler()
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == strings.Split(tc.path, "?")[0] {
					mu.Lock()
					atRemote = r.Clone(context.Background())
					mu.Unlock()
				}
				there.ServeHTTP(w, r)
			}))
			t.Cleanup(remote.Close)
			if err := provider.Save(provider.Provider{ID: "office", Preset: provider.RemoteMagpiePreset, Chat: remote.URL, Key: "remote-key"}); err != nil {
				t.Fatal(err)
			}
			office, _ := provider.Find("office")
			if _, err := office.Fetch(context.Background()); err != nil {
				t.Fatal(err)
			}

			req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
			req.RemoteAddr = "127.0.0.1:51000" // the agent runs on this computer
			req.Header.Set("User-Agent", tc.ua)
			if tc.originator != "" {
				req.Header.Set("originator", tc.originator)
			}
			req.Header.Set("x-app", "cli")
			req.Header.Set("Authorization", "Bearer "+TokenFor(tc.agent))
			req.Header.Set(SessionHeader, "ses-ua")
			rec := httptest.NewRecorder()
			New().Handler().ServeHTTP(rec, req)
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hello") {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			mu.Lock()
			defer mu.Unlock()
			if atRemote == nil || atRelay == nil {
				t.Fatalf("asked: remote %v, relay %v", atRemote != nil, atRelay != nil)
			}

			// the remote magpie was asked as magpie, the agent's named
			if ua := atRemote.Header.Get("User-Agent"); ua != magpieUA() {
				t.Errorf("the remote magpie was sent User-Agent %q, want %q", ua, magpieUA())
			}
			if got := atRemote.Header.Get(ClientUAHeader); got != tc.ua {
				t.Errorf("the remote magpie was sent %s %q, want %q", ClientUAHeader, got, tc.ua)
			}
			// and takes what it was told, as withCaller reads it there
			c := callerFrom(atRemote)
			if c.agent != tc.agent || c.via == "" || c.hops != 1 || c.ua != tc.ua || !c.relayed || c.cacheTTL != tc.ttl {
				t.Errorf("the remote took agent %q via %q hops %d ua %q relayed %v ttl %q; want %s, a computer, 1, %q, true, %q",
					c.agent, c.via, c.hops, c.ua, c.relayed, c.cacheTTL, tc.agent, tc.ua, tc.ttl)
			}

			// the remote's relay is sent the agent's own (#179), never
			// magpie's headers
			if atRelay.Header.Get("User-Agent") != tc.ua || atRelay.Header.Get("originator") != tc.originator {
				t.Errorf("the relay was sent User-Agent %q originator %q", atRelay.Header.Get("User-Agent"), atRelay.Header.Get("originator"))
			}
			if tc.proto == provider.Anthropic && (atRelay.Header.Get("X-App") != "cli" || atRelay.URL.RawQuery != "beta=true") {
				t.Errorf("the relay was sent x-app %q, query %q", atRelay.Header.Get("X-App"), atRelay.URL.RawQuery)
			}
			for k := range atRelay.Header {
				if strings.HasPrefix(strings.ToLower(k), "x-magpie-") {
					t.Errorf("the relay was sent %s", k)
				}
			}
			if a := atRelay.Header.Get("Authorization"); a != "" && a != "Bearer relay-key" {
				t.Errorf("the relay was sent Authorization %q", a)
			}

			// the remote records the call as the agent's, via this computer
			var got usage.Record
			for range 200 {
				for _, r := range usage.Load(time.Time{}) {
					if r.Provider == "relay" {
						got = r
					}
				}
				if got.Provider != "" {
					break
				}
				mu.Unlock()
				time.Sleep(20 * time.Millisecond)
				mu.Lock()
			}
			if got.Agent != tc.agent || got.Via == "" || got.Session != "ses-ua" {
				t.Errorf("the remote recorded agent %q via %q session %q; want %s, a computer, ses-ua", got.Agent, got.Via, got.Session, tc.agent)
			}
		})
	}
}
