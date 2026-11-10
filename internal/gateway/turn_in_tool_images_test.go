package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// An image a client sends beside a tool result is the result's, as the
// bridge tells it (match): pi-ai's "Attached image(s) from tool result:"
// over Chat, a separate user message after the tool's. It begins no turn
// of the user's (#5767): counted one, the router re-decided the
// conversation's account on every image result, the bridge let the run go
// under another owner ("tool results account changed"), and a new Claude
// Code was told the whole conversation, the prompt cache rewritten each
// time — while the same results as text kept the account and the run.
func TestTurnInCarriesAnImageBesideItsToolResult(t *testing.T) {
	ask := userMsg("read the screenshot")
	call := Message{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: "t1", Name: "read"}}}
	img := Message{Role: "user", Parts: []Part{
		{Kind: Text, Text: "Attached image(s) from tool result:"},
		{Kind: Image, URL: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUg"},
	}}
	for _, tc := range []struct {
		name   string
		ms     []Message
		turn   int
		within bool
	}{
		// pi's shape: the tool's text, then the image beside it
		{"image beside its result", []Message{ask, call, Message{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "t1", Text: "(see attached image)"}}}, img}, 1, true},
		// a user's own mid-turn image rides with the results, as the bridge
		// tells it (match), and the turn's account holds for the tool loop
		{"the user's image beside a result", []Message{ask, call, Message{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "t1", Text: "ok"}}}, img}, 1, true},
		// a plain result, as before
		{"a text result", []Message{ask, call, Message{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "t1", Text: "ok"}}}}, 1, true},
		// the user's own words beside the results, with no image, are a
		// turn of theirs, as the bridge tells them (saidWithResults)
		{"the user's words beside a result", []Message{ask, call, Message{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "t1", Text: "ok"}}}, userMsg("stop, read the other one")}, 2, false},
	} {
		if turn, within := turnIn(&Request{Messages: tc.ms}); turn != tc.turn || within != tc.within {
			t.Errorf("%s: turn %d within %v, want %d %v", tc.name, turn, within, tc.turn, tc.within)
		}
	}
	// as pi sends it over Chat: the tool's message, then the image's
	body := `{"model":"m","messages":[` +
		`{"role":"user","content":"read the screenshot"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"t1","type":"function","function":{"name":"read","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"t1","content":"(see attached image)"},` +
		`{"role":"user","content":[{"type":"text","text":"Attached image(s) from tool result:"},{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUg"}}]}]}`
	if turn, within := turnOf(provider.Chat, []byte(body)); turn != 1 || !within {
		t.Errorf("pi's image result: turn %d within %v", turn, within)
	}
}

// An image of the user's own still begins a turn, when no result of the
// turn's is waiting: a screenshot pasted with the next ask is a turn.
func TestTurnInCountsTheUsersOwnImage(t *testing.T) {
	ask := Message{Role: "user", Parts: []Part{{Kind: Image, URL: "data:image/png;base64,iVBORw0KGgo"}, {Kind: Text, Text: "what is wrong here"}}}
	answer := Message{Role: "assistant", Parts: []Part{{Kind: Text, Text: "the tests are red"}}}
	next := Message{Role: "user", Parts: []Part{{Kind: Image, URL: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUg"}, {Kind: Text, Text: "and now here"}}}
	if turn, within := turnIn(&Request{Messages: []Message{ask, answer, next}}); turn != 2 || within {
		t.Errorf("the user's next image: turn %d within %v, want 2 false", turn, within)
	}
	if turn, within := turnIn(&Request{Messages: []Message{ask}}); turn != 1 || within {
		t.Errorf("the first image: turn %d within %v, want 1 false", turn, within)
	}
}

// When a run is let go for a changed owner, the log says whose process it
// was and whose account the request goes to (#5767): "account changed"
// alone left the reporter guessing at image hashes.
func TestToolResultsNameTheChangedOwner(t *testing.T) {
	run := &subscriptionRun{}
	o := "claude\x00a@example.com"
	run.owner.Store(&o)
	said := ownerSaid(run.owned())
	if !strings.Contains(said, "a@example.com") || !strings.Contains(said, "claude") {
		t.Fatalf("ownerSaid(%q) = %q", run.owned(), said)
	}
	o = "claude\x00Claude Max\x00" + ownHome + "\x00" + unnamedOwner
	run.owner.Store(&o)
	if said = ownerSaid(run.owned()); !strings.Contains(said, "Claude Max") || !strings.Contains(said, "own sign-in") || !strings.Contains(said, "unnamed") {
		t.Fatalf("ownerSaid(%q) = %q", run.owned(), said)
	}
	if said = ownerSaid("claude\x00"); said != "claude" {
		t.Fatalf("ownerSaid of a userless owner: %q", said)
	}
}
