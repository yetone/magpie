package library

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// agentCall is what an agent posts to the router at gw, taking accept
// back: the event stream's messages in order, or the one JSON reply.
func agentCall(ctx context.Context, t *testing.T, gw, agent, accept string, body any, hdr ...string) []map[string]json.RawMessage {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, gw+"/mcp/magpie/hub/"+agent, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Errorf("post: %v", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Errorf("%d %s", resp.StatusCode, b)
		return nil
	}
	var out []map[string]json.RawMessage
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		var m map[string]json.RawMessage
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
			t.Errorf("reply: %v", err)
		}
		return append(out, m)
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			var m map[string]json.RawMessage
			if err := json.Unmarshal([]byte(d), &m); err != nil {
				t.Errorf("event %q: %v", d, err)
			}
			out = append(out, m)
		}
	}
	return out
}

func callBody(name string, args any, token any) map[string]any {
	p := map[string]any{"name": name, "arguments": args}
	if token != nil {
		p["_meta"] = map[string]any{"progressToken": token}
	}
	return map[string]any{"jsonrpc": "2.0", "id": 9, "method": "tools/call", "params": p}
}

// show is messages as JSON, for a failure to print.
func show(evs []map[string]json.RawMessage) string {
	b, _ := json.Marshal(evs)
	return string(b)
}

const sseAccept, jsonAccept = "application/json, text/event-stream", "application/json"

// noteServer is a streamable HTTP server whose tools/call answers on an
// event stream: work tells another call's progress, its own under the
// token it was given, and a log line, then its result; hold tells its
// progress and waits for its POST to end. tokens are the progressTokens
// its calls came with ("" for none).
type noteServer struct {
	*httptest.Server
	mu     sync.Mutex
	tokens []string
	held   chan struct{} // a hold call began
	freed  chan bool     // a hold call ended: whether its POST was ended
}

func newNoteServer(t *testing.T) *noteServer {
	s := &noteServer{held: make(chan struct{}, 4), freed: make(chan bool, 4)}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var m struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
				Meta struct {
					Token json.RawMessage `json:"progressToken"`
				} `json:"_meta"`
			} `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&m)
		send := func(f string, a ...any) {
			fmt.Fprintf(w, "event: message\ndata: "+f+"\n\n", a...)
			w.(http.Flusher).Flush()
		}
		switch m.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "s-1")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}}}}`, *m.ID)
		case "tools/list":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"work","inputSchema":{"type":"object"}},{"name":"hold","inputSchema":{"type":"object"}}]}}`, *m.ID)
		case "tools/call":
			var tok string
			json.Unmarshal(m.Params.Meta.Token, &tok)
			s.mu.Lock()
			s.tokens = append(s.tokens, tok)
			s.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			send(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"magpie-0","progress":9}}`)
			if tok != "" {
				send(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":%q,"progress":1,"total":2,"message":"half"}}`, tok)
			}
			if m.Params.Name == "hold" {
				s.held <- struct{}{}
				select {
				case <-r.Context().Done():
					s.freed <- true
				case <-time.After(30 * time.Second):
					s.freed <- false
				}
				return
			}
			send(`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"working"}}`)
			send(`{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"work done"}]}}`, *m.ID)
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *noteServer) given() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.tokens...)
}

func progressOf(t *testing.T, m map[string]json.RawMessage) (token json.RawMessage, message string) {
	t.Helper()
	if string(m["method"]) != `"notifications/progress"` {
		t.Fatalf("%v isn't progress", m)
	}
	var p struct {
		ProgressToken json.RawMessage
		Message       string
	}
	json.Unmarshal(m["params"], &p)
	return p.ProgressToken, p.Message
}

func textOf(t *testing.T, m map[string]json.RawMessage) string {
	t.Helper()
	if m["result"] == nil {
		t.Fatalf("%v isn't a result", m)
	}
	return hubText(t, m["result"])
}

// #7920 (uratmangun2 on X): a long call through the router showed its
// agent nothing until it ended, as every notification of the server's was
// dropped. A call with a progressToken, by an agent that takes an event
// stream, is told its server's progress under its own token, not another
// call's, and its log line, then its result; the server is given a token
// of magpie's. Through call_tool the same. A client that takes JSON only,
// or a call with no token, is answered with JSON as before, and the server
// is given no token.
func TestRouterRelaysProgressHTTP(t *testing.T) {
	hubSandbox(t)
	up := newNoteServer(t)
	ok(t)(SaveServer("", Server{Name: "up", Transport: "http", URL: up.URL, Agents: []string{"claude"}}))
	ok(t)(SetMCPHub(true))
	gw := httptest.NewServer(gateway.New().Handler())
	t.Cleanup(gw.Close)
	ctx := context.Background()

	for _, c := range []struct {
		name string
		body map[string]any
	}{
		{"direct", callBody("up__work", map[string]any{}, "agent-1")},
		{"call_tool", callBody("call_tool", map[string]any{"name": "up__work", "arguments": map[string]any{}}, "agent-1")},
	} {
		evs := agentCall(ctx, t, gw.URL, "claude", sseAccept, c.body)
		if len(evs) != 3 {
			t.Fatalf("%s: %d messages %v, want progress, a log line and the result", c.name, len(evs), show(evs))
		}
		if tok, msg := progressOf(t, evs[0]); string(tok) != `"agent-1"` || msg != "half" {
			t.Fatalf("%s: progress %s %q, want the agent's own token", c.name, tok, msg)
		}
		if string(evs[1]["method"]) != `"notifications/message"` || !strings.Contains(string(evs[1]["params"]), "working") {
			t.Fatalf("%s: %v, want the log line", c.name, evs[1])
		}
		if got := textOf(t, evs[2]); got != "work done" || string(evs[2]["id"]) != "9" {
			t.Fatalf("%s: result %q id %s", c.name, got, evs[2]["id"])
		}
		g := up.given()
		if tok := g[len(g)-1]; !strings.HasPrefix(tok, "magpie-") {
			t.Fatalf("%s: the server was given token %q, want magpie's own", c.name, tok)
		}
	}

	for name, call := range map[string]func() []map[string]json.RawMessage{
		"json only": func() []map[string]json.RawMessage {
			return agentCall(ctx, t, gw.URL, "claude", jsonAccept, callBody("up__work", map[string]any{}, "agent-2"))
		},
		"no token": func() []map[string]json.RawMessage {
			return agentCall(ctx, t, gw.URL, "claude", sseAccept, callBody("up__work", map[string]any{}, nil))
		},
	} {
		evs := call()
		if len(evs) != 1 || textOf(t, evs[0]) != "work done" {
			t.Fatalf("%s: %v, want the JSON result alone", name, show(evs))
		}
		g := up.given()
		if tok := g[len(g)-1]; tok != "" {
			t.Fatalf("%s: the server was given token %q, want none", name, tok)
		}
	}
}

// An agent that goes away mid-call ends its call on the server: the POST
// to it is ended, not left waiting with its progress unread, and the next
// call is answered.
func TestRouterDroppedAgentEndsTheCall(t *testing.T) {
	hubSandbox(t)
	up := newNoteServer(t)
	ok(t)(SaveServer("", Server{Name: "up", Transport: "http", URL: up.URL, Agents: []string{"claude"}}))
	ok(t)(SetMCPHub(true))
	gw := httptest.NewServer(gateway.New().Handler())
	t.Cleanup(gw.Close)

	ctx, cancel := context.WithCancel(context.Background())
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		b, _ := json.Marshal(callBody("up__hold", map[string]any{}, "agent-1"))
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, gw.URL+"/mcp/magpie/hub/claude", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", sseAccept)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	select {
	case <-up.held:
	case <-time.After(30 * time.Second):
		t.Fatal("the hold call never reached the server")
	}
	cancel()
	<-gone
	select {
	case ended := <-up.freed:
		if !ended {
			t.Fatal("the server's call was never ended")
		}
	case <-time.After(40 * time.Second):
		t.Fatal("the server's call was never ended")
	}
	evs := agentCall(context.Background(), t, gw.URL, "claude", sseAccept, callBody("up__work", map[string]any{}, "agent-2"))
	if len(evs) != 3 || textOf(t, evs[2]) != "work done" {
		t.Fatalf("the next call: %v", show(evs))
	}
}

// On a command's stdio every reply shares one stream: two calls at once,
// one with a string token and one a number, are each told their own
// progress only, and a log line said while both wait goes to neither; a
// lone call is told its log line.
func TestRouterRelaysProgressStdio(t *testing.T) {
	hubSandbox(t)
	tools := t.TempDir() + "/tools.json"
	write(t, tools, `{"jsonrpc":"2.0","id":0,"result":{"tools":[{"name":"logs","inputSchema":{"type":"object"}},{"name":"together","inputSchema":{"type":"object"}}]}}`)
	s := fakeStdio(t, "ok", map[string]string{"MAGPIE_FAKE_MCP_TOOLS": tools})
	s.Agents = []string{"claude"}
	ok(t)(SaveServer("", *s))
	ok(t)(SetMCPHub(true))
	gw := httptest.NewServer(gateway.New().Handler())
	t.Cleanup(gw.Close)
	ctx := context.Background()

	evs := agentCall(ctx, t, gw.URL, "claude", sseAccept, callBody("fake__logs", map[string]any{"who": "lone"}, "solo"))
	if len(evs) != 3 || string(evs[0]["method"]) != `"notifications/message"` {
		t.Fatalf("a lone call: %v, want its log line, progress and result", show(evs))
	}
	if tok, msg := progressOf(t, evs[1]); string(tok) != `"solo"` || msg != "lone" {
		t.Fatalf("a lone call's progress: %s %q", tok, msg)
	}

	type got struct {
		who string
		evs []map[string]json.RawMessage
	}
	out := make(chan got, 2)
	for _, c := range []struct {
		who   string
		token any
	}{{"a", "tok-a"}, {"b", 7}} {
		go func() {
			out <- got{c.who, agentCall(ctx, t, gw.URL, "claude", sseAccept, callBody("fake__together", map[string]any{"who": c.who}, c.token))}
		}()
	}
	want := map[string]string{"a": `"tok-a"`, "b": `7`}
	for range 2 {
		var g got
		select {
		case g = <-out:
		case <-time.After(60 * time.Second):
			t.Fatal("the two calls weren't answered")
		}
		if len(g.evs) != 2 {
			t.Fatalf("call %s: %v, want its own progress and its result", g.who, show(g.evs))
		}
		if tok, msg := progressOf(t, g.evs[0]); string(tok) != want[g.who] || msg != g.who {
			t.Fatalf("call %s was told progress %s %q", g.who, tok, msg)
		}
		if text := textOf(t, g.evs[1]); text != `done "`+g.who+`"` {
			t.Fatalf("call %s: result %q", g.who, text)
		}
	}
}
