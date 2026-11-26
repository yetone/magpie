package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// strictRelay checks the request as CHCP and Keenc's Vertex relay do
// (pydantic, extra fields forbidden): thinking.display only summarized or
// omitted, and nothing but role and content on a message.
type strictRelay struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (v *strictRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	json.Unmarshal(b, &m)
	v.mu.Lock()
	v.bodies = append(v.bodies, m)
	v.mu.Unlock()
	var errs []string
	th, _ := m["thinking"].(map[string]any)
	if d, ok := th["display"]; ok && d != "summarized" && d != "omitted" {
		errs = append(errs, fmt.Sprintf("thinking.%s.display: Input should be 'summarized', 'omitted'", th["type"]))
	}
	msgs, _ := m["messages"].([]any)
	for i, x := range msgs {
		for k := range x.(map[string]any) {
			if k != "role" && k != "content" {
				errs = append(errs, fmt.Sprintf("messages.%d.%s: Extra inputs are not permitted", i, k))
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if len(errs) > 0 {
		w.WriteHeader(400)
		e, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "message": strings.Join(errs, "; ")}})
		w.Write(e)
		return
	}
	io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
}

func (v *strictRelay) all() []map[string]any {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Clone(v.bodies)
}

// Keenc on Discord: Claude Code 2.1.288 through magpie to a relay got
// "thinking.adaptive.display: Input should be 'summarized', 'omitted'" (its
// display "updates") and "messages.1.output_config: Extra inputs are not
// permitted" (a turn's effort on a system message). magpie asks the relay
// again without them, and from then on sends it none; the request's own
// effort and a system message's text stay.
func TestRelayRefusingClaudeCodesNewShapes(t *testing.T) {
	fresh(t)
	up := &strictRelay{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "chcp", Name: "CHCP", Key: "k", Anthropic: srv.URL,
		Models: []string{"claude-opus-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetUpstreamName("chcp/claude-opus-5.5", "claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	// as Claude Code sends it: an environment system message after the
	// first user message, and the second turn's effort on an empty one
	body := `{"model":"chcp/claude-opus-5.5","max_tokens":64000,` +
		`"thinking":{"type":"adaptive","display":"updates"},"output_config":{"effort":"low"},"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"say pong"}]},` +
		`{"role":"system","content":[{"type":"text","text":"# Environment <cwd>"}],"output_config":{"effort":"high"}},` +
		`{"role":"assistant","content":[{"type":"text","text":"pong"}]},` +
		`{"role":"user","content":[{"type":"text","text":"again"}]},` +
		`{"role":"system","content":[],"output_config":{"effort":"low"}}]}`
	check := func(t *testing.T, got map[string]any) {
		t.Helper()
		if th, _ := json.Marshal(got["thinking"]); string(th) != `{"type":"adaptive"}` {
			t.Errorf("thinking = %s, want display left out", th)
		}
		if oc, _ := json.Marshal(got["output_config"]); string(oc) != `{"effort":"low"}` {
			t.Errorf("output_config = %s, want the request's own", oc)
		}
		msgs, _ := got["messages"].([]any)
		var roles []string
		for _, x := range msgs {
			m := x.(map[string]any)
			roles = append(roles, m["role"].(string))
			if _, ok := m["output_config"]; ok {
				t.Errorf("a message kept its output_config: %v", m)
			}
		}
		if r := strings.Join(roles, ","); r != "user,system,assistant,user" {
			t.Errorf("roles = %s, want the empty system message dropped and the environment kept", r)
		}
		if c, _ := msgs[1].(map[string]any)["content"].([]any); len(c) != 1 || c[0].(map[string]any)["text"] != "# Environment <cwd>" {
			t.Errorf("system message content = %v", c)
		}
	}

	h := New().Handler() // one gateway, which learns
	post := func(body string) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		return rec.Code, rec.Body.String()
	}
	code, out := post(body)
	if code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	sent := up.all()
	if len(sent) < 2 {
		t.Fatalf("relay asked %d times, want again after its 400", len(sent))
	}
	check(t, sent[len(sent)-1])

	// learned: the next request goes as the relay takes it, the first time
	n := len(sent)
	if code, out = post(body); code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	if sent = up.all(); len(sent) != n+1 {
		t.Fatalf("relay asked %d more times, want once", len(sent)-n)
	}
	check(t, sent[len(sent)-1])
}

// A provider that takes them (Anthropic's own API, a relay passing them
// on) is sent them as Claude Code wrote them; "highlights" goes as
// "summarized" to one that takes only the two.
func TestClaudeCodesNewShapesKeptWhereTaken(t *testing.T) {
	fresh(t)
	up := &adaptiveVendor{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "anth", Name: "Anth", Key: "k", Anthropic: srv.URL,
		Models: []string{"claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"anth/claude-opus-5-5","max_tokens":64000,"thinking":{"type":"adaptive","display":"updates"},"messages":[` +
		`{"role":"user","content":"hi"},{"role":"system","content":[],"output_config":{"effort":"high"}}]}`
	if code, out := post(t, "/v1/messages", body); code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	got := up.last()
	if th, _ := json.Marshal(got["thinking"]); string(th) != `{"display":"updates","type":"adaptive"}` {
		t.Errorf("thinking = %s, want as sent", th)
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 2 || msgs[1].(map[string]any)["output_config"] == nil {
		t.Errorf("messages = %v, want as sent", msgs)
	}

	if got := withDisplay([]byte(`{"thinking":{"type":"adaptive","display":"highlights"}}`), false); string(got) != `{"thinking":{"display":"summarized","type":"adaptive"}}` {
		t.Errorf("highlights = %s", got)
	}
	if got := withDisplay([]byte(`{"thinking":{"type":"adaptive","display":"omitted"}}`), false); string(got) != `{"thinking":{"type":"adaptive","display":"omitted"}}` {
		t.Errorf("omitted = %s, want kept", got)
	}
}

// commandCodeAPI checks messages as Command Code's Provider API does: a
// role other than user or assistant inside messages (#1407), or a block it
// doesn't read in a message's content or a tool_result's (a tool_reference
// beside text too, #7833), is turned away by the message's index alone.
type commandCodeAPI struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (v *commandCodeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	json.Unmarshal(b, &m)
	v.mu.Lock()
	v.bodies = append(v.bodies, m)
	v.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	msgs, _ := m["messages"].([]any)
	for i, x := range msgs {
		if r := x.(map[string]any)["role"]; r != "user" && r != "assistant" {
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"type":"error","error":{"type":"invalid_request_error","message":"Invalid input at messages.%d.role"}}`, i)
			return
		}
		if !commandCodeReads(x.(map[string]any)["content"]) {
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"type":"error","error":{"type":"invalid_request_error","message":"Invalid input at messages.%d.content"}}`, i)
			return
		}
	}
	io.WriteString(w, `{"model":"claude-sonnet-5-5","id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"OK."}],"stop_reason":"end_turn","usage":{"input_tokens":20,"output_tokens":5}}`)
}

// commandCodeReads is a message's content Command Code takes: a string,
// or blocks of the types it knows, a tool_result's content a string or
// text and image blocks.
func commandCodeReads(c any) bool {
	blocks, ok := c.([]any)
	if !ok {
		_, ok = c.(string)
		return ok
	}
	for _, x := range blocks {
		b, _ := x.(map[string]any)
		switch b["type"] {
		case "text", "image", "document", "thinking", "redacted_thinking", "tool_use":
		case "tool_result":
			inner, ok := b["content"].([]any)
			if !ok {
				continue
			}
			for _, y := range inner {
				if t := y.(map[string]any)["type"]; t != "text" && t != "image" {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

func (v *commandCodeAPI) all() []map[string]any {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Clone(v.bodies)
}

// #1407: Command Code answered Claude Code's system message inside messages
// with 400 "Invalid input at messages.1.role". magpie asks again with its
// text at the end of the top-level system and an empty one (a turn's effort
// alone) left out, and sends it that way from then on.
func TestSystemMessageRefusedGoesOnTop(t *testing.T) {
	fresh(t)
	up := &commandCodeAPI{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "commandcode", Name: "Command Code", Key: "k", Anthropic: srv.URL,
		Models: []string{"claude-sonnet-5-5"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler() // one gateway, which learns
	post := func(body string) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		return rec.Code, rec.Body.String()
	}
	roles := func(got map[string]any) string {
		var rs []string
		for _, x := range got["messages"].([]any) {
			rs = append(rs, x.(map[string]any)["role"].(string))
		}
		return strings.Join(rs, ",")
	}

	// the issue's request, byte for byte but the model's provider
	code, out := post(`{
    "model": "commandcode/claude-sonnet-5-5",
    "max_tokens": 32,
    "messages": [
      {"role": "user", "content": "Say OK."},
      {"role": "system", "content": "Use concise answers."},
      {"role": "user", "content": "Reply OK."}
    ]
  }`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	sent := up.all()
	if len(sent) != 2 {
		t.Fatalf("Command Code asked %d times, want again once after its 400", len(sent))
	}
	got := sent[1]
	if r := roles(got); r != "user,user" {
		t.Errorf("roles = %s, want the system message out of messages", r)
	}
	if sys, _ := json.Marshal(got["system"]); string(sys) != `[{"text":"Use concise answers.","type":"text"}]` {
		t.Errorf("system = %s, want the system message's text", sys)
	}

	// learned: Claude Code's own shape (a top-level system, the turn's
	// effort on an empty system message, the issue's other row) goes as
	// Command Code takes it the first time
	n := len(sent)
	code, out = post(`{"model":"commandcode/claude-sonnet-5-5","max_tokens":32,` +
		`"system":[{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}}],"messages":[` +
		`{"role":"user","content":"Say OK."},` +
		`{"role":"system","content":[],"output_config":{"effort":"high"}},` +
		`{"role":"assistant","content":[{"type":"text","text":"OK."}]},` +
		`{"role":"user","content":"Reply OK."},` +
		`{"role":"system","content":[{"type":"text","text":"# Environment <cwd>"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	if sent = up.all(); len(sent) != n+1 {
		t.Fatalf("Command Code asked %d more times, want once", len(sent)-n)
	}
	got = sent[n]
	if r := roles(got); r != "user,assistant,user" {
		t.Errorf("roles = %s", r)
	}
	if sys, _ := marshalPlain(got["system"]); string(sys) != `[{"cache_control":{"type":"ephemeral"},"text":"You are Claude Code.","type":"text"},{"text":"# Environment <cwd>","type":"text"}]` {
		t.Errorf("system = %s, want the agent's own, then the environment", sys)
	}
}

// A provider that takes system messages inside messages (Anthropic's own
// API) is sent them where the agent put them, and a role refusal over a
// user or assistant message marks nothing.
func TestSystemMessageKeptWhereTaken(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(b, &m)
		mu.Lock()
		bodies = append(bodies, m)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(b), `"bad"`) {
			w.WriteHeader(400)
			io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"Invalid input at messages.0.role"}}`)
			return
		}
		io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "anth", Name: "Anth", Key: "k", Anthropic: srv.URL,
		Models: []string{"claude-sonnet-5-5"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	post := func(body string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		return rec.Code
	}
	// refused over its first, a user message: passed on, nothing learned
	if code := post(`{"model":"anth/claude-sonnet-5-5","max_tokens":32,"messages":[{"role":"user","content":"bad"},{"role":"system","content":"Use concise answers."},{"role":"user","content":"Reply OK."}]}`); code != 400 {
		t.Fatalf("status %d, want the refusal passed on", code)
	}
	if code := post(`{"model":"anth/claude-sonnet-5-5","max_tokens":32,"messages":[{"role":"user","content":"Say OK."},{"role":"system","content":"Use concise answers."},{"role":"user","content":"Reply OK."}]}`); code != 200 {
		t.Fatalf("status %d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	last := bodies[len(bodies)-1]
	if m := last["messages"].([]any); len(m) != 3 || m[1].(map[string]any)["role"] != "system" {
		t.Errorf("messages = %v, want the system message where it was", m)
	}
	if _, ok := last["system"]; ok {
		t.Errorf("system = %v, want none added", last["system"])
	}
}
