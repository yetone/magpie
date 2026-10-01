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

// Codex's sub-agent tools, in its collaboration namespace, reach a Grok
// subscription as functions under their flat names, a call made earlier
// with them; the call Grok makes back reaches Codex under spawn_agent and
// its namespace again, streamed or not (#404).
func TestGrokGetsNamespacedTools(t *testing.T) {
	grokSignedIn(t)
	var mu sync.Mutex
	var sent []map[string]any
	call := `{"type":"function_call","id":"fc_1","call_id":"c9","name":"collaboration__spawn_agent","arguments":"{\"message\":\"go\"}","status":"completed"}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":[{"id":"grok-4.7","api_backend":"responses"}]}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var q map[string]any
		json.Unmarshal(b, &q)
		mu.Lock()
		sent = append(sent, q)
		mu.Unlock()
		done := `{"id":"r1","object":"response","status":"completed","output":[` + call + `],"usage":{"input_tokens":5,"output_tokens":3}}`
		if q["stream"] != true {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, done)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","object":"response","status":"in_progress","output":[]}}`,
			`event: response.output_item.added`+"\n"+`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"c9","name":"collaboration__spawn_agent","arguments":""}}`,
			`event: response.output_item.done`+"\n"+`data: {"type":"response.output_item.done","output_index":0,"item":`+call+`}`,
			`event: response.completed`+"\n"+`data: {"type":"response.completed","response":`+done+`}`))
	}))
	defer up.Close()
	was := provider.GrokBase
	provider.GrokBase = up.URL + "/v1"
	defer func() { provider.GrokBase = was }()

	s := New()
	for _, stream := range []bool{true, false} {
		mu.Lock()
		sent = nil
		mu.Unlock()
		body := `{"model":"grok/grok-4.7","stream":` + map[bool]string{true: "true", false: "false"}[stream] + `,` + namespacedTools + `,"input":[
			{"type":"message","role":"user","content":"go"},
			{"type":"function_call","call_id":"c1","name":"spawn_agent","namespace":"collaboration","arguments":"{}"},
			{"type":"function_call_output","call_id":"c1","output":"ok"}]}`
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("stream %v: %d %s", stream, rec.Code, rec.Body)
		}
		mu.Lock()
		if len(sent) != 1 {
			t.Fatalf("stream %v: Grok was asked %d times", stream, len(sent))
		}
		q := sent[0]
		mu.Unlock()
		var names []string
		for _, tl := range q["tools"].([]any) {
			tm := tl.(map[string]any)
			names = append(names, tm["type"].(string)+":"+tm["name"].(string))
		}
		if got := strings.Join(names, ","); got != "function:exec_command,function:collaboration__spawn_agent" {
			t.Fatalf("stream %v: Grok was offered %s", stream, got)
		}
		in := q["input"].([]any)
		if c := in[1].(map[string]any); c["name"] != "collaboration__spawn_agent" || c["namespace"] != nil || c["call_id"] != "c1" {
			t.Fatalf("stream %v: the call handed back went as %v", stream, c)
		}
		if o := in[2].(map[string]any); o["call_id"] != "c1" {
			t.Fatalf("stream %v: its output went as %v", stream, o)
		}

		// every function_call Codex reads is spawn_agent in collaboration
		var items []map[string]any
		if stream {
			for _, line := range strings.Split(rec.Body.String(), "\n") {
				data, ok := strings.CutPrefix(line, "data: ")
				if !ok {
					continue
				}
				var ev struct {
					Item     map[string]any `json:"item"`
					Response struct {
						Output []map[string]any `json:"output"`
					} `json:"response"`
				}
				if err := json.Unmarshal([]byte(data), &ev); err != nil {
					t.Fatalf("%v: %s", err, data)
				}
				if ev.Item != nil {
					items = append(items, ev.Item)
				}
				items = append(items, ev.Response.Output...)
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
		if len(items) == 0 || stream && len(items) != 3 {
			t.Fatalf("stream %v: %s", stream, rec.Body)
		}
		for _, it := range items {
			// arguments not sealed, as callTo says: an empty list
			if sealed, ok := it["encrypted_function_args"].([]any); it["name"] != "spawn_agent" || it["namespace"] != "collaboration" || it["call_id"] != "c9" || !ok || len(sealed) != 0 {
				t.Fatalf("stream %v: Codex was given %v", stream, it)
			}
		}
	}
}
