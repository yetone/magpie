package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// What the classifier must never be told of the turn before: the system
// prompt, thinking, a tool call's arguments and a tool's result.
var priorSecrets = []string{"SYSPROMPT_sk-live-9f8e7d", "THINK_sk-live-6c5b4a", "ARGS_sk-live-3a2b1c", "RESULT_sk-live-0z9y8x"}

// One conversation in each API: the user asked, the assistant thought and
// called a tool, read its result, then thought again, answered and called
// one more tool in the same message, and the user followed up with 继续
// after that tool's result.
var priorTurns = map[provider.Protocol]string{
	provider.Anthropic: `{"model":"m","system":"You are X. SYSPROMPT_sk-live-9f8e7d","messages":[
		{"role":"user","content":"add a cache to the parser"},
		{"role":"assistant","content":[{"type":"thinking","thinking":"THINK_sk-live-6c5b4a","signature":"s"},{"type":"tool_use","id":"t1","name":"bash","input":{"cmd":"echo ARGS_sk-live-3a2b1c"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"RESULT_sk-live-0z9y8x"}]},
		{"role":"assistant","content":[{"type":"thinking","thinking":"THINK_sk-live-6c5b4a again","signature":"s"},{"type":"text","text":"Cache added in parser.go. Want tests?"},{"type":"tool_use","id":"t2","name":"bash","input":{"cmd":"echo ARGS_sk-live-3a2b1c again"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"RESULT_sk-live-0z9y8x again"}]},
		{"role":"user","content":[{"type":"text","text":"<system-reminder>\nnudge\n</system-reminder>"},{"type":"text","text":"继续"}]}]}`,
	provider.Chat: `{"model":"m","messages":[
		{"role":"system","content":"You are X. SYSPROMPT_sk-live-9f8e7d"},
		{"role":"user","content":"add a cache to the parser"},
		{"role":"assistant","content":null,"reasoning_content":"THINK_sk-live-6c5b4a","tool_calls":[{"id":"t1","type":"function","function":{"name":"bash","arguments":"{\"cmd\":\"echo ARGS_sk-live-3a2b1c\"}"}}]},
		{"role":"tool","tool_call_id":"t1","content":"RESULT_sk-live-0z9y8x"},
		{"role":"assistant","content":"Cache added in parser.go. Want tests?","reasoning_content":"THINK_sk-live-6c5b4a again","tool_calls":[{"id":"t2","type":"function","function":{"name":"bash","arguments":"{\"cmd\":\"echo ARGS_sk-live-3a2b1c again\"}"}}]},
		{"role":"tool","tool_call_id":"t2","content":"RESULT_sk-live-0z9y8x again"},
		{"role":"user","content":"继续"}]}`,
	provider.Responses: `{"model":"m","instructions":"You are X. SYSPROMPT_sk-live-9f8e7d","input":[
		{"type":"message","role":"developer","content":[{"type":"input_text","text":"SYSPROMPT_sk-live-9f8e7d"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"add a cache to the parser"}]},
		{"type":"reasoning","id":"r1","summary":[{"type":"summary_text","text":"THINK_sk-live-6c5b4a"}]},
		{"type":"function_call","call_id":"t1","name":"bash","arguments":"{\"cmd\":\"echo ARGS_sk-live-3a2b1c\"}"},
		{"type":"function_call_output","call_id":"t1","output":"RESULT_sk-live-0z9y8x"},
		{"type":"reasoning","id":"r2","summary":[{"type":"summary_text","text":"THINK_sk-live-6c5b4a again"}]},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Cache added in parser.go. Want tests?"}]},
		{"type":"function_call","call_id":"t2","name":"bash","arguments":"{\"cmd\":\"echo ARGS_sk-live-3a2b1c again\"}"},
		{"type":"function_call_output","call_id":"t2","output":"RESULT_sk-live-0z9y8x again"},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"继续"}]}]}`,
	provider.Gemini: `{"systemInstruction":{"parts":[{"text":"You are X. SYSPROMPT_sk-live-9f8e7d"}]},"contents":[
		{"role":"user","parts":[{"text":"add a cache to the parser"}]},
		{"role":"model","parts":[{"text":"THINK_sk-live-6c5b4a","thought":true},{"functionCall":{"name":"bash","args":{"cmd":"echo ARGS_sk-live-3a2b1c"}}}]},
		{"role":"user","parts":[{"functionResponse":{"name":"bash","response":{"output":"RESULT_sk-live-0z9y8x"}}}]},
		{"role":"model","parts":[{"text":"THINK_sk-live-6c5b4a again","thought":true},{"text":"Cache added in parser.go. Want tests?"},{"functionCall":{"name":"bash","args":{"cmd":"echo ARGS_sk-live-3a2b1c again"}}}]},
		{"role":"user","parts":[{"functionResponse":{"name":"bash","response":{"output":"RESULT_sk-live-0z9y8x again"}}},{"text":"继续"}]}]}`,
}

// Each API's conversation gives the classifier the user's message before
// the follow-up and the assistant's text reply to it, and none of the
// system prompt, thinking, tool arguments or tool results, in Jev's call
// or a chat classifier's.
func TestTurnBeforeEachProtocol(t *testing.T) {
	for proto, body := range priorTurns {
		req, err := parse(proto, []byte(body))
		if err != nil {
			t.Fatalf("%s: %v", proto, err)
		}
		user, reply := turnBefore(req)
		if user != "add a cache to the parser" || reply != "Cache added in parser.go. Want tests?" || userText(req) != "继续" {
			t.Errorf("%s: user %q reply %q message %q", proto, user, reply, userText(req))
		}
		prev := before{Intent: "complex task", Effort: "high", User: user, Reply: reply}
		for name, b := range map[string][]byte{
			"jev":    jevBody("jev-latest", []string{"simple task", "complex task"}, nil, prev, true, userText(req)),
			"intent": classifyBody("m", "", []string{"simple task", "complex task"}, prev, userText(req)),
			"effort": effortBody("m", "", prev, userText(req)),
		} {
			for _, s := range priorSecrets {
				if strings.Contains(string(b), s) {
					t.Errorf("%s %s: %s sent: %s", proto, name, s, b)
				}
			}
		}
	}
}

// What came before is cut as the message is, smaller: the user's message
// keeps its start and end, the reply its end; a reply that only called
// tools is none, and a reply to an older message isn't taken for one.
func TestTurnBeforeCuts(t *testing.T) {
	long := strings.Repeat("a", 750) + strings.Repeat("m", 4000) + strings.Repeat("z", 250)
	reply := strings.Repeat("r", 3000) + "END"
	user, got := turnBefore(&Request{Messages: []Message{
		userMsg(long),
		{Role: "assistant", Parts: []Part{{Kind: Text, Text: reply}}},
		userMsg("go on"),
	}})
	if user != strings.Repeat("a", 750)+"\n…\n"+strings.Repeat("z", 250) {
		t.Errorf("user cut to %d runes: %.40q…", len([]rune(user)), user)
	}
	if want := "…" + reply[len(reply)-priorCap:]; got != want || !strings.HasSuffix(got, "END") {
		t.Errorf("reply cut to %d runes", len([]rune(got)))
	}
	if n := len([]rune(user)) + len([]rune(got)); n > 2*priorCap+4 {
		t.Errorf("%d runes of context", n)
	}
	// a first message has nothing before it
	if u, r := turnBefore(&Request{Messages: []Message{userMsg("hi")}}); u != "" || r != "" {
		t.Errorf("first: %q %q", u, r)
	}
	// only tool calls answered the message before: no reply; the text
	// answering the one before that is not this one's
	u, r := turnBefore(&Request{Messages: []Message{
		userMsg("old ask"),
		{Role: "assistant", Parts: []Part{{Kind: Text, Text: "old answer"}}},
		userMsg("run the tests"),
		{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: "c", Name: "bash", Args: json.RawMessage(`{"cmd":"ARGS_sk-live-3a2b1c"}`)}}},
		{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "c", Text: "RESULT_sk-live-0z9y8x"}}},
		userMsg("继续"),
	}})
	if u != "run the tests" || r != "" {
		t.Errorf("tool-only reply: %q %q", u, r)
	}
}

// Jev is told what came before the message, and asked to judge a short
// follow-up with it, naming only the keys it has.
func TestJevIsToldWhatCameBefore(t *testing.T) {
	type body struct {
		State     map[string]string `json:"state"`
		Questions map[string]struct {
			Instructions string `json:"instructions"`
		} `json:"questions"`
	}
	read := func(prev before) body {
		var got body
		if err := json.Unmarshal(jevBody("jev-latest", []string{"simple task", "complex task"}, nil, prev, true, "继续"), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	got := read(before{User: "add a cache to the parser", Reply: "Cache added. Want tests?"})
	if got.State["message"] != "继续" || got.State["previous_user_message"] != "add a cache to the parser" || got.State["previous_assistant_reply"] != "Cache added. Want tests?" {
		t.Errorf("state = %v", got.State)
	}
	for _, q := range []string{"intent", "level", "effort"} {
		in := got.Questions[q].Instructions
		if !strings.Contains(in, "`previous_user_message`") || !strings.Contains(in, "`previous_assistant_reply`") {
			t.Errorf("%s: %q", q, in)
		}
	}
	got = read(before{User: "add a cache to the parser"})
	if _, ok := got.State["previous_assistant_reply"]; ok || strings.Contains(got.Questions["intent"].Instructions, "previous_assistant_reply") ||
		!strings.Contains(got.Questions["intent"].Instructions, "previous_user_message") {
		t.Errorf("no reply: %v %q", got.State, got.Questions["intent"].Instructions)
	}
	got = read(before{})
	if len(got.State) != 1 || strings.Contains(got.Questions["effort"].Instructions, "previous_") {
		t.Errorf("nothing before: %v %q", got.State, got.Questions["effort"].Instructions)
	}
}

// A classifier asked in words is told the same, only when there is any.
func TestChatClassifierIsToldWhatCameBefore(t *testing.T) {
	prev := before{User: "add a cache to the parser", Reply: "Cache added. Want tests?"}
	for name, b := range map[string][]byte{
		"intent": classifyBody("m", "", []string{"x", "y"}, prev, "继续"),
		"effort": effortBody("m", "", prev, "继续"),
	} {
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		if err := json.Unmarshal(b, &req); err != nil || len(req.Messages) != 2 {
			t.Fatalf("%s: %v %s", name, err, b)
		}
		s := req.Messages[1].Content
		if !strings.Contains(s, "<previous_user_message>\nadd a cache to the parser\n</previous_user_message>") ||
			!strings.Contains(s, "<previous_assistant_reply>\nCache added. Want tests?\n</previous_assistant_reply>") ||
			!strings.Contains(s, "<message>\n继续\n</message>") {
			t.Errorf("%s: %s", name, s)
		}
	}
	for name, b := range map[string][]byte{
		"intent": classifyBody("m", "", []string{"x", "y"}, before{}, "继续"),
		"effort": effortBody("m", "", before{}, "继续"),
	} {
		if strings.Contains(string(b), "previous_") {
			t.Errorf("%s with nothing before: %s", name, b)
		}
	}
}

// An answer is kept for the message with what came before it: 继续 after
// one request is not 继续 after another.
func TestClassifyKeepsAnAnswerPerTurnBefore(t *testing.T) {
	resetClassified(t)
	n := 0
	ask := func(_ access.Identity, _ string, _ []string, prev before, _ bool, _ string) (verdict, error) {
		n++
		return verdict{Intent: prev.User}, nil
	}
	for _, c := range []struct {
		prev   before
		cached bool
	}{
		{before{User: "rename a variable"}, false},
		{before{User: "rename a variable"}, true},
		{before{User: "design the sync protocol"}, false},
		{before{User: "design the sync protocol", Reply: "Here is a plan."}, false},
	} {
		v, cached, err := classify(ask, access.Identity{}, t.Name(), []string{"rename a variable", "design the sync protocol"}, c.prev, false, "继续")
		if err != nil || cached != c.cached || v.Intent != c.prev.User {
			t.Fatalf("%+v: %+v cached %v %v", c.prev, v, cached, err)
		}
	}
	if n != 3 {
		t.Fatalf("asked %d times", n)
	}
}

// Through the gateway: Jev's state carries the turn before from the
// request the rules read, masked as the message is when Privacy masks.
func TestJevSeesTheTurnBefore(t *testing.T) {
	s, _, _, j := jevved(t, "", provider.Rule{Use: "b/big", Intent: "debugging"})
	if err := settings.Save(settings.Settings{Redact: true, RedactPersonal: true}); err != nil {
		t.Fatal(err)
	}
	postOK(t, s, "s1", chat("mail moon.shard@proton.me why the parser crashes", []string{"继续"}, 0, ""))
	turns := j.turns()
	st := turns[len(turns)-1]["state"].(map[string]any)
	if st["message"] != "继续" || st["previous_assistant_reply"] != "done" {
		t.Fatalf("state %v", st)
	}
	u, _ := st["previous_user_message"].(string)
	if !strings.Contains(u, "why the parser crashes") || strings.Contains(u, "moon.shard@proton.me") {
		t.Fatalf("previous_user_message %q", u)
	}
}

// A group whose every member the user set to go unmasked hands its rules
// the body as the agent wrote it; Jev, which isn't one of them, is still
// told the turn masked.
func TestJevIsToldTheTurnMaskedWhenTheGroupGoesUnmasked(t *testing.T) {
	s, _, _, j := jevved(t, "", provider.Rule{Use: "b/big", Intent: "debugging"})
	if err := settings.Save(settings.Settings{Redact: true, RedactPersonal: true}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a/small", "b/big"} {
		p, _, ok := provider.Resolve(id)
		if !ok {
			t.Fatalf("no %s", id)
		}
		p.Unredacted = true
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
		if p, _, _ := provider.Resolve(id); !p.SkipsRedaction() {
			t.Fatalf("%s doesn't skip redaction", id)
		}
	}
	postOK(t, s, "s1", chat("mail moon.shard@proton.me why the parser crashes", []string{"tell sun.ray@proton.me 继续"}, 0, ""))
	turns := j.turns()
	st := turns[len(turns)-1]["state"].(map[string]any)
	for k, v := range st {
		if s, _ := v.(string); strings.Contains(s, "@proton.me") {
			t.Fatalf("%s went to Jev unmasked: %q", k, s)
		}
	}
	if u, _ := st["previous_user_message"].(string); !strings.Contains(u, "why the parser crashes") {
		t.Fatalf("state %v", st)
	}
}
