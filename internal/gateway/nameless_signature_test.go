package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// namelessSig is the thought signature namelessSigUp gives the step's
// first call, the one that names no function.
const namelessSig = "CiQBbmFtZWxlc3MtY2FsbC1zaWduYXR1cmUtYnl0ZXMtLT4/Pz8="

// namelessSigUp is Gemini on chat completions, on Google's host or a
// relay's (Kisxndbskx on X): the first turn answers with a step whose
// first call names no function and carries the step's signature in
// extra_content, then a named call with none, in the shape Gemini's
// OpenAI-compatible API streams its calls (no index, as Linka's frames in
// gemini_compat_cut_test.go). A request that sends calls back has the
// first call of each assistant turn carry a signature, as Google checks,
// or is turned away with Google's 400.
type namelessSigUp struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (u *namelessSigUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var b map[string]any
	json.NewDecoder(r.Body).Decode(&b)
	u.mu.Lock()
	u.bodies = append(u.bodies, b)
	u.mu.Unlock()
	stream, _ := b["stream"].(bool)
	msgs, _ := b["messages"].([]any)
	answered := false
	for _, m := range msgs {
		calls, _ := m.(map[string]any)["tool_calls"].([]any)
		if len(calls) == 0 {
			continue
		}
		answered = true
		first, _ := calls[0].(map[string]any)
		extra, _ := first["extra_content"].(map[string]any)
		google, _ := extra["google"].(map[string]any)
		if sig, _ := google["thought_signature"].(string); sig == "" {
			w.WriteHeader(400)
			io.WriteString(w, `[{"error":{"code":400,"message":"Function call is missing a thought_signature in functionCall parts. This is required for tools to work correctly, and missing thought_signature may lead to degraded model performance. Additional data, function call `+"`default_api:bash`"+` , position 2. Please refer to https://ai.google.dev/gemini-api/docs/thought-signatures for more details.","status":"INVALID_ARGUMENT"}}]`)
			return
		}
	}
	if answered {
		sigReply(w, stream, map[string]any{"role": "assistant", "content": "done"}, "stop")
		return
	}
	nameless := `{"extra_content":{"google":{"thought_signature":"` + namelessSig + `"}},"function":{"arguments":"{}","name":""},"id":"function-call-1","type":"function"}`
	named := `{"function":{"arguments":"{\"command\":\"ls\"}","name":"bash"},"id":"function-call-2","type":"function"}`
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"finish_reason":"tool_calls","index":0,"message":{"role":"assistant","tool_calls":[`+nameless+`,`+named+`]}}],"created":1,"id":"x","model":"gemini-4-argon","object":"chat.completion","usage":{"completion_tokens":30,"prompt_tokens":20,"total_tokens":50}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, `data: {"choices":[{"delta":{"role":"assistant","tool_calls":[`+nameless+`]},"index":0}],"created":1,"id":"x","model":"gemini-4-argon","object":"chat.completion.chunk"}`+"\n\n"+
		`data: {"choices":[{"delta":{"role":"assistant","tool_calls":[`+named+`]},"index":0}],"created":1,"id":"x","model":"gemini-4-argon","object":"chat.completion.chunk"}`+"\n\n"+
		`data: {"choices":[{"delta":{"role":"assistant"},"finish_reason":"tool_calls","index":0}],"created":1,"id":"x","model":"gemini-4-argon","object":"chat.completion.chunk","usage":{"completion_tokens":30,"prompt_tokens":20,"total_tokens":50}}`+"\n\n"+
		"data: [DONE]\n\n")
}

func (u *namelessSigUp) last(t *testing.T) map[string]any {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.bodies) == 0 {
		t.Fatal("the upstream was asked nothing")
	}
	return u.bodies[len(u.bodies)-1]
}

// Kisxndbskx on X (v0.1.4795): Codex on Gemini through chat completions.
// In one step Gemini gave a call naming no function, with the step's
// thought signature, then a named call. The nameless call never reaches
// the client (namelessEncoder, namelessResult), and its signature went
// with it: the named call came back on the next turn with none, and a
// relay serving Gemini answered 400 "missing a thought_signature" (Google's
// own host was sent skip_thought_signature_validator in its place). The
// signature now goes to the named call, on Google's host and a relay's,
// to Codex and Claude Code, streamed and whole.
func TestNamelessCallHandsOnItsSignature(t *testing.T) {
	tools := map[string]string{
		"/v1/responses": `[{"type":"function","name":"bash","parameters":{"type":"object"}}]`,
		"/v1/messages":  `[{"name":"bash","input_schema":{"type":"object"}}]`,
	}
	first := map[string]string{
		"/v1/responses": `{"model":"%m","stream":%s,"input":[{"role":"user","content":"list files"}],"tools":%t}`,
		"/v1/messages":  `{"model":"%m","max_tokens":1000,"stream":%s,"messages":[{"role":"user","content":"list files"}],"tools":%t}`,
	}
	// the next turn as each client keeps the step: the named call it was
	// given, by its id, and its answer
	second := map[string]string{
		"/v1/responses": `{"model":"%m","stream":%s,"input":[
			{"role":"user","content":"list files"},
			{"type":"function_call","call_id":"%id","name":"bash","arguments":"{\"command\":\"ls\"}"},
			{"type":"function_call_output","call_id":"%id","output":"a.txt"}],"tools":%t}`,
		"/v1/messages": `{"model":"%m","max_tokens":1000,"stream":%s,"messages":[
			{"role":"user","content":"list files"},
			{"role":"assistant","content":[{"type":"tool_use","id":"%id","name":"bash","input":{"command":"ls"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"%id","content":"a.txt"}]}],"tools":%t}`,
	}
	idIn := regexp.MustCompile(`"(?:id|call_id)":"(function-call-2[^"]*)"`)
	for _, relay := range []bool{true, false} {
		for _, path := range []string{"/v1/responses", "/v1/messages"} {
			for _, stream := range []bool{true, false} {
				name := map[bool]string{true: "relay", false: "google"}[relay] + strings.TrimPrefix(path, "/v1") + map[bool]string{true: "-streamed", false: "-whole"}[stream]
				t.Run(name, func(t *testing.T) {
					up := &namelessSigUp{}
					srv := httptest.NewServer(up)
					t.Cleanup(srv.Close)
					fresh(t)
					p := provider.Provider{ID: "peta-gemini", Name: "peta-gemini", Key: "k", Models: []string{"gemini-4-argon"}, Chat: srv.URL + "/v1"}
					if relay {
						// a relay on a host of its own, reached at the
						// test's server
						p.Chat = "http://api.peta.example.com/v1"
					}
					if err := provider.Save(p); err != nil {
						t.Fatal(err)
					}
					s := New()
					if relay {
						addr := strings.TrimPrefix(srv.URL, "http://")
						s.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
							return (&net.Dialer{}).DialContext(ctx, network, addr)
						}}}
					}
					fill := func(b, id string) string {
						b = strings.ReplaceAll(b, "%m", "peta-gemini/gemini-4-argon")
						b = strings.ReplaceAll(b, "%t", tools[path])
						b = strings.ReplaceAll(b, "%s", map[bool]string{true: "true", false: "false"}[stream])
						return strings.ReplaceAll(b, "%id", id)
					}
					out := sigPost(t, s, path, fill(first[path], ""))
					if strings.Contains(out, "function-call-1") {
						t.Fatalf("the client was handed the call naming nothing:\n%s", out)
					}
					m := idIn.FindStringSubmatch(out)
					if m == nil {
						t.Fatalf("no named call in the reply:\n%s", out)
					}
					sigPost(t, s, path, fill(second[path], m[1]))
					var call, result map[string]any
					for _, m := range up.last(t)["messages"].([]any) {
						m := m.(map[string]any)
						if cs, ok := m["tool_calls"].([]any); ok {
							call = cs[0].(map[string]any)
						}
						if m["role"] == "tool" {
							result = m
						}
					}
					if call == nil || result == nil {
						t.Fatalf("the upstream was sent %v", up.last(t)["messages"])
					}
					extra, _ := call["extra_content"].(map[string]any)
					google, _ := extra["google"].(map[string]any)
					if google["thought_signature"] != namelessSig || call["id"] != "function-call-2" || result["tool_call_id"] != "function-call-2" {
						t.Fatalf("the upstream was sent the call %v and the result for %v", call, result["tool_call_id"])
					}
				})
			}
		}
	}
}

// A client that kept the nameless signed call in its history (from
// before its replies left one out) sends it back: it goes nowhere, and
// its signature goes to the named call after it, on every API a client
// speaks. Relayed bodies keep each call and its answer paired.
func TestNamelessCallInHistoryHandsOnItsSignature(t *testing.T) {
	fresh(t)
	signed := signedID("function-call-1", namelessSig)
	asks := map[provider.Protocol]string{
		provider.Responses: `{"model":"m","input":[
			{"role":"user","content":"list files"},
			{"type":"function_call","call_id":"` + signed + `","name":"","arguments":"{}"},
			{"type":"function_call","call_id":"function-call-2","name":"bash","arguments":"{\"command\":\"ls\"}"},
			{"type":"function_call_output","call_id":"` + signed + `","output":"no such tool"},
			{"type":"function_call_output","call_id":"function-call-2","output":"a.txt"}],
			"tools":[{"type":"function","name":"bash","parameters":{"type":"object"}}]}`,
		provider.Chat: `{"model":"m","messages":[
			{"role":"user","content":"list files"},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"` + signed + `","type":"function","function":{"name":"","arguments":"{}"}},
				{"id":"function-call-2","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}}]},
			{"role":"tool","tool_call_id":"` + signed + `","content":"no such tool"},
			{"role":"tool","tool_call_id":"function-call-2","content":"a.txt"}],
			"tools":[{"type":"function","function":{"name":"bash","parameters":{"type":"object"}}}]}`,
		provider.Anthropic: `{"model":"m","max_tokens":16,"messages":[
			{"role":"user","content":"list files"},
			{"role":"assistant","content":[
				{"type":"tool_use","id":"` + signed + `","name":"","input":{}},
				{"type":"tool_use","id":"function-call-2","name":"bash","input":{"command":"ls"}}]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"` + signed + `","content":"no such tool"},
				{"type":"tool_result","tool_use_id":"function-call-2","content":"a.txt"}]}],
			"tools":[{"name":"bash","input_schema":{"type":"object"}}]}`,
	}
	// a Chat client that keeps extra_content gives the signature there
	extraAsk := strings.Replace(asks[provider.Chat], `"id":"`+signed+`","type":"function","function":{"name":"","arguments":"{}"}`,
		`"id":"function-call-1","type":"function","function":{"name":"","arguments":"{}"},"extra_content":{"google":{"thought_signature":"`+namelessSig+`"}}`, 1)
	extraAsk = strings.Replace(extraAsk, `"tool_call_id":"`+signed+`"`, `"tool_call_id":"function-call-1"`, 1)
	type tc struct {
		name, ask string
		proto     provider.Protocol
	}
	cases := []tc{
		{"responses", asks[provider.Responses], provider.Responses},
		{"chat", asks[provider.Chat], provider.Chat},
		{"chat-extra", extraAsk, provider.Chat},
		{"messages", asks[provider.Anthropic], provider.Anthropic},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := []byte(c.ask)
			// what attempt does to a client's body before any route; a
			// Messages body translated keeps it until withoutNamelessCalls
			switch c.proto {
			case provider.Responses:
				body = namelessItemsOut(body)
			case provider.Chat:
				body = namelessChatCallsOut(body)
			}
			if !json.Valid(body) {
				t.Fatalf("not JSON: %s", body)
			}
			if c.proto != provider.Anthropic && (strings.Contains(string(body), "function-call-1") || strings.Contains(string(body), "no such tool")) {
				t.Fatalf("the nameless call or its answer is still in: %s", body)
			}
			// translated, to Gemini on chat completions and natively
			r, err := parseRequest(c.proto, body, false, false)
			if err != nil {
				t.Fatal(err)
			}
			var parts []Part
			for _, m := range r.Messages {
				parts = append(parts, m.Parts...)
			}
			var named, result *Part
			for i := range parts {
				switch parts[i].Kind {
				case ToolCall:
					named = &parts[i]
				case ToolResult:
					result = &parts[i]
				}
			}
			if named == nil || result == nil || named.ID != "function-call-2" || named.Signature != namelessSig || result.CallID != "function-call-2" {
				t.Fatalf("read as the call %+v and the result %+v", named, result)
			}
			if out := string(buildChat(r, "gemini-4-argon", aiStudioHost, false)); !strings.Contains(out, namelessSig) || strings.Contains(out, skipSignature) {
				t.Errorf("Gemini on chat completions was sent %s", out)
			}
			if out, err := buildGemini(r, "gemini-4-argon"); err != nil || !strings.Contains(string(out), namelessSig) {
				t.Errorf("Gemini was sent %s (%v)", out, err)
			}
			if c.proto != provider.Chat {
				return
			}
			// relayed as written, to Gemini's chat completions
			var q struct {
				Messages []struct {
					ToolCallID string `json:"tool_call_id"`
					ToolCalls  []struct {
						ID    string          `json:"id"`
						Extra json.RawMessage `json:"extra_content"`
					} `json:"tool_calls"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(chatCallSignatures(body, true, "google", true), &q); err != nil {
				t.Fatal(err)
			}
			if len(q.Messages) != 3 || len(q.Messages[1].ToolCalls) != 1 {
				t.Fatalf("relayed %+v", q.Messages)
			}
			call := q.Messages[1].ToolCalls[0]
			if call.ID != "function-call-2" || googleSignature(call.Extra) != namelessSig || q.Messages[2].ToolCallID != "function-call-2" {
				t.Errorf("relayed the call %s %s and the result for %s", call.ID, call.Extra, q.Messages[2].ToolCallID)
			}
		})
	}
}

// A call with no Gemini signature hands nothing on: another vendor's
// reply and history, with a nameless call before a named one, keep the
// named call's id as it came, and a named call's own signature stays.
func TestNamelessUnsignedHandsNothing(t *testing.T) {
	fresh(t)
	spy := &crSpy{}
	enc := &namelessEncoder{streamEncoder: spy}
	for _, ev := range []Event{
		{Kind: KStart},
		{Kind: KToolStart, ID: "call_bad", Name: ""},
		{Kind: KToolArgs, Text: "{}"},
		{Kind: KToolStart, ID: "call_ok", Name: "Read"},
		{Kind: KToolArgs, Text: `{"file_path":"/x"}`},
		{Kind: KStop, Stop: "tool"},
	} {
		enc.event(ev)
	}
	var ids []string
	for _, ev := range spy.said {
		if ev.Kind == KToolStart {
			ids = append(ids, ev.ID)
		}
	}
	if len(ids) != 1 || ids[0] != "call_ok" {
		t.Errorf("streamed calls %q, want [call_ok]", ids)
	}
	res := namelessResult(Result{Stop: "tool", Parts: []Part{
		{Kind: ToolCall, ID: "call_bad", Name: ""}, {Kind: ToolCall, ID: "call_ok", Name: "Read"},
	}})
	if len(res.Parts) != 1 || res.Parts[0].ID != "call_ok" {
		t.Errorf("whole reply's calls %+v", res.Parts)
	}

	// a named call with a signature of its own keeps it
	own := signedID("call_ok", "b3duLXNpZw==")
	res = namelessResult(Result{Stop: "tool", Parts: []Part{
		{Kind: ToolCall, ID: signedID("call_bad", namelessSig), Name: ""}, {Kind: ToolCall, ID: own, Name: "Read"},
	}})
	if len(res.Parts) != 1 || res.Parts[0].ID != own {
		t.Errorf("a signed named call became %+v", res.Parts)
	}

	// history: Claude Code's turn on another vendor (#7565)
	r, err := parseRequest(provider.Anthropic, []byte(namelessAsk), false, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range r.Messages {
		for _, p := range m.Parts {
			if p.Kind == ToolCall && (p.ID != "call_ok" || p.Signature != "") {
				t.Errorf("the named call became %+v", p)
			}
		}
	}
	for proto, ask := range map[provider.Protocol]string{
		provider.Responses: `{"model":"m","input":[{"type":"function_call","call_id":"call_bad","name":"","arguments":"{}"},{"type":"function_call","call_id":"call_ok","name":"Read","arguments":"{}"},{"type":"function_call_output","call_id":"call_ok","output":"ok"}]}`,
		provider.Chat:      `{"model":"m","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_bad","type":"function","function":{"name":"","arguments":"{}"}},{"id":"call_ok","type":"function","function":{"name":"Read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_ok","content":"ok"}]}`,
	} {
		var out []byte
		if proto == provider.Responses {
			out = namelessItemsOut([]byte(ask))
		} else {
			out = namelessChatCallsOut([]byte(ask))
		}
		if strings.Count(string(out), `"call_ok"`) != 2 || strings.Contains(string(out), sigMark) || strings.Contains(string(out), "extra_content") {
			t.Errorf("%s: relayed %s", proto, out)
		}
	}
}
