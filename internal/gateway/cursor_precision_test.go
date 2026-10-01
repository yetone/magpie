package gateway

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCursorLargeIntegerArgs(t *testing.T) {
	r := &Request{Messages: []Message{{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: "c1", Name: "lookup", Args: json.RawMessage(`{"id":9007199254740993,"nested":{"n":9223372036854775807}}`)}}}}}
	out := cursorMessages(r, nil)
	b := bytes.Join(out, []byte("\n"))
	if !bytes.Contains(b, []byte(`9007199254740993`)) || !bytes.Contains(b, []byte(`9223372036854775807`)) {
		t.Fatalf("integer arguments changed: %s", b)
	}
}
func TestCursorResultLargeInteger(t *testing.T) {
	b, _ := json.Marshal(cursorResult("c1", `{"id":9007199254740993,"n":9223372036854775807}`, false))
	if !bytes.Contains(b, []byte(`"id":9007199254740993`)) || !bytes.Contains(b, []byte(`"n":9223372036854775807`)) {
		t.Fatalf("result changed: %s", b)
	}
}

// A call whose streamed arguments were cut short stays in the history, with
// empty arguments, rather than making the whole message fail to marshal.
func TestCursorTruncatedArgsKeepCall(t *testing.T) {
	r := &Request{Messages: []Message{{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: "c1", Name: "lookup", Args: json.RawMessage(`{"query":"x`)}}}}}
	b := bytes.Join(cursorMessages(r, nil), []byte("\n"))
	if !bytes.Contains(b, []byte(`"toolName":"lookup"`)) {
		t.Fatalf("call lost: %s", b)
	}
}
