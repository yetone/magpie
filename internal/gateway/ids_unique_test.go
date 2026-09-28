package gateway

import (
	"testing"
	"time"
)

// stillClock holds newID's clock on one instant for the test, as Windows'
// clock does for up to 15.6ms.
func stillClock(t *testing.T) {
	at := time.Unix(1790000000, 0)
	idClock = func() time.Time { return at }
	t.Cleanup(func() { idClock = time.Now })
}

// Gemini (Code Assist) names no id for its function calls, so the gateway
// makes them up. Two parallel calls in one chunk must still get two ids, or
// the client cannot tell their results apart.
func TestCodeAssistParallelCallsGetDistinctIDs(t *testing.T) {
	stillClock(t)
	chunk := `{"response":{"candidates":[{"content":{"role":"model","parts":[` +
		`{"functionCall":{"name":"read","args":{"path":"a"}}},` +
		`{"functionCall":{"name":"read","args":{"path":"b"}}},` +
		`{"functionCall":{"name":"read","args":{"path":"c"}}}]},"finishReason":"STOP"}]}}`
	var col collector
	d := &codeAssistDecoder{}
	if err := d.decode(chunk, col.add); err != nil {
		t.Fatal(err)
	}
	res := col.finish()
	seen := map[string]bool{}
	for _, p := range res.Parts {
		if p.Kind != ToolCall {
			continue
		}
		if seen[p.ID] {
			t.Fatalf("two tool calls share the id %q: %+v", p.ID, res.Parts)
		}
		seen[p.ID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("got %d distinct calls, want 3", len(seen))
	}
}

func TestNewIDUnique(t *testing.T) {
	stillClock(t)
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := newID()
		if seen[id] {
			t.Fatalf("newID repeated %q after %d calls", id, i)
		}
		seen[id] = true
	}
}
