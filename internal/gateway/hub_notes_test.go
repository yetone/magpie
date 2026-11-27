package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// noteEvents are the messages of an event stream reply, in order.
func noteEvents(t *testing.T, w *httptest.ResponseRecorder) []map[string]json.RawMessage {
	t.Helper()
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("%d %s, want an event stream: %s", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	var out []map[string]json.RawMessage
	for _, e := range strings.Split(w.Body.String(), "\n\n") {
		for _, l := range strings.Split(e, "\n") {
			if d, ok := strings.CutPrefix(l, "data: "); ok {
				var m map[string]json.RawMessage
				if err := json.Unmarshal([]byte(d), &m); err != nil {
					t.Fatalf("event %q: %v", d, err)
				}
				out = append(out, m)
			}
		}
	}
	return out
}

func hubCallWithToken(name, args, token string) string {
	return `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `,"_meta":{"progressToken":` + token + `}}}`
}

// The router says it logs (initialize's capabilities), and takes a
// session's logging/setLevel: a level MCP has is answered, another is
// invalid params (#7920).
func TestHubDeclaresLogging(t *testing.T) {
	fresh(t)
	withHub(t, &fakeHub{on: true, tools: map[string][]HubTool{"claude": {{Server: "s", Tool: "a", Spec: hubSpec("a")}}}})
	h := lanGuard(New().Handler())
	init := hubPost(h, "claude", here, hubInit)
	var r struct {
		Result struct {
			Capabilities map[string]json.RawMessage
		}
	}
	json.Unmarshal(init.Body.Bytes(), &r)
	if _, ok := r.Result.Capabilities["logging"]; !ok {
		t.Fatalf("capabilities %s, want logging", init.Body.String())
	}
	sid := init.Header().Get("Mcp-Session-Id")
	if sid == "" {
		t.Fatal("no session id")
	}
	set := func(level string) mcpReply {
		return readMCP(t, hubPost(h, "claude", here, `{"jsonrpc":"2.0","id":2,"method":"logging/setLevel","params":{"level":"`+level+`"}}`, "Mcp-Session-Id", sid))
	}
	if m := set("warning"); m.Error != nil {
		t.Fatalf("setLevel warning: %+v", m.Error)
	}
	if m := set("loud"); m.Error == nil || m.Error.Code != -32602 {
		t.Fatalf("setLevel loud: %+v, want -32602", m.Error)
	}
}

// A call with a progressToken is told its server's progress under the
// agent's own token, and its log lines, on its event stream before its
// result; a log line under the session's level is left out; the same call
// by a client that takes JSON only, or with no token, is answered with
// JSON, and its tool isn't asked for progress.
func TestHubRelaysNotes(t *testing.T) {
	fresh(t)
	type asked struct{ note, progress bool }
	seen := make(chan asked, 8)
	f := &fakeHub{on: true, tools: map[string][]HubTool{"claude": {{Server: "s", Tool: "a", Spec: hubSpec("a")}}},
		call: func(ctx context.Context, server, tool string) (json.RawMessage, error) {
			n, progress := HubNoteOf(ctx)
			seen <- asked{n != nil, progress}
			if n != nil {
				n("notifications/progress", json.RawMessage(`{"progressToken":"magpie-1","progress":1,"total":2}`))
				n("notifications/message", json.RawMessage(`{"level":"debug","data":"quiet"}`))
				n("notifications/message", json.RawMessage(`{"level":"error","data":"loud"}`))
				n("notifications/tools/list_changed", json.RawMessage(`{}`))
			}
			return json.RawMessage(`{"content":[{"type":"text","text":"done"}]}`), nil
		}}
	withHub(t, f)
	h := lanGuard(New().Handler())

	for _, tok := range []string{`"mine"`, `42`} {
		evs := noteEvents(t, hubPost(h, "claude", here, hubCallWithToken("s__a", `{}`, tok)))
		if a := <-seen; !a.note || !a.progress {
			t.Fatalf("token %s: the tool was asked %+v, want progress", tok, a)
		}
		if len(evs) != 4 {
			t.Fatalf("token %s: %d events %v, want progress, 2 log lines, result", tok, len(evs), evs)
		}
		var p struct {
			ProgressToken json.RawMessage
			Progress      float64
			Total         float64
		}
		json.Unmarshal(evs[0]["params"], &p)
		if string(evs[0]["method"]) != `"notifications/progress"` || string(p.ProgressToken) != tok || p.Progress != 1 || p.Total != 2 {
			t.Fatalf("token %s: first event %s %s", tok, evs[0]["method"], evs[0]["params"])
		}
		if string(evs[1]["method"]) != `"notifications/message"` || !strings.Contains(string(evs[1]["params"]), "quiet") ||
			!strings.Contains(string(evs[2]["params"]), "loud") {
			t.Fatalf("token %s: log lines %v %v", tok, evs[1], evs[2])
		}
		if string(evs[3]["id"]) != "3" || !strings.Contains(string(evs[3]["result"]), "done") {
			t.Fatalf("token %s: last event %v, want the result", tok, evs[3])
		}
	}

	// JSON only, a bad token, or none: the reply is JSON, nothing asked
	for name, body := range map[string]string{
		"json only": hubCallWithToken("s__a", `{}`, `"mine"`),
		"no token":  toolsCall("s__a", `{}`),
		"null":      hubCallWithToken("s__a", `{}`, `null`),
		"object":    hubCallWithToken("s__a", `{}`, `{"x":1}`),
	} {
		hdr := []string{}
		if name == "json only" {
			hdr = []string{"Accept", "application/json"}
		}
		m := readMCP(t, hubPost(h, "claude", here, body, hdr...))
		if len(m.Result.Content) == 0 || m.Result.Content[0].Text != "done" {
			t.Fatalf("%s: %+v", name, m)
		}
		if a := <-seen; a.note || a.progress {
			t.Fatalf("%s: the tool was asked %+v, want nothing", name, a)
		}
	}

	// a session at warning is told the error line only, a call with no
	// token too, and never progress it didn't ask for
	sid := hubPost(h, "claude", here, hubInit).Header().Get("Mcp-Session-Id")
	readMCP(t, hubPost(h, "claude", here, `{"jsonrpc":"2.0","id":2,"method":"logging/setLevel","params":{"level":"warning"}}`, "Mcp-Session-Id", sid))
	evs := noteEvents(t, hubPost(h, "claude", here, toolsCall("s__a", `{}`), "Mcp-Session-Id", sid))
	if a := <-seen; !a.note || a.progress {
		t.Fatalf("leveled call without a token: asked %+v, want log lines only", a)
	}
	if len(evs) != 2 || !strings.Contains(string(evs[0]["params"]), "loud") || string(evs[1]["id"]) != "3" {
		t.Fatalf("leveled call: %v, want the error line and the result", evs)
	}
	// another session isn't held to it
	other := hubPost(h, "claude", here, hubInit).Header().Get("Mcp-Session-Id")
	evs = noteEvents(t, hubPost(h, "claude", here, hubCallWithToken("s__a", `{}`, `"t"`), "Mcp-Session-Id", other))
	<-seen
	if len(evs) != 4 {
		t.Fatalf("another session: %v", evs)
	}
}

// call_tool reaches the same relay; run_code relays its tools' log lines
// but never their progress, which under one token would go back and forth
// between them.
func TestHubRelaysThroughCallToolAndCode(t *testing.T) {
	type asked struct{ note, progress bool }
	seen := make(chan asked, 8)
	_, _ = codeHub(t, func(ctx context.Context, server, tool string) (json.RawMessage, error) {
		n, progress := HubNoteOf(ctx)
		seen <- asked{n != nil, progress}
		if n != nil {
			n("notifications/progress", json.RawMessage(`{"progressToken":"magpie-1","progress":1}`))
			n("notifications/message", json.RawMessage(`{"level":"info","data":"from `+tool+`"}`))
		}
		return json.RawMessage(`{"content":[{"type":"text","text":"[]"}]}`), nil
	})
	h := lanGuard(New().Handler())

	evs := noteEvents(t, hubPost(h, "opencode", here, hubCallWithToken(hubCallTool, `{"name":"github__get_issue","arguments":{}}`, `"c"`)))
	if a := <-seen; !a.note || !a.progress {
		t.Fatalf("call_tool: asked %+v", a)
	}
	if len(evs) != 3 || !strings.Contains(string(evs[0]["params"]), `"progressToken":"c"`) || !strings.Contains(string(evs[1]["params"]), "from get_issue") {
		t.Fatalf("call_tool: %v", evs)
	}

	code, _ := json.Marshal(map[string]string{"code": `await tools.github__get_issue({}); await tools.github__search_issues({}); return 1;`})
	evs = noteEvents(t, hubPost(h, "opencode", here, hubCallWithToken(hubCodeTool, string(code), `"r"`)))
	for range 2 {
		if a := <-seen; !a.note || a.progress {
			t.Fatalf("run_code: asked %+v, want log lines only", a)
		}
	}
	if len(evs) != 3 {
		t.Fatalf("run_code: %d events %v, want 2 log lines and the result", len(evs), evs)
	}
	for _, e := range evs[:2] {
		if string(e["method"]) != `"notifications/message"` {
			t.Fatalf("run_code relayed %v", e)
		}
	}
}

// stuckWriter is a client that reads nothing of its stream.
type stuckWriter struct {
	h    http.Header
	hold chan struct{}
}

func (s *stuckWriter) Header() http.Header         { return s.h }
func (s *stuckWriter) WriteHeader(int)             {}
func (s *stuckWriter) Write(b []byte) (int, error) { <-s.hold; return len(b), nil }

// The note is handed on from the server's reader: an agent that reads
// nothing never holds it up; what can't wait is dropped.
func TestHubRelayNeverBlocks(t *testing.T) {
	fresh(t)
	w := &stuckWriter{h: http.Header{}, hold: make(chan struct{})}
	x := &mcpExchange{w: w, canStream: true}
	r := httptest.NewRequest(http.MethodPost, HubMCPPath+"claude", nil)
	r = r.WithContext(context.WithValue(r.Context(), mcpExchangeKey{}, x))
	h := newHubRelay(r, "claude", json.RawMessage(`"t"`), true)
	if h == nil {
		t.Fatal("no relay")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 10 * hubRelayQueue {
			h.note("notifications/progress", json.RawMessage(`{"progressToken":"magpie-1","progress":1}`))
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("note blocked on a client that reads nothing")
	}
	close(w.hold)
	h.end()
	// after the end nothing more is sent
	h.note("notifications/progress", json.RawMessage(`{"progressToken":"magpie-1","progress":2}`))
}
