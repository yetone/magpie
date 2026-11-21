package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A plugin's fetch is told the session the agent named and the subagent a
// request is of (SessionToPluginHeader, SubagentToPluginHeader): the
// agent's own headers never reach a plugin, so Devin's cache keepalive
// keyed Claude Code Auto's permission check by its text alone, and a check
// whose history grew kept its older pings running beside the new ones
// (magpie-community/plugins#620, zelong77). The same holds for Codex's
// spawned subagents (thread-id / turn metadata) and OpenCode's subagents
// (x-opencode-parent-session-id / x-opencode-session-id). The headers
// reach the plugin and never the vendor; a request that names no session
// has neither.
func TestPluginToldTheSession(t *testing.T) {
	var mu sync.Mutex
	var got []http.Header
	pid := besideFake(t, "fakeco", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		got = append(got, r.Header.Clone())
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		switch r.URL.Path {
		case "/v1/messages":
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
		case "/v1/responses":
			for _, e := range []string{
				`{"type":"response.created","response":{"id":"r1"}}`,
				`{"type":"response.output_text.delta","delta":"hi"}`,
				`{"type":"response.completed","response":{"id":"r1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":9,"output_tokens":2}}}`,
			} {
				io.WriteString(w, "data: "+e+"\n\n")
			}
		default:
			io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}`+"\n\n")
			io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
		}
	}))

	srv := New().Handler()
	check := func(t *testing.T, name, endpoint, body string, headers map[string]string, wantSession, wantSubagent string) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			mu.Lock()
			got = nil
			mu.Unlock()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", endpoint, strings.NewReader(body))
			if endpoint == "/v1/messages" {
				req.Header.Set("anthropic-version", "2023-06-01")
			}
			for k, v := range headers {
				req.Header.Set(k, v)
			}
			srv.ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			mu.Lock()
			sent := slices.Clone(got)
			mu.Unlock()
			if len(sent) == 0 {
				t.Fatalf("the vendor got nothing")
			}
			for _, h := range sent {
				for k, vs := range h {
					if strings.HasPrefix(strings.ToLower(k), "x-magpie-") {
						t.Errorf("the vendor got %s: %q", k, vs)
					}
				}
				for _, pair := range []struct {
					hdr, label, want string
				}{
					{"X-Saw-Session", "session", wantSession},
					{"X-Saw-Subagent", "subagent", wantSubagent},
				} {
					vs := h.Values(pair.hdr)
					if pair.want == "" {
						if len(vs) > 0 {
							t.Errorf("the plugin was told %s %q; want omitted", pair.label, vs)
						}
					} else if len(vs) != 1 || vs[0] != pair.want {
						t.Errorf("the plugin was told %s %q; want [%q]", pair.label, vs, pair.want)
					}
				}
			}
		})
	}

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
			{"claude code's subagent without session", map[string]string{"X-Claude-Code-Agent-Id": "agent-7"}, "", "agent-7"},
			{"magpie's own session header first", map[string]string{"X-Magpie-Session": "mine", "X-Claude-Code-Session-Id": "cc-s1"}, "mine", ""},
			{"opencode", map[string]string{"X-Opencode-Session": "ses_1"}, "ses_1", ""},
			{"no session named", nil, "", ""},
		} {
			body := `{"model":"` + pid + `/` + model + `","max_tokens":64,"stream":false,"messages":[{"role":"user","content":"hi"}]}`
			check(t, model+", "+tc.name, "/v1/messages", body, tc.headers, tc.session, tc.subagent)
		}
	}

	// Codex over Responses: relayed (fake-resp) and translated to Chat
	// (fake-1, where client_metadata is gone from the outgoing body).
	codexUA := "codex_exec/0.159.3 (Mac OS 26.7.0; arm64) dumb (codex_exec; 0.159.3)"
	tooLong := strings.Repeat("a", 129)
	for _, model := range []string{"fake-resp", "fake-1"} {
		for _, tc := range []struct {
			name              string
			headers           map[string]string
			clientMeta        string // optional JSON for body.client_metadata
			session, subagent string
		}{
			{
				name: "codex lead",
				headers: map[string]string{
					"User-Agent":            codexUA,
					"originator":            "codex_exec",
					"session-id":            "codex-root",
					"thread-id":             "codex-root",
					"x-codex-window-id":     "codex-root:0",
					"x-codex-turn-metadata": `{"session_id":"codex-root","thread_id":"codex-root"}`,
				},
				clientMeta: `{"session_id":"codex-root","thread_id":"codex-root"}`,
				session:    "codex-root",
			},
			{
				name: "codex collab_spawn subagent",
				headers: map[string]string{
					"User-Agent":               codexUA,
					"originator":               "codex_exec",
					"session-id":               "codex-root",
					"thread-id":                "codex-child-1",
					"x-codex-parent-thread-id": "codex-root",
					"x-openai-subagent":        "collab_spawn",
					"x-codex-window-id":        "codex-child-1:0",
					"x-codex-turn-metadata":    `{"session_id":"codex-root","thread_id":"codex-child-1"}`,
				},
				clientMeta: `{"session_id":"codex-root","thread_id":"codex-child-1","x-codex-parent-thread-id":"codex-root","x-openai-subagent":"collab_spawn"}`,
				session:    "codex-root",
				subagent:   "codex-child-1",
			},
			{
				name: "codex body client_metadata only with custom originator",
				headers: map[string]string{
					"User-Agent":        "lobehub-desktop/0.169.0 (Mac OS 26.5.0; arm64) unknown (lobehub-desktop; 2.1.40)",
					"originator":        "lobehub-desktop",
					"session-id":        "codex-root",
					"x-codex-window-id": "codex-child-2:0",
				},
				clientMeta: `{"thread_id":"codex-child-2","x-openai-subagent":"thread_spawn"}`,
				session:    "codex-root",
				subagent:   "codex-child-2",
			},
			{
				name: "codex nested turn-metadata subagent_kind in body",
				headers: map[string]string{
					"User-Agent":        codexUA,
					"session-id":        "codex-root",
					"x-codex-window-id": "codex-child-3:0",
				},
				clientMeta: `{"x-codex-turn-metadata":"{\"session_id\":\"codex-root\",\"thread_id\":\"codex-child-3\",\"subagent_kind\":\"collab_spawn\"}"}`,
				session:    "codex-root",
				subagent:   "codex-child-3",
			},
			{
				name: "codex side call thread_title is not a subagent",
				headers: map[string]string{
					"User-Agent":               codexUA,
					"originator":               "codex_exec",
					"session-id":               "codex-root",
					"thread-id":                "codex-title-1",
					"x-codex-parent-thread-id": "codex-root",
					"x-openai-subagent":        "thread_title",
					"x-codex-window-id":        "codex-title-1:0",
				},
				session: "codex-root",
			},
			{
				name: "codex side call guardian is not a subagent",
				headers: map[string]string{
					"User-Agent":        codexUA,
					"originator":        "codex_exec",
					"session-id":        "codex-root",
					"thread-id":         "codex-guard-1",
					"x-openai-subagent": "guardian",
					"x-codex-window-id": "codex-guard-1:0",
				},
				session: "codex-root",
			},
			{
				name: "codex side call review is not a subagent",
				headers: map[string]string{
					"User-Agent":        codexUA,
					"originator":        "codex_exec",
					"session-id":        "codex-root",
					"thread-id":         "codex-rev-1",
					"x-openai-subagent": "review",
					"x-codex-window-id": "codex-rev-1:0",
				},
				session: "codex-root",
			},
			{
				name: "codex nested spawn without thread id does not turn root into subagent",
				headers: map[string]string{
					"User-Agent":               codexUA,
					"originator":               "codex_exec",
					"session-id":               "codex-root",
					"x-codex-parent-thread-id": "codex-mid",
					"x-openai-subagent":        "collab_spawn",
					"x-codex-window-id":        "codex-root:0",
				},
				session: "codex-root",
			},
			{
				name: "codex subagent with explicit X-Magpie-Session matching child thread keeps subagent",
				headers: map[string]string{
					"User-Agent":               codexUA,
					"originator":               "codex_exec",
					"X-Magpie-Session":         "codex-child-1",
					"session-id":               "codex-root",
					"thread-id":                "codex-child-1",
					"x-codex-parent-thread-id": "codex-root",
					"x-openai-subagent":        "collab_spawn",
					"x-codex-window-id":        "codex-child-1:0",
				},
				session:  "codex-child-1",
				subagent: "codex-child-1",
			},
			{
				name: "codex malformed metadata thread falls back to valid thread-id header",
				headers: map[string]string{
					"User-Agent":            codexUA,
					"originator":            "codex_exec",
					"session-id":            "codex-root",
					"thread-id":             "codex-child-4",
					"x-openai-subagent":     "collab_spawn",
					"x-codex-window-id":     "codex-child-4:0",
					"x-codex-turn-metadata": `{"session_id":"codex-root","thread_id":"bad\nid"}`,
				},
				session:  "codex-root",
				subagent: "codex-child-4",
			},
			{
				name: "codex overlong thread-id is ignored",
				headers: map[string]string{
					"User-Agent":        codexUA,
					"originator":        "codex_exec",
					"session-id":        "codex-root",
					"thread-id":         tooLong,
					"x-openai-subagent": "collab_spawn",
					"x-codex-window-id": "codex-root:0",
				},
				session: "codex-root",
			},
			{
				name: "codex forged X-Magpie-Subagent on lead is dropped",
				headers: map[string]string{
					"User-Agent":        codexUA,
					"originator":        "codex_exec",
					"session-id":        "codex-root",
					"thread-id":         "codex-root",
					"X-Magpie-Subagent": "forged",
					"x-codex-window-id": "codex-root:0",
				},
				session: "codex-root",
			},
		} {
			body := `{"model":"` + pid + `/` + model + `","stream":true,"input":[{"role":"user","content":"hi"}]`
			if tc.clientMeta != "" {
				body += `,"client_metadata":` + tc.clientMeta
			}
			body += `}`
			check(t, model+", "+tc.name, "/v1/responses", body, tc.headers, tc.session, tc.subagent)
		}
	}

	// OpenCode 2 and OpenCode 1 over Chat Completions: relayed (fake-1)
	// and translated to Anthropic Messages (fake-claude).
	oc2UA := "opencode/latest/2.3.2/cli"
	oc1UA := "opencode/1.19.6 ai-sdk/provider-utils/4.0.23 runtime/bun/1.4.4"
	for _, model := range []string{"fake-1", "fake-claude"} {
		for _, tc := range []struct {
			name              string
			headers           map[string]string
			session, subagent string
		}{
			{
				name: "opencode 2 lead",
				headers: map[string]string{
					"User-Agent":            oc2UA,
					"x-opencode-session":    "ses_root",
					"x-opencode-session-id": "ses_root",
					"x-session-affinity":    "ses_root",
					"x-session-id":          "ses_root",
				},
				session: "ses_root",
			},
			{
				name: "opencode 2 user fork without parent header is not a subagent",
				headers: map[string]string{
					"User-Agent":            oc2UA,
					"x-opencode-session":    "ses_root",
					"x-opencode-session-id": "ses_fork",
					"x-session-affinity":    "ses_root",
					"x-session-id":          "ses_root",
				},
				session: "ses_root",
			},
			{
				name: "opencode 2 subagent",
				headers: map[string]string{
					"User-Agent":                   oc2UA,
					"x-opencode-session":           "ses_root",
					"x-opencode-session-id":        "ses_child",
					"x-opencode-parent-session-id": "ses_root",
					"x-parent-session-id":          "ses_root",
					"x-session-affinity":           "ses_root",
					"x-session-id":                 "ses_root",
				},
				session:  "ses_root",
				subagent: "ses_child",
			},
			{
				name: "opencode 1 lead",
				headers: map[string]string{
					"User-Agent":            oc1UA,
					"x-opencode-session-id": "ses_root",
					"x-session-affinity":    "ses_root",
					"x-session-id":          "ses_root",
				},
				session: "ses_root",
			},
			{
				name: "opencode 1 subagent",
				headers: map[string]string{
					"User-Agent":                   oc1UA,
					"x-opencode-session-id":        "ses_child",
					"x-opencode-parent-session-id": "ses_root",
					"x-parent-session-id":          "ses_root",
					"x-session-affinity":           "ses_child",
					"x-session-id":                 "ses_child",
				},
				session:  "ses_root",
				subagent: "ses_child",
			},
			{
				name: "opencode 1 subagent respects explicit X-Magpie-Session",
				headers: map[string]string{
					"User-Agent":                   oc1UA,
					"X-Magpie-Session":             "mine",
					"x-opencode-session-id":        "ses_child",
					"x-opencode-parent-session-id": "ses_root",
					"x-session-affinity":           "ses_child",
				},
				session:  "mine",
				subagent: "ses_child",
			},
			{
				name: "opencode 1 subagent with explicit X-Magpie-Session matching child keeps subagent",
				headers: map[string]string{
					"User-Agent":                   oc1UA,
					"X-Magpie-Session":             "ses_child",
					"x-opencode-session-id":        "ses_child",
					"x-opencode-parent-session-id": "ses_root",
					"x-session-affinity":           "ses_child",
				},
				session:  "ses_child",
				subagent: "ses_child",
			},
			{
				name: "opencode self-parent is not a subagent",
				headers: map[string]string{
					"User-Agent":                   oc1UA,
					"x-opencode-session-id":        "ses_self",
					"x-opencode-parent-session-id": "ses_self",
					"x-session-affinity":           "ses_self",
				},
				session: "ses_self",
			},
			{
				name: "generic x-parent-session-id without opencode headers is not treated as opencode",
				headers: map[string]string{
					"x-session-affinity":  "other_child",
					"x-parent-session-id": "other_parent",
				},
				session: "other_child",
			},
			{
				name: "opencode overlong session id is ignored as subagent",
				headers: map[string]string{
					"User-Agent":                   oc2UA,
					"x-opencode-session":           "ses_root",
					"x-opencode-session-id":        tooLong,
					"x-opencode-parent-session-id": "ses_root",
				},
				session: "ses_root",
			},
		} {
			body := `{"model":"` + pid + `/` + model + `","stream":true,"messages":[{"role":"user","content":"hi"}]}`
			check(t, model+", "+tc.name, "/v1/chat/completions", body, tc.headers, tc.session, tc.subagent)
		}
	}

	// Codex's Responses WebSocket (responses_ws.go): the handshake carries
	// session-id, thread-id and x-openai-subagent (codex-rs
	// build_websocket_headers), and each turn is served as its POST is
	t.Run("codex subagent over the responses websocket", func(t *testing.T) {
		mu.Lock()
		got = nil
		mu.Unlock()
		_, ts := sockServer(t)
		c, resp, err := dialSock(t, ts, "/v1/responses", http.Header{
			"User-Agent": {codexUA}, "Originator": {"codex_exec"},
			"Session-Id": {"codex-root"}, "Thread-Id": {"codex-child-ws"},
			"X-Openai-Subagent": {"collab_spawn"}, "X-Codex-Window-Id": {"codex-child-ws:0"},
		})
		if err != nil {
			code := 0
			if resp != nil {
				code = resp.StatusCode
			}
			t.Fatalf("%v (%d)", err, code)
		}
		out := c.turn(`{"type":"response.create","model":"` + pid + `/fake-resp","stream":true,"input":[{"role":"user","content":"hi"}],"client_metadata":{"session_id":"codex-root","thread_id":"codex-child-ws","x-openai-subagent":"collab_spawn"}}`)
		if last(out).Get("type").String() != "response.completed" {
			t.Fatalf("%v", out)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(got) == 0 {
			t.Fatal("the vendor got nothing")
		}
		for _, h := range got {
			for k, vs := range h {
				if strings.HasPrefix(strings.ToLower(k), "x-magpie-") {
					t.Errorf("the vendor got %s: %q", k, vs)
				}
			}
			if s, a := h.Values("X-Saw-Session"), h.Values("X-Saw-Subagent"); !slices.Equal(s, []string{"codex-root"}) || !slices.Equal(a, []string{"codex-child-ws"}) {
				t.Errorf("the plugin was told session %q, subagent %q; want codex-root, codex-child-ws", s, a)
			}
		}
	})

	// magpie's own ask made while a Codex subagent's turn is out (a web
	// search, searchWith) enters serve again on that turn's context: serve
	// tells the plugin the ask's own subagent, none, not the turn's
	t.Run("a re-entry from a codex subagent's turn", func(t *testing.T) {
		mu.Lock()
		got = nil
		mu.Unlock()
		ctx := context.WithValue(withCodexPluginSubagent(context.Background(), "codex-child-1"), callerCtx{}, caller{agent: "codex"})
		r := httptest.NewRequestWithContext(ownAsk(magpieChose(ctx)), "POST", "http://magpie/v1/messages", nil)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("User-Agent", SearchAgent)
		rec := httptest.NewRecorder()
		New().serve(rec, r, provider.Anthropic, []byte(`{"model":"`+pid+`/fake-claude","max_tokens":64,"stream":false,"messages":[{"role":"user","content":"hi"}]}`))
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(got) == 0 {
			t.Fatal("the vendor got nothing")
		}
		for _, h := range got {
			if v := h.Values("X-Saw-Subagent"); len(v) > 0 {
				t.Errorf("the plugin was told subagent %q for magpie's own ask; want omitted", v)
			}
		}
	})
}

func TestPluginSessionEdges(t *testing.T) {
	// Re-entering serve (as searchWith does) overwrites the outer Codex
	// subagent in ctx with "".
	outer := withCodexPluginSubagent(context.Background(), "codex-child-1")
	inner := withCodexPluginSubagent(outer, "")
	in := http.Header{"User-Agent": []string{SearchAgent}}
	if s, sub := pluginSessionOf(inner, in, nil); s != "" || sub != "" {
		t.Errorf("inner search got (%q, %q); want (\"\", \"\")", s, sub)
	}

	// Bypassing serve (as count_tokens does) still derives Codex subagent
	// from incoming headers and callerCtx.
	ctx := context.WithValue(context.Background(), callerCtx{}, caller{agent: "codex"})
	codexHdr := http.Header{
		"Session-Id":        []string{"codex-root"},
		"Thread-Id":         []string{"codex-child-9"},
		"X-Openai-Subagent": []string{"collab_spawn"},
	}
	if s, sub := pluginSessionOf(ctx, codexHdr, nil); s != "codex-root" || sub != "codex-child-9" {
		t.Errorf("count_tokens fallback got (%q, %q); want (\"codex-root\", \"codex-child-9\")", s, sub)
	}

	// validPluginID rejects C0 control characters, DEL, and >128 bytes.
	for _, bad := range []string{"", "   ", "a\x00b", "a\nb", "a\rb", "a\tb", "a\x7fb", strings.Repeat("x", 129)} {
		if got := validPluginID(bad); got != "" {
			t.Errorf("validPluginID(%q) = %q; want empty", bad, got)
		}
	}
	if got := validPluginID("  ok-123  "); got != "ok-123" {
		t.Errorf("validPluginID trimmed = %q; want ok-123", got)
	}
}
