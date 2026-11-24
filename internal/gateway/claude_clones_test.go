package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// capturedRequest is a request kept in testdata as it came: its path, its
// headers in order and its JSON body.
type capturedRequest struct {
	path    string
	headers [][2]string
	body    map[string]any
}

func readOmpCapture(t *testing.T, name string) capturedRequest {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "omp_claude_shaped", name))
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Path    string         `json:"path"`
		Headers [][2]string    `json:"headers"`
		Body    map[string]any `json:"body"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return capturedRequest{c.Path, c.Headers, c.Body}
}

func readClaudeCodeCapture(t *testing.T, name string) capturedRequest {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "claude_code_compact_fork", name))
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Path    string            `json:"path"`
		Headers map[string]string `json:"headers"`
		Body    map[string]any    `json:"body"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	out := capturedRequest{path: c.Path, body: c.Body}
	for k, v := range c.Headers {
		out.headers = append(out.headers, [2]string{k, v})
	}
	out.headers = append(out.headers, [2]string{"Authorization", "Bearer " + Token})
	return out
}

// SadApple235141 on X (v0.1.4743): "在omp使用Antigravity插件后 magpie会将
// omp识别为Claude code". omp's anthropic-messages provider with an apiKey
// and no auth is shaped as a Claude subscription's, so its request comes in
// Claude Code's headers; the fixtures are omp 19.1.1's and 18.20.0's
// requests as they reached a stand-in gateway. Its calls, and the side
// calls of its session that say nothing of omp, are omp's on the Routing
// card and in usage, and Claude Code's own request stays Claude Code's.
// What goes upstream is what came: the headers that make it Claude Code's
// reach the vendor as they were sent.
func TestOmpInClaudeCodeHeadersIsOmp(t *testing.T) {
	setHome(t, t.TempDir())
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"msg","type":"message","role":"assistant","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`}
	setup(t, provider.Anthropic, f)
	was := claudeClones
	t.Cleanup(func() {
		claudeClones = was
		cloneSessions.Lock()
		cloneSessions.m = map[string]string{}
		cloneSessions.Unlock()
	})
	s := New()
	h := s.Handler()
	send := func(c capturedRequest) *httptest.ResponseRecorder {
		t.Helper()
		// on the fake provider, answered whole; the rest as captured
		c.body["model"], c.body["stream"] = "fake/m1", false
		b, _ := json.Marshal(c.body)
		req := httptest.NewRequest("POST", c.path, bytes.NewReader(b))
		for _, kv := range c.headers {
			req.Header.Add(kv[0], kv[1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", c.path, rec.Code, rec.Body.String())
		}
		return rec
	}
	omp19 := readOmpCapture(t, "omp-19.1.1.json")
	sessionHeader := func(c capturedRequest) string {
		for _, kv := range c.headers {
			if http.CanonicalHeaderKey(kv[0]) == "X-Claude-Code-Session-Id" {
				return kv[1]
			}
		}
		return ""
	}
	// a title of the same session: omp's title-system.md, nothing of omp's
	title := capturedRequest{path: omp19.path, headers: omp19.headers, body: map[string]any{
		"max_tokens": 64,
		"system":     []any{map[string]any{"type": "text", "text": "You name coding-agent sessions. Write a 3-6 word title for the task in the next message."}},
		"messages":   []any{map[string]any{"role": "user", "content": "say hello"}},
	}}
	cc := readClaudeCodeCapture(t, "1-turn.json")
	cases := []struct {
		name string
		req  capturedRequest
		want string
	}{
		{"omp 19.1.1", omp19, "omp"},
		{"omp 18.20.0", readOmpCapture(t, "omp-18.20.0.json"), "omp"},
		{"a side call of omp's session", title, "omp"},
		{"claude code itself", cc, usage.AgentOf("claude-cli/2.1.325 (external, cli)")},
	}
	type upstream struct {
		body string
		head http.Header
	}
	var ups []upstream
	for i, c := range cases {
		send(c.req)
		var got []usage.Record
		for range 200 {
			if got = usage.Load(time.Time{}); len(got) > i {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if len(got) <= i || got[i].Agent != c.want {
			t.Fatalf("%s: recorded as %+v, want %q", c.name, got, c.want)
		}
		s.trace.mu.Lock()
		rt := s.trace.routes[len(s.trace.routes)-1]
		agent, session := rt.Agent, rt.Session
		s.trace.mu.Unlock()
		if agent != c.want {
			t.Errorf("%s: routing card says %q, want %q", c.name, agent, c.want)
		}
		if want := sessionHeader(c.req); session != want {
			t.Errorf("%s: route session %q, want %q", c.name, session, want)
		}
		ups = append(ups, upstream{body: string(f.got), head: f.head.Clone()})
	}
	// Claude Code's own session is never remembered as a clone's, and a
	// count_tokens of omp's is omp's too
	if a := cloneOf(httptest.NewRequest("POST", "/v1/messages", nil)); a != "" {
		t.Errorf("a bare request named %q", a)
	}
	ccReq := httptest.NewRequest("POST", "/v1/messages", nil)
	for _, kv := range cc.headers {
		ccReq.Header.Set(kv[0], kv[1])
	}
	if a := agentOf(ccReq); a != usage.AgentOf("claude-cli/2.1.325 (external, cli)") {
		t.Errorf("Claude Code's session named %q", a)
	}
	ompReq := httptest.NewRequest("POST", "/v1/messages/count_tokens", nil)
	for _, kv := range omp19.headers {
		ompReq.Header.Set(kv[0], kv[1])
	}
	if a := agentOf(ompReq); a != "omp" {
		t.Errorf("omp's count_tokens named %q", a)
	}
	// only the name changes: with no clone known, omp's requests go
	// upstream as they did when they were named Claude Code's
	claudeClones = nil
	cloneSessions.Lock()
	cloneSessions.m = map[string]string{}
	cloneSessions.Unlock()
	for i, c := range cases[:2] {
		send(c.req)
		var got []usage.Record
		for range 200 {
			if got = usage.Load(time.Time{}); len(got) > len(cases)+i {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if len(got) <= len(cases)+i || got[len(cases)+i].Agent == "omp" {
			t.Fatalf("%s: with no clone known, recorded as %+v", c.name, got)
		}
		if string(f.got) != ups[i].body {
			t.Errorf("%s: upstream body changed with the name:\n%.300s\n%.300s", c.name, ups[i].body, f.got)
		}
		for _, k := range []string{"User-Agent", "Anthropic-Beta", "X-App", "Authorization", "X-Api-Key", "X-Claude-Code-Session-Id"} {
			if f.head.Get(k) != ups[i].head.Get(k) {
				t.Errorf("%s: upstream %s %q, named Claude Code's %q", c.name, k, ups[i].head.Get(k), f.head.Get(k))
			}
		}
	}
}

// A request is named a clone's only by a system block of the clone's own
// beginning it, and only in Claude Code's headers: Claude Code's own
// prompt, a user's line quoting omp's later in a block, and omp's own
// User-Agent (named as before, by it) are not.
func TestCloneByPrompt(t *testing.T) {
	for _, c := range []struct {
		body, want string
	}{
		{`{"system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},{"type":"text","text":"You are omp's trusted coding assistant.\n\n# Engineering"}]}`, "omp"},
		{`{"system":"RFC 2119 keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.\n"}`, "omp"},
		{`{"system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},{"type":"text","text":"\nYou are an interactive agent. Note: You are omp's trusted coding assistant."}]}`, ""},
		{`{"messages":[{"role":"user","content":"You are omp's trusted coding assistant."}]}`, ""},
		{`{"system":[]}`, ""},
	} {
		if got := cloneByPrompt([]byte(c.body)); got != c.want {
			t.Errorf("%s: %q, want %q", c.body, got, c.want)
		}
	}
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("User-Agent", "omp/19.1.1")
	req = req.WithContext(t.Context())
	if got := withClone(req, provider.Anthropic, []byte(`{"system":"You are omp's trusted coding assistant."}`)); cloneOf(got) != "" {
		t.Error("omp's own User-Agent taken through the clone path")
	}
}
