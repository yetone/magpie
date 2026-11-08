package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"testing"
)

func dshMessageID(msg string) string {
	sum := sha256.Sum256([]byte(msg))
	return "magpie-" + hex.EncodeToString(sum[:12])
}

// dsh's requests carry no session id, and a fork copies its source's
// opening: the user messages of the request's turn are what tell a request
// still answering which branch it is of, newest first, each hashed as the
// branch's own session file has it (the message with its text alone). The
// turn's messages are the run its newest user message ends: the one typed
// and the runtime context dsh sends after it, not the turn before.
func TestDshLastConvsAreTheTurnsUserMessages(t *testing.T) {
	body := []byte(`{"model":"m","messages":[` +
		`{"role":"system","content":"s"},` +
		`{"role":"user","content":"old"},` +
		`{"role":"assistant","content":"a"},` +
		`{"role":"user","content":"typed"},` +
		`{"role":"user","content":[{"type":"text","text":"context","cache_control":{"type":"ephemeral"}}]},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"out"}]}`)
	want := []string{dshMessageID(`{"role":"user","content":"context"}`), dshMessageID(`{"role":"user","content":"typed"}`)}
	if got := dshLastConvs("dsh", body); !slices.Equal(got, want) {
		t.Fatalf("ids %v, want %v", got, want)
	}
	// Messages' later steps end on the answer to a tool call, a user
	// message of tool_result parts: the turn's messages are before it
	msgs := []byte(`{"model":"m","messages":[` +
		`{"role":"user","content":"typed"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"f","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"out"}]}]}`)
	if got, want := dshLastConvs("dsh", msgs), []string{dshMessageID(`{"role":"user","content":"typed"}`)}; !slices.Equal(got, want) {
		t.Fatalf("after a tool_result: %v, want %v", got, want)
	}
}

// Only dsh's requests are read that way, only text messages, and no more
// than a session keeps of its turn.
func TestDshLastConvsOnlyForDshTextMessages(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"new"}]}`)
	if got := dshLastConvs("pi", body); got != nil {
		t.Errorf("another agent: %v, want none", got)
	}
	if got := dshLastConvs("dsh", []byte(`{"messages":[{"role":"assistant","content":"a"}]}`)); got != nil {
		t.Errorf("no user message: %v, want none", got)
	}
	if got := dshLastConvs("dsh", []byte(`{"messages":[{"role":"user","content":[{"type":"image","url":"x"}]}]}`)); got != nil {
		t.Errorf("no text part: %v, want none", got)
	}
	many := []byte(`{"messages":[{"role":"user","content":"1"},{"role":"user","content":"2"},{"role":"user","content":"3"},{"role":"user","content":"4"},{"role":"user","content":"5"}]}`)
	if got := dshLastConvs("dsh", many); len(got) != 4 || got[0] != dshMessageID(`{"role":"user","content":"5"}`) {
		t.Errorf("five messages: %v, want the newest four", got)
	}
}

// A done route is named by its reply: the history keeps none of its turn's
// ids, which would only grow every dsh route of the 30 days kept.
func TestHistoryKeepsNoTurnIDs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := historyNoon(t)
	saveRoute(Route{ID: 1, Time: now, Agent: "dsh", Conv: "magpie-open", ResponseID: "chatcmpl-1", LastConvs: []string{"magpie-msg"}, Done: true})
	_, routes, _ := History(now.Format(dayForm))
	if len(routes) != 1 || routes[0].ResponseID != "chatcmpl-1" || routes[0].LastConvs != nil {
		t.Fatalf("history route %+v, want its reply and no turn ids", routes)
	}
}
