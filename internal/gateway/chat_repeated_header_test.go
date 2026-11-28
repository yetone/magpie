package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

func repeatedHeaderQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// A stream that opens a new index for every frame naming the tool, under
// the id the call already has, sends the open call's id and name again at
// a second index partway through its arguments, and the rest of them
// there. That is the same call: its arguments are given as they come, not
// held as a second call's until the reply ends, while the client's input
// stopped at the header.
func TestChatRepeatedToolHeaderContinuesTheCall(t *testing.T) {
	var d chatDecoder
	var got []Event
	for i, c := range []string{
		`{"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"exec","arguments":""}}]}}]}`,
		`{"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"input\":\"a(\\\""}}]}}]}`,
		`{"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_1","type":"function","function":{"name":"exec","arguments":""}}]}}]}`,
		`{"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"b\\\")\"}"}}]}}]}`,
		`{"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	} {
		n := len(got)
		if err := d.decode(c, func(ev Event) { got = append(got, ev) }); err != nil {
			t.Fatal(err)
		}
		if i == 3 && (len(got) != n+1 || got[n].Kind != KToolArgs || got[n].Text != `b\")"}`) {
			t.Fatalf("the arguments after the repeated header were not given as they came: %+v", got[n:])
		}
	}
	var starts []string
	var args, stop string
	for _, ev := range got {
		switch ev.Kind {
		case KToolStart:
			starts = append(starts, ev.ID+" "+ev.Name)
		case KToolArgs:
			args += ev.Text
		case KStop:
			stop = ev.Stop
		}
	}
	if strings.Join(starts, ",") != "call_1 exec" || args != `{"input":"a(\"b\")"}` || stop != "tool" {
		t.Fatalf("calls %q, arguments %q, stop %q", starts, args, stop)
	}
}

// What is not that stays a second call: the same id at a second index once
// the first call's arguments are whole, or the same id under another name,
// or another id, while they are not.
func TestChatRepeatedToolHeaderOnlyWhileTheCallIsOpen(t *testing.T) {
	cases := []struct {
		name, header, second, want string
	}{
		{"whole first", `{"n":1}`,
			`{"index":1,"id":"call_1","type":"function","function":{"name":"exec","arguments":"{\"n\":2}"}}`,
			`call_1 exec {"n":1} | call_1 exec {"n":2}`},
		{"another name", `{"n":`,
			`{"index":1,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"p\":2}"}}`,
			`call_1 exec {"n": | call_1 read {"p":2}`},
		{"another id", `{"n":`,
			`{"index":1,"id":"call_2","type":"function","function":{"name":"exec","arguments":"{\"p\":2}"}}`,
			`call_1 exec {"n": | call_2 exec {"p":2}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var d chatDecoder
			var col collector
			for _, chunk := range []string{
				`{"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"exec","arguments":` + repeatedHeaderQuote(c.header) + `}}]}}]}`,
				`{"id":"x","choices":[{"index":0,"delta":{"tool_calls":[` + c.second + `]}}]}`,
				`{"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			} {
				if err := d.decode(chunk, col.add); err != nil {
					t.Fatal(err)
				}
			}
			var got []string
			for _, p := range col.finish().Parts {
				if p.Kind == ToolCall {
					got = append(got, p.ID+" "+p.Name+" "+string(p.Args))
				}
			}
			if strings.Join(got, " | ") != c.want {
				t.Fatalf("calls = %q, want %q", got, c.want)
			}
		})
	}
}

// A Responses client sees one function_call item with its whole
// arguments, streamed under one item id, and the repeated header does not
// open a second item.
func TestChatRepeatedToolHeaderToResponses(t *testing.T) {
	out := chatToResponses(t,
		`{"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"exec","arguments":"{\"cmd\":\"ls"}}]}}]}`,
		`{"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_1","type":"function","function":{"name":"exec","arguments":" -la"}}]}}]}`,
		`{"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"\"}"}}]}}]}`,
		`{"id":"x","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	var added, done []string
	var streamed string
	for _, ev := range out {
		switch ev["type"] {
		case "response.output_item.added":
			if it, _ := ev["item"].(map[string]any); it["type"] == "function_call" {
				added = append(added, it["call_id"].(string))
			}
		case "response.function_call_arguments.delta":
			streamed += ev["delta"].(string)
		case "response.output_item.done":
			if it, _ := ev["item"].(map[string]any); it["type"] == "function_call" {
				done = append(done, it["call_id"].(string)+"="+it["arguments"].(string))
			}
		}
	}
	if strings.Join(added, ",") != "call_1" || strings.Join(done, " ") != `call_1={"cmd":"ls -la"}` || streamed != `{"cmd":"ls -la"}` {
		t.Fatalf("added %q, done %q, streamed %q", added, done, streamed)
	}
}
