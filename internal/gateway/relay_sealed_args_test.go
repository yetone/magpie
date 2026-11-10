package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// relaySealedToken stands for what the ChatGPT backend puts in a
// collaboration call's message when it seals it (a Fernet token).
const relaySealedToken = "gAAAAABpTestSealedResult_0123456789abcdefABCDEF-_xyz=="

// codexSendTools is Codex's collaboration namespace with send_message, as
// a subagent is offered it.
const codexSendTools = `"tools":[
	{"type":"function","name":"exec_command","parameters":{"type":"object"}},
	{"type":"namespace","name":"collaboration","description":"Tools in the collaboration namespace.","tools":[
		{"type":"function","name":"send_message","description":"Sends a message.","parameters":{"type":"object","properties":{"target":{"type":"string"},"message":{"type":"string","encrypted":true}}}},
		{"type":"function","name":"spawn_agent","description":"Spawns an agent.","parameters":{"type":"object","properties":{"message":{"type":"string","encrypted":true}}}}
	]}
]`

// brethhbbm on X ("codex subagent 返回的加密内容主agent 无法解析"), and
// oevery and WilliamK7 on #3183: a Responses relay of the ChatGPT backend
// (CPA, Sub2API) seals a subagent's send_message as the backend does but
// leaves encrypted_function_args out. magpie gave every such call the
// empty list, saying the message was plain text, so Codex handed the lead
// the token as text and the lead could read nothing of its subagent's
// result. A call whose message is sealed now names it; a plain one still
// gets the empty list, and a stream's first, argument-less item is left
// as it came.
func TestRelaySealedMessageNamedSealed(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var sent []string
	reply := `{"type":"function_call","id":"fc_1","call_id":"c9","name":"send_message","namespace":"collaboration","arguments":"{\"target\":\"/root\",\"message\":\"` + relaySealedToken + `\"}","status":"completed"}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = append(sent, string(b))
		mu.Unlock()
		var q map[string]any
		json.Unmarshal(b, &q)
		done := `{"id":"r1","object":"response","status":"completed","output":[` + reply + `],"usage":{"input_tokens":5,"output_tokens":3}}`
		if q["stream"] != true {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, done)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","object":"response","status":"in_progress","output":[]}}`,
			`event: response.output_item.added`+"\n"+`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"c9","name":"send_message","namespace":"collaboration","arguments":""}}`,
			`event: response.output_item.done`+"\n"+`data: {"type":"response.output_item.done","output_index":0,"item":`+reply+`}`,
			`event: response.completed`+"\n"+`data: {"type":"response.completed","response":`+done+`}`))
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "cpa", Name: "CPA", Key: "k", Models: []string{"gpt-6-astra"}, Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	for _, stream := range []bool{true, false} {
		body := `{"model":"cpa/gpt-6-astra","stream":` + map[bool]string{true: "true", false: "false"}[stream] + `,` + codexSendTools + `,"input":[` + plainTask + `]}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
		req.Header.Set("session_id", "worker-1")
		req.Header.Set("x-codex-parent-thread-id", "lead-1")
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("stream %v: %d %s", stream, rec.Code, rec.Body)
		}
		var items []map[string]any
		var added map[string]any
		if stream {
			for _, line := range strings.Split(rec.Body.String(), "\n") {
				data, ok := strings.CutPrefix(line, "data: ")
				if !ok {
					continue
				}
				var ev struct {
					Type     string         `json:"type"`
					Item     map[string]any `json:"item"`
					Response struct {
						Output []map[string]any `json:"output"`
					} `json:"response"`
				}
				if err := json.Unmarshal([]byte(data), &ev); err != nil {
					t.Fatalf("%v: %s", err, data)
				}
				if ev.Type == "response.output_item.added" {
					added = ev.Item
					continue
				}
				if ev.Item != nil {
					items = append(items, ev.Item)
				}
				items = append(items, ev.Response.Output...)
			}
			if added == nil {
				t.Fatalf("no output_item.added: %s", rec.Body)
			}
			if v, ok := added["encrypted_function_args"]; ok {
				t.Fatalf("the argument-less first item was given %v", v)
			}
		} else {
			var res struct {
				Output []map[string]any `json:"output"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
				t.Fatalf("%v: %s", err, rec.Body)
			}
			items = res.Output
		}
		if len(items) == 0 || stream && len(items) != 2 {
			t.Fatalf("stream %v: %s", stream, rec.Body)
		}
		for _, it := range items {
			sealed, _ := it["encrypted_function_args"].([]any)
			if len(sealed) != 1 || sealed[0] != "message" {
				t.Fatalf("stream %v: Codex was given %v, the sealed message read as plain text", stream, it)
			}
			if args, _ := it["arguments"].(string); !strings.Contains(args, relaySealedToken) {
				t.Fatalf("stream %v: the token was changed: %v", stream, it)
			}
		}
	}

	// Codex then hands the lead the message sealed, and the lead's turn,
	// on the same relay, carries it to the relay as it came: it opens it
	mu.Lock()
	sent = nil
	mu.Unlock()
	sealed := `{"type":"agent_message","author":"/root/worker","recipient":"/root","content":[{"type":"input_text","text":"Message Type: MESSAGE\nTask name: /root\nSender: /root/worker\nPayload:\n"},{"type":"encrypted_content","encrypted_content":"` + relaySealedToken + `"}]}`
	for i, input := range []string{leadAsk, leadAsk + `,` + sealed} {
		body := `{"model":"cpa/gpt-6-astra","stream":false,` + codexSendTools + `,"input":[` + input + `]}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
		req.Header.Set("session_id", "lead-1")
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("lead turn %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 || !strings.Contains(sent[1], `"encrypted_content":"`+relaySealedToken+`"`) {
		t.Fatalf("the lead's turn reached the relay as %q", sent)
	}
}

// A plain message on a relay still says so, and one whose arguments
// aren't an object is left as it came.
func TestSealedArgs(t *testing.T) {
	for _, tc := range []struct {
		args string
		want []any
		ok   bool
	}{
		{`{"message":"go"}`, []any{}, true},
		{`{"target":"/root","message":"` + relaySealedToken + `"}`, []any{"message"}, true},
		{`{"message":"gAAAAA is how a token starts"}`, []any{}, true},
		{``, nil, false},
		{relaySealedToken, nil, false},
	} {
		got, ok := sealedArgs(tc.args)
		if ok != tc.ok || len(got) != len(tc.want) || len(got) == 1 && got[0] != tc.want[0] {
			t.Errorf("sealedArgs(%q) = %v %v, want %v %v", tc.args, got, ok, tc.want, tc.ok)
		}
	}
}
