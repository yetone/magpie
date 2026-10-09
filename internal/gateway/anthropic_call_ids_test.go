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

// chatCallUpstream is a Chat upstream (a plugin's provider, a relay) that
// answers every request with one Bash call under id, and keeps the bodies
// it was sent.
func chatCallUpstream(t *testing.T, id string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, string(b))
		mu.Unlock()
		quoted, _ := json.Marshal(id)
		if !strings.Contains(string(b), `"stream":true`) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":`+string(quoted)+`,"type":"function","function":{"name":"Bash","arguments":"{\"command\":\"date\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":5}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range []string{
			`{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":` + string(quoted) + `,"type":"function","function":{"name":"Bash","arguments":""}}]}}]}`,
			`{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":\"date\"}"}}]}}]}`,
			`{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":5}}`,
		} {
			io.WriteString(w, "data: "+c+"\n\n")
		}
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(up.Close)
	return up, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

// anthropicCallID is the id of the tool_use in an Anthropic reply, streamed
// or whole.
func anthropicCallID(t *testing.T, body string) string {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev struct {
			ContentBlock struct{ Type, ID string } `json:"content_block"`
		}
		if json.Unmarshal([]byte(data), &ev) == nil && ev.ContentBlock.Type == "tool_use" {
			return ev.ContentBlock.ID
		}
	}
	var m struct{ Content []struct{ Type, ID string } }
	json.Unmarshal([]byte(body), &m)
	for _, p := range m.Content {
		if p.Type == "tool_use" {
			return p.ID
		}
	}
	t.Fatalf("no tool_use in the reply: %s", body)
	return ""
}

// chatIDsSent is the ids of the calls and of the results in a Chat request.
func chatIDsSent(t *testing.T, body string) (calls, answers []string) {
	t.Helper()
	var q struct {
		Messages []struct {
			ToolCalls  []struct{ ID string } `json:"tool_calls"`
			ToolCallID string                `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &q); err != nil {
		t.Fatalf("upstream request: %v: %s", err, body)
	}
	for _, m := range q.Messages {
		for _, c := range m.ToolCalls {
			calls = append(calls, c.ID)
		}
		if m.ToolCallID != "" {
			answers = append(answers, m.ToolCallID)
		}
	}
	return
}

// callAndAnswer asks for a call through /v1/messages, then answers it the
// way Claude Code does, under the id it was given: the id the client saw,
// and the upstream's second request.
func callAndAnswer(t *testing.T, s *Server, stream bool, sent func() []string) (string, string) {
	t.Helper()
	post := func(body string) string {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	st := "false"
	if stream {
		st = "true"
	}
	ask := `{"role":"user","content":"用 Bash 跑一下 date"}`
	id := anthropicCallID(t, post(`{"model":"up/m1","max_tokens":100,"stream":`+st+`,"messages":[`+ask+`]}`))
	before := len(sent())
	quoted, _ := json.Marshal(id)
	post(`{"model":"up/m1","max_tokens":100,"stream":` + st + `,"messages":[` + ask + `,
 {"role":"assistant","content":[{"type":"tool_use","id":` + string(quoted) + `,"name":"Bash","input":{"command":"date"}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":` + string(quoted) + `,"content":"Thu Oct  8 10:00:00 CST 2026"}]}]}`)
	got := sent()
	if len(got) != before+1 {
		t.Fatalf("upstream got %d requests for the answer, want 1", len(got)-before)
	}
	return id, got[len(got)-1]
}

// A call id with a character Claude Code doesn't take, from any upstream
// (#1304: Devin SWE-2's, here through a Chat provider as a plugin or relay
// gives it), reaches the Anthropic client of [A-Za-z0-9_-] only, and the
// upstream gets its own id back on the call and on the result answering it.
func TestAnthropicCallIDsSafeBothWays(t *testing.T) {
	fresh(t)
	up, sent := chatCallUpstream(t, devinRawID)
	if err := provider.Save(provider.Provider{ID: "up", Name: "Up", Key: "k", Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	for _, stream := range []bool{true, false} {
		id, next := callAndAnswer(t, s, stream, sent)
		if !anthropicSafeID.MatchString(id) {
			t.Fatalf("stream=%v: call id %q, want one of [A-Za-z0-9_-] only", stream, id)
		}
		calls, answers := chatIDsSent(t, next)
		if len(calls) != 1 || calls[0] != devinRawID || len(answers) != 1 || answers[0] != devinRawID {
			t.Errorf("stream=%v: upstream got calls %q, results %q; want %q for both", stream, calls, answers, devinRawID)
		}
	}
}

// An id that is safe already goes to the client and back byte for byte, so
// prompt caching and every upstream that works today see no change;
// Devin's "dv_…" ids are not encoded a second time.
func TestAnthropicSafeCallIDsUnchanged(t *testing.T) {
	for _, id := range []string{"toolu_01A09q90qw90lq917835lq9", "call_abc-123", "dv_QmFzaDowI2E2NWI2YTVl", devinOutID(devinRawID)} {
		if got := anthropicOutID(id); got != id {
			t.Errorf("anthropicOutID(%q) = %q, want it as it came", id, got)
		}
		if got := anthropicInID(id); got != id {
			t.Errorf("anthropicInID(%q) = %q, want it as it came", id, got)
		}
	}
	// a client's own id that looks like one of magpie's goes back as it came
	for _, id := range []string{"mp_", "mp_!!", "mp_abc"} {
		if anthropicInID(id) != id && anthropicOutID(anthropicInID(id)) != id {
			t.Errorf("anthropicInID(%q) = %q, which doesn't go back to it", id, anthropicInID(id))
		}
	}
	// an upstream's safe id that begins "mp_" still comes back as it was
	if out := anthropicOutID("mp_abc"); anthropicInID(out) != "mp_abc" || !anthropicSafeID.MatchString(out) {
		t.Errorf(`"mp_abc" went out as %q and came back as %q`, out, anthropicInID(out))
	}

	fresh(t)
	const safe = "call_9f8e7d6c5b4a"
	up, sent := chatCallUpstream(t, safe)
	if err := provider.Save(provider.Provider{ID: "up", Name: "Up", Key: "k", Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	for _, stream := range []bool{true, false} {
		id, next := callAndAnswer(t, s, stream, sent)
		calls, answers := chatIDsSent(t, next)
		if id != safe || len(calls) != 1 || calls[0] != safe || len(answers) != 1 || answers[0] != safe {
			t.Errorf("stream=%v: client got %q; upstream got calls %q, results %q; want %q everywhere", stream, id, calls, answers, safe)
		}
	}
}
