package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// mistralRefuseMessages answers as Mistral's chat completions do a message
// with a field it doesn't know (#494): 422 extra_forbidden located in the
// messages, every such field listed; top-level fields as mistralRefuse.
func mistralRefuseMessages(body []byte) (int, string) {
	if code, reply := mistralRefuse(body); code != 0 {
		return code, reply
	}
	known := []string{"role", "content", "tool_calls", "tool_call_id", "name", "prefix"}
	var q struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	json.Unmarshal(body, &q)
	var detail []string
	for i, m := range q.Messages {
		var role string
		json.Unmarshal(m["role"], &role)
		for k, v := range m {
			if !slices.Contains(known, k) {
				detail = append(detail, fmt.Sprintf(`{"type":"extra_forbidden","loc":["body","messages",%d,%q,%q],"msg":"Extra inputs are not permitted","input":%s}`, i, role, k, v))
			}
		}
	}
	if len(detail) == 0 {
		return 0, ""
	}
	return 422, `{"detail":[` + strings.Join(detail, ",") + `]}`
}

// sentMessages are the messages an upstream was sent, raw.
func sentMessages(t *testing.T, body []byte) []json.RawMessage {
	t.Helper()
	var q struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &q); err != nil {
		t.Fatalf("sent %s: %v", body, err)
	}
	return q.Messages
}

func sameJSON(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	json.Unmarshal(got, &g)
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if !bytes.Equal(gb, wb) {
		t.Errorf("sent %s\nwant %s", got, want)
	}
}

const chatWholeOK = `{"id":"c1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"No."},"finish_reason":"stop"}]}`

// OpenCode sends an earlier assistant turn's thinking back in
// reasoning_content, which Mistral turns away (#494): to Mistral it goes
// as the thinking part Mistral itself returns, ahead of the turn's text,
// in the first request; the other messages go as they were sent.
func TestMistralReasoningAsThinking(t *testing.T) {
	f := &fake{t: t, reply: chatWholeOK, ctype: "application/json", refuse: mistralRefuseMessages}
	up := setup(t, provider.Chat, f)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"},
		Preset: "mistral", Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	srv := New()
	// the issue's request
	code, body := postTo(t, srv, "/v1/chat/completions", `{"model":"m1","stream":false,"messages":[
        {"role":"user","content":"Is 91 prime?"},
        {"role":"assistant","content":"91 = 7 x 13.","reasoning_content":"Check divisibility by 7: 7*13=91."},
        {"role":"user","content":"And 97? One word."}]}`)
	if code != 200 || f.calls != 1 {
		t.Fatalf("%d after %d calls: %s", code, f.calls, body)
	}
	ms := sentMessages(t, f.got)
	if string(ms[0]) != `{"role":"user","content":"Is 91 prime?"}` || string(ms[2]) != `{"role":"user","content":"And 97? One word."}` {
		t.Errorf("other messages changed: %s", f.got)
	}
	sameJSON(t, ms[1], `{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"Check divisibility by 7: 7*13=91."}]},{"type":"text","text":"91 = 7 x 13."}]}`)

	// a tool-call turn: content null, its calls kept as they were
	calls := `[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"filePath\":\"note.txt\"}"}}]`
	f.calls = 0
	code, body = postTo(t, srv, "/v1/chat/completions", `{"model":"m1","stream":false,"store":false,"messages":[
        {"role":"user","content":"Read note.txt."},
        {"role":"assistant","content":null,"tool_calls":`+calls+`,"reasoning_content":"Let me use the read tool."},
        {"role":"tool","tool_call_id":"call_1","content":"secret: magpie"}]}`)
	if code != 200 {
		t.Fatalf("tool turn: %d after %d calls: %s", code, f.calls, body)
	}
	ms = sentMessages(t, f.got)
	sameJSON(t, ms[1], `{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"Let me use the read tool."}]}],"tool_calls":`+calls+`}`)
	sameJSON(t, ms[2], `{"role":"tool","tool_call_id":"call_1","content":"secret: magpie"}`)
}

// Another vendor that turns an earlier turn's thinking away the way
// Mistral does is asked again without it, and not sent it again; one that
// takes it (DeepSeek, Kimi) is sent it as it came.
func TestMessageReasoningRefused(t *testing.T) {
	f := &fake{t: t, reply: chatWholeOK, ctype: "application/json"}
	setup(t, provider.Chat, f)
	srv := New()
	req := `{"model":"m1","stream":false,"messages":[{"role":"user","content":"Read note.txt."},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}],"reasoning_content":"Let me read it."},` +
		`{"role":"tool","tool_call_id":"call_1","content":"secret: magpie"}]}`
	// taken: goes as it came
	if code, body := postTo(t, srv, "/v1/chat/completions", req); code != 200 || f.calls != 1 ||
		!bytes.Contains(f.got, []byte(`"reasoning_content":"Let me read it."`)) {
		t.Fatalf("%d after %d calls, sent %s: %s", code, f.calls, f.got, body)
	}
	f.refuse = mistralRefuseMessages
	for _, want := range []int{2, 1} {
		f.calls = 0
		code, body := postTo(t, srv, "/v1/chat/completions", req)
		if code != 200 || f.calls != want {
			t.Fatalf("%d after %d calls (want %d): %s", code, f.calls, want, body)
		}
		ms := sentMessages(t, f.got)
		sameJSON(t, ms[1], `{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]}`)
	}
}

// The Responses route through the same provider, with a model whose name
// says DeepSeek served at Mistral (#4906): the Chat build replays the
// turn's reasoning in reasoning_content wherever the model is served
// (chatReplaysReasoning keys on the name), and Mistral turns that field
// away — 422 extra_forbidden on every follow-up of a tool loop, where the
// direct Chat route converts it (above). The translated body is converted
// the same way.
func TestTranslatedReasoningAsThinkingForMistral(t *testing.T) {
	f := &fake{t: t, refuse: mistralRefuseMessages, reply: sse(
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{"role":"assistant","content":"pelican"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
		`data: [DONE]`)}
	up := setup(t, provider.Chat, f)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"deepseek-v4-1-flash-1017"},
		Preset: "mistral", Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	// the issue's request: a reasoning item before the call its thinking
	// was of, the call answered
	code, body := postTo(t, New(), "/v1/responses", `{"model":"fake/deepseek-v4-1-flash-1017","stream":false,"max_output_tokens":40,
		"tools":[{"type":"function","name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}],
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Read note.txt and tell me the word."}]},
			{"type":"reasoning","summary":[{"type":"summary_text","text":"I should read note.txt with the tool."}]},
			{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"note.txt\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"pelican"}]}`)
	if code != 200 || f.calls != 1 {
		t.Fatalf("%d after %d calls: %s", code, f.calls, body)
	}
	for _, m := range sentMessages(t, f.got) {
		if strings.Contains(string(m), "reasoning_content") {
			t.Fatalf("a message still carries reasoning_content: %s", f.got)
		}
	}
	var think string
	for _, m := range sentMessages(t, f.got) {
		var msg struct {
			ToolCalls []any `json:"tool_calls"`
			Content   []struct {
				Type     string `json:"type"`
				Thinking []struct {
					Text string `json:"text"`
				} `json:"thinking"`
			} `json:"content"`
		}
		if json.Unmarshal(m, &msg) != nil || len(msg.ToolCalls) == 0 {
			continue
		}
		if len(msg.Content) != 1 || msg.Content[0].Type != "thinking" {
			t.Fatalf("the turn's thinking is not the part Mistral takes: %s", m)
		}
		think = msg.Content[0].Thinking[0].Text
	}
	if think != "I should read note.txt with the tool." {
		t.Fatalf("the reasoning item's summary is not the thinking: %q", think)
	}
}

// A tool turn with no reasoning item, translated for a DeepSeek-named
// model at Mistral (#4906): the build's " " placeholder in
// reasoning_content is dropped, not sent as a blank thinking part, and
// the call goes as the client made it.
func TestTranslatedToolTurnWithoutReasoningForMistral(t *testing.T) {
	f := &fake{t: t, refuse: mistralRefuseMessages, reply: sse(
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{"role":"assistant","content":"pelican"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
		`data: [DONE]`)}
	up := setup(t, provider.Chat, f)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"deepseek-v4-1-flash-1017"},
		Preset: "mistral", Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	code, body := postTo(t, New(), "/v1/responses", `{"model":"fake/deepseek-v4-1-flash-1017","stream":false,"max_output_tokens":40,
		"tools":[{"type":"function","name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}],
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Read note.txt and tell me the word."}]},
			{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"note.txt\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"pelican"}]}`)
	if code != 200 || f.calls != 1 {
		t.Fatalf("%d after %d calls: %s", code, f.calls, body)
	}
	calls := 0
	for _, m := range sentMessages(t, f.got) {
		var msg map[string]json.RawMessage
		if json.Unmarshal(m, &msg) != nil || msg["tool_calls"] == nil {
			continue
		}
		calls++
		for _, k := range messageReasoning {
			if _, ok := msg[k]; ok {
				t.Fatalf("the call carries %s: %s", k, m)
			}
		}
		if strings.Contains(string(msg["content"]), "thinking") {
			t.Fatalf("the call was given a blank thinking part: %s", m)
		}
	}
	if calls != 1 {
		t.Fatalf("%d calls sent: %s", calls, f.got)
	}
}
