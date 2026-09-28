package gateway

import (
	"bufio"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// chatToResponses runs Chat Completions stream chunks through the decoder and
// the Responses encoder and returns every event written.
func chatToResponses(t *testing.T, chunks ...string) []map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	enc := &responsesEncoder{w: newSSEWriter(rec), model: "m"}
	var d chatDecoder
	for _, c := range chunks {
		if err := d.decode(c, enc.event); err != nil {
			t.Fatal(err)
		}
	}
	enc.finish()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(rec.Body.String()))
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			var m map[string]any
			if json.Unmarshal([]byte(data), &m) == nil {
				out = append(out, m)
			}
		}
	}
	return out
}

// The translated reply has just one choice. Interleaved choices must not
// mix text, thinking, tool calls, or a finish reason into the first choice.
func TestChatTranslationKeepsFirstChoice(t *testing.T) {
	var d chatDecoder
	var got []Event
	for _, chunk := range []string{
		`{"id":"x","choices":[{"index":0,"delta":{"reasoning_content":"First thought","content":"Alpha","tool_calls":[{"index":0,"id":"first","function":{"name":"right","arguments":"{\"ok\":true}"}}]}},{"index":1,"delta":{"reasoning_content":"Other thought","content":"Beta","tool_calls":[{"index":0,"id":"other","function":{"name":"wrong","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"id":"x","choices":[{"index":1,"delta":{"content":" again","tool_calls":[{"index":0,"function":{"arguments":"not mine"}}]},"finish_reason":"stop"},{"index":0,"delta":{"content":" done","tool_calls":[{"index":0,"function":{"arguments":" more"}}]},"finish_reason":"tool_calls"}]}`,
		// An omitted index continues the choice already selected.
		`{"id":"x","choices":[{"delta":{"content":"!"}}]}`,
	} {
		if err := d.decode(chunk, func(ev Event) { got = append(got, ev) }); err != nil {
			t.Fatal(err)
		}
	}
	var text, think, toolID, toolName, args, stop string
	for _, ev := range got {
		switch ev.Kind {
		case KText:
			text += ev.Text
		case KThink:
			think += ev.Text
		case KToolStart:
			toolID, toolName = ev.ID, ev.Name
		case KToolArgs:
			args += ev.Text
		case KStop:
			stop = ev.Stop
		}
	}
	if text != "Alpha done!" || think != "First thought" || toolID != "first" || toolName != "right" || args != `{"ok":true} more` || stop != "tool" {
		t.Fatalf("mixed choices: text=%q think=%q tool=%q/%q args=%q stop=%q", text, think, toolID, toolName, args, stop)
	}
}

// A relay can number its only choice 1. Translation must keep that reply,
// including on later chunks, instead of returning a successful empty answer.
func TestChatTranslationKeepsOnlyChoiceIndexedOne(t *testing.T) {
	out := chatToResponses(t,
		`{"id":"x","choices":[{"index":1,"delta":{"content":"Hel"}}]}`,
		`{"id":"x","choices":[{"index":0,"delta":{"content":"not mine"}},{"index":1,"delta":{"content":"lo"},"finish_reason":"stop"}]}`,
	)
	var text string
	for _, ev := range out {
		if ev["type"] == "response.output_text.delta" {
			text += ev["delta"].(string)
		}
	}
	if text != "Hello" {
		t.Fatalf("translated text = %q; want Hello", text)
	}
}

// Some relays spell the choice index as a string. Decoding the rest of the
// chunk must not fail or silently drop content on these responses.
func TestChatTranslationAcceptsStringChoiceIndex(t *testing.T) {
	out := chatToResponses(t,
		`{"id":"x","choices":[{"index":"0","delta":{"content":"Hel"}}]}`,
		`{"id":"x","choices":[{"index":"1","delta":{"content":"not mine"}},{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}`,
	)
	var text string
	for _, ev := range out {
		if ev["type"] == "response.output_text.delta" {
			text += ev["delta"].(string)
		}
	}
	if text != "Hello" {
		t.Fatalf("translated text = %q; want Hello", text)
	}
}

// A usage-only chunk cannot choose which answer is kept. An unindexed
// one-choice relay continues to work when index is omitted or null.
func TestChatTranslationUsageBeforeUnindexedChoice(t *testing.T) {
	out := chatToResponses(t,
		`{"id":"x","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":2}}`,
		`{"id":"x","choices":[{"index":null,"delta":{"content":"Hel"}}]}`,
		`{"id":"x","choices":[{"delta":{"content":"lo"},"finish_reason":"stop"}]}`,
	)
	var text string
	for _, ev := range out {
		if ev["type"] == "response.output_text.delta" {
			text += ev["delta"].(string)
		}
	}
	if text != "Hello" {
		t.Fatalf("translated text = %q; want Hello", text)
	}
}

// A role-only chunk with no index must not select index 0 before the relay
// sends the actual answer under its sole indexed choice.
func TestChatTranslationUnindexedRoleBeforeIndexedChoice(t *testing.T) {
	out := chatToResponses(t,
		`{"id":"x","choices":[{"delta":{"role":"assistant"}}]}`,
		`{"id":"x","choices":[{"index":1,"delta":{"content":"Hel"}}]}`,
		`{"id":"x","choices":[{"index":1,"delta":{"content":"lo"},"finish_reason":"stop"}]}`,
	)
	var text string
	for _, ev := range out {
		if ev["type"] == "response.output_text.delta" {
			text += ev["delta"].(string)
		}
	}
	if text != "Hello" {
		t.Fatalf("translated text = %q; want Hello", text)
	}
}

// Missing and null indexes after an indexed first chunk continue that
// choice; malformed indexes should not turn an ordinary reply into a 200/empty.
func TestChatTranslationUnindexedChunksContinueIndexedChoice(t *testing.T) {
	out := chatToResponses(t,
		`{"id":"x","choices":[{"index":1,"delta":{"content":"H"}}]}`,
		`{"id":"x","choices":[{"delta":{"content":"e"}}]}`,
		`{"id":"x","choices":[{"index":null,"delta":{"content":"l"}}]}`,
		`{"id":"x","choices":[{"index":{},"delta":{"content":"l"}}]}`,
		`{"id":"x","choices":[{"index":1,"delta":{"content":"o"},"finish_reason":"stop"}]}`,
	)
	var text string
	for _, ev := range out {
		if ev["type"] == "response.output_text.delta" {
			text += ev["delta"].(string)
		}
	}
	if text != "Hello" {
		t.Fatalf("translated text = %q; want Hello", text)
	}
}

// JSON numeric 1.0 and 1 name the same upstream choice. The normalized
// index must not vary across chunks of that one answer.
func TestChatTranslationEquivalentNumericIndexes(t *testing.T) {
	out := chatToResponses(t,
		`{"id":"x","choices":[{"index":1.0,"delta":{"content":"Hel"}}]}`,
		`{"id":"x","choices":[{"index":1,"delta":{"content":"lo"},"finish_reason":"stop"}]}`,
	)
	var text string
	for _, ev := range out {
		if ev["type"] == "response.output_text.delta" {
			text += ev["delta"].(string)
		}
	}
	if text != "Hello" {
		t.Fatalf("translated text = %q; want Hello", text)
	}
}

// #18: with parallel tool calls each function_call item kept the next call's
// id, and the last two shared one — upstreams then refused the history.
func TestParallelToolCallIDs(t *testing.T) {
	var chunks []string
	for i, id := range []string{"call_00", "call_01", "call_02"} {
		chunks = append(chunks, `{"id":"x","choices":[{"delta":{"tool_calls":[{"index":`+string(rune('0'+i))+`,"id":"`+id+`","type":"function","function":{"name":"run","arguments":""}}]}}]}`,
			`{"id":"x","choices":[{"delta":{"tool_calls":[{"index":`+string(rune('0'+i))+`,"function":{"arguments":"{\"cmd\":\"`+id+`\"}"}}]}}]}`)
	}
	chunks = append(chunks, `{"id":"x","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
	var got []string
	for _, ev := range chatToResponses(t, chunks...) {
		if ev["type"] != "response.output_item.done" {
			continue
		}
		item := ev["item"].(map[string]any)
		if item["type"] != "function_call" {
			continue
		}
		var args struct{ Cmd string }
		json.Unmarshal([]byte(item["arguments"].(string)), &args)
		if item["call_id"] != args.Cmd {
			t.Errorf("call %s went out with id %v", args.Cmd, item["call_id"])
		}
		got = append(got, item["call_id"].(string))
	}
	if strings.Join(got, ",") != "call_00,call_01,call_02" {
		t.Fatalf("call ids = %v", got)
	}
}

// Some relays put the same thought under reasoning_content and reasoning;
// it must come out once, not "TheThe user user".
func TestReasoningSentUnderBothNames(t *testing.T) {
	var think strings.Builder
	for _, ev := range chatToResponses(t,
		`{"id":"x","choices":[{"delta":{"reasoning_content":"The ","reasoning":"The "}}]}`,
		`{"id":"x","choices":[{"delta":{"reasoning_content":"user","reasoning":"user"}}]}`,
		`{"id":"x","choices":[{"delta":{"reasoning":" wants"}}]}`,
		`{"id":"x","choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`) {
		if ev["type"] == "response.reasoning_summary_text.delta" {
			think.WriteString(ev["delta"].(string))
		}
	}
	if think.String() != "The user wants" {
		t.Fatalf("thinking = %q", think.String())
	}
}

// The ChatGPT backend's completed response lists no output, so a streamed
// function call alone says the turn stopped for a tool.
func TestStreamedCallStopsForTool(t *testing.T) {
	var d responsesDecoder
	var stop string
	for _, c := range []string{
		`{"type":"response.created","response":{"id":"r1"}}`,
		`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"c1","name":"get_weather"}}`,
		`{"type":"response.function_call_arguments.delta","delta":"{\"city\":\"Paris\"}"}`,
		`{"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`,
	} {
		d.decode(c, func(e Event) {
			if e.Kind == KStop {
				stop = e.Stop
			}
		})
	}
	if stop != "tool" {
		t.Fatalf("stop %q", stop)
	}
}
