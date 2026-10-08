package gateway

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// splitCalls is #1275's request as an agent sent it: one turn's parallel
// tool_use blocks split over two consecutive assistant messages, answered
// by one user message carrying both results.
const splitCalls = `{"model":"m","max_tokens":1024,"messages":[
 {"role":"user","content":"read a and b"},
 {"role":"assistant","content":[{"type":"tool_use","id":"toolu_a","name":"read","input":{"path":"a"}}]},
 {"role":"assistant","content":[{"type":"tool_use","id":"toolu_b","name":"read","input":{"path":"b"}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_a","content":"A!"},
                           {"type":"tool_result","tool_use_id":"toolu_b","content":"B!"}]}]}`

func parseSplit(t *testing.T, body string) *Request {
	t.Helper()
	r, err := parseAnthropic([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// The two calls are one exchange on Chat: both answered by their real
// results, none marked interrupted, nothing left as a stray user message.
func TestChatSplitParallelCallsKeepTheirResults(t *testing.T) {
	r := parseSplit(t, splitCalls)
	trace, msgs := chatTrace(t, r)
	if want := "user assistant(toolu_a,toolu_b) tool(toolu_a) tool(toolu_b)"; trace != want {
		t.Fatalf("trace = %q, want %q", trace, want)
	}
	if a, b := msgs[2]["content"], msgs[3]["content"]; a != "A!" || b != "B!" {
		t.Fatalf("results = %q, %q, want the real ones", a, b)
	}
	if !kimiValid(msgs) || !idsUsedOnce(msgs) {
		t.Fatal("sequence fails Kimi's tool exchange validation")
	}
	if len(r.Messages) != 4 {
		t.Fatalf("the request's own messages changed: %d", len(r.Messages))
	}
}

// A call of the split turn that really went unanswered still gets the
// synthetic result, after the real ones as in a turn sent whole, and the
// answered one keeps its own.
func TestChatSplitCallsUnansweredStillSynthetic(t *testing.T) {
	r := parseSplit(t, strings.Replace(splitCalls,
		`{"type":"tool_result","tool_use_id":"toolu_a","content":"A!"},`, "", 1))
	trace, msgs := chatTrace(t, r)
	if want := "user assistant(toolu_a,toolu_b) tool(toolu_b) tool(toolu_a)"; trace != want {
		t.Fatalf("trace = %q, want %q", trace, want)
	}
	if a, _ := msgs[3]["content"].(string); !strings.Contains(a, "unavailable") {
		t.Fatalf("unanswered toolu_a = %q, want the synthetic result", a)
	}
	if b := msgs[2]["content"]; b != "B!" {
		t.Fatalf("toolu_b = %q, want B!", b)
	}
}

// Text the turn said after its call, in a message of its own, joins the
// turn too, so the call is still answered by its result.
func TestChatSplitTextAfterCallKeepsResult(t *testing.T) {
	r := &Request{Messages: []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "read a"}}},
		{Role: "assistant", Parts: []Part{call("A")}},
		{Role: "assistant", Parts: []Part{{Kind: Text, Text: "reading"}}},
		{Role: "user", Parts: []Part{result("A", "1")}},
	}}
	trace, msgs := chatTrace(t, r)
	if want := "user assistant(A) tool(A)"; trace != want {
		t.Fatalf("trace = %q, want %q", trace, want)
	}
	if msgs[1]["content"] != "reading" || msgs[2]["content"] != "1" {
		t.Fatalf("assistant %q, answer %q", msgs[1]["content"], msgs[2]["content"])
	}
}

// Two assistant messages with no calls are left as they were.
func TestChatConsecutiveTextAssistantsUnchanged(t *testing.T) {
	r := &Request{Messages: []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}},
		{Role: "assistant", Parts: []Part{{Kind: Text, Text: "one"}}},
		{Role: "assistant", Parts: []Part{{Kind: Text, Text: "two"}}},
	}}
	if trace, _ := chatTrace(t, r); trace != "user assistant assistant" {
		t.Fatalf("trace = %q", trace)
	}
}

// Responses carries calls as items of their own, so the split turn's
// results follow their calls there already.
func TestResponsesSplitParallelCallsKeepTheirResults(t *testing.T) {
	var q struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(buildResponses(parseSplit(t, splitCalls), "gpt-5.4", "api.openai.com", false), &q); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range q.Input {
		s, _ := it["type"].(string)
		if id, ok := it["call_id"].(string); ok {
			s += "(" + id
			if out, ok := it["output"].(string); ok {
				s += "=" + out
			}
			s += ")"
		}
		got = append(got, s)
	}
	want := "message function_call(toolu_a) function_call(toolu_b) function_call_output(toolu_a=A!) function_call_output(toolu_b=B!)"
	if strings.Join(got, " ") != want {
		t.Fatalf("input = %s\nwant    %s", strings.Join(got, " "), want)
	}
}

// Cursor's built-in answers a reply's calls at the next assistant message,
// as Chat did.
func TestCursorSplitParallelCallsKeepTheirResults(t *testing.T) {
	r := parseSplit(t, splitCalls)
	b := bytes.Join(cursorMessages(r, bridgeTools(r)), []byte("\n"))
	if bytes.Contains(b, []byte(devinNoResult)) {
		t.Fatalf("a call answered as interrupted:\n%s", b)
	}
	if !bytes.Contains(b, []byte(`"result":"A!"`)) || !bytes.Contains(b, []byte(`"result":"B!"`)) {
		t.Fatalf("results lost:\n%s", b)
	}
}

// So did Devin's.
func TestDevinSplitParallelCallsKeepTheirResults(t *testing.T) {
	d := decodeDevinRequest(t, buildDevin(parseSplit(t, splitCalls), "swe-2-high", "k"))
	var got []string
	for _, m := range d.msgs {
		got = append(got, devinSummary(m))
	}
	want := []string{
		"1:read a and b",
		`2: call toolu_a/read {"path":"a"} call toolu_b/read {"path":"b"}`,
		"4:A! for toolu_a",
		"4:B! for toolu_b",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
