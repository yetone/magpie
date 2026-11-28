package gateway

import (
	"strings"
	"testing"
)

// Once a second call is interleaved, an id/name repeated at another index
// cannot safely be folded into the open call. It keeps its own arguments.
func TestChatRepeatedToolHeaderAfterInterleavingKeepsCalls(t *testing.T) {
	var d chatDecoder
	var col collector
	for _, chunk := range []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"exec","arguments":"{\"first\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","function":{"name":"read","arguments":"{\"other\":1}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":2,"id":"call_1","function":{"name":"exec","arguments":"{\"last\":2}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"0}"}}]},"finish_reason":"tool_calls"}]}`,
	} {
		if err := d.decode(chunk, col.add); err != nil {
			t.Fatal(err)
		}
	}
	var calls []string
	for _, p := range col.finish().Parts {
		if p.Kind == ToolCall {
			calls = append(calls, p.ID+" "+p.Name+" "+string(p.Args))
		}
	}
	if got, want := strings.Join(calls, " | "), `call_1 exec {"first":0} | call_2 read {"other":1} | call_1 exec {"last":2}`; got != want {
		t.Fatalf("interleaved calls = %q, want %q", got, want)
	}
}
