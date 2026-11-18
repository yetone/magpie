package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// copilotWSModels is Copilot's model list as the owner's account has it:
// gpt-5.5 served over the Responses WebSocket too, gpt-5.3-codex over
// HTTP only.
const copilotWSModels = `{"data":[
  {"id":"gpt-5.5","name":"GPT-5.5","model_picker_enabled":true,"policy":{"state":"enabled"},"supported_endpoints":["/responses","ws:/responses"],"capabilities":{"type":"chat"}},
  {"id":"gpt-5.3-codex","name":"GPT-5.3-Codex","model_picker_enabled":true,"policy":{"state":"enabled"},"supported_endpoints":["/responses"],"capabilities":{"type":"chat"}}]}`

// copilotSocketFake signs a Copilot account in to a fake Copilot API whose
// /responses is f, over HTTP and over its WebSocket.
func copilotSocketFake(t *testing.T, f *fakeSocketBackend) {
	t.Helper()
	socketOn(t)
	provider.ForgetCopilotForTest()
	t.Cleanup(provider.ForgetCopilotForTest)
	f.path = "/responses"
	f.whole = true
	copilotFake(t, "gho_copilot_ws", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			io.WriteString(w, copilotWSModels)
		case "/responses":
			f.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

func copilotTurn(model string, items ...string) string {
	return `{"model":"copilot/` + model + `","stream":true,"instructions":"be brief","input":[` + strings.Join(items, ",") + `]}`
}

func copilotPost(t *testing.T, s *Server, body string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "pong") ||
		!strings.Contains(rec.Body.String(), "event: response.completed\n") {
		t.Fatalf("%d, a reply without its end: %s", rec.Code, rec.Body.String())
	}
}

// yuushabu_buyinn on X: magpie sent each Copilot Responses turn whole over
// HTTP (129 GB up for 31 GB down), where Copilot answers
// previous_response_id 400 "previous_response_id is not supported". Over
// its Responses WebSocket, as Copilot Chat sends a model that lists
// "ws:/responses", a conversation's next turn goes on the same socket as
// its new input after previous_response_id.
func TestCopilotSocketSendsTheDelta(t *testing.T) {
	f := &fakeSocketBackend{keep: true}
	f.frames = keptReply(f)
	copilotSocketFake(t, f)
	s := New()
	copilotPost(t, s, copilotTurn("gpt-5.5", userPing))
	copilotPost(t, s, copilotTurn("gpt-5.5", userPing, asstPong, userAgain))

	posts, sockets := f.counts()
	if posts != 0 || sockets != 2 || f.opened() != 1 {
		t.Fatalf("%d over HTTP, %d turns on %d sockets; want both turns on one socket", posts, sockets, f.opened())
	}
	first, next := f.turn(0), f.turn(1)
	for i, turn := range []socketTurn{first, next} {
		m := gjson.ParseBytes(turn.msg)
		if m.Get("type").String() != "response.create" || m.Get("stream").Exists() || m.Get("model").String() != "gpt-5.5" {
			t.Errorf("turn %d's message: %s", i, turn.msg)
		}
		if m.Get("initiator").String() == "" {
			t.Errorf("turn %d's message says no initiator: %s", i, turn.msg)
		}
	}
	if gjson.GetBytes(first.msg, "previous_response_id").Exists() || len(gjson.GetBytes(first.msg, "input").Array()) != 1 {
		t.Errorf("the first turn: %s", first.msg)
	}
	if id := gjson.GetBytes(next.msg, "previous_response_id").String(); id != "resp-1" {
		t.Errorf("the next turn carries on from %q: %s", id, next.msg)
	}
	if in := gjson.GetBytes(next.msg, "input").Array(); len(in) != 1 || in[0].Get("content.0.text").String() != "again" {
		t.Errorf("the next turn is not its new input alone: %s", next.msg)
	}
	if h := first.header; h.Get("OpenAI-Beta") != "" || h.Get("X-Initiator") != "" ||
		h.Get("Authorization") != "Bearer sess" || h.Get("Copilot-Integration-Id") == "" {
		t.Errorf("the handshake's headers: %v", h)
	}
}

// A model Copilot's list doesn't name for "ws:/responses" goes over HTTP,
// as Copilot Chat sends it.
func TestCopilotSocketOnlyForItsModels(t *testing.T) {
	f := &fakeSocketBackend{keep: true}
	f.frames = keptReply(f)
	copilotSocketFake(t, f)
	s := New()
	copilotPost(t, s, copilotTurn("gpt-5.3-codex", userPing))
	copilotPost(t, s, copilotTurn("gpt-5.3-codex", userPing, asstPong, userAgain))
	if posts, sockets := f.counts(); posts != 2 || sockets != 0 || f.opened() != 0 {
		t.Fatalf("%d over HTTP, %d on a socket (%d opened); want both over HTTP", posts, sockets, f.opened())
	}
}

// A Copilot that refuses the handshake has the turn over HTTP.
func TestCopilotSocketRefusedGoesOverHTTP(t *testing.T) {
	f := &fakeSocketBackend{keep: true, refuse: http.StatusNotFound}
	f.frames = keptReply(f)
	copilotSocketFake(t, f)
	s := New()
	copilotPost(t, s, copilotTurn("gpt-5.5", userPing))
	copilotPost(t, s, copilotTurn("gpt-5.5", userPing, asstPong, userAgain))
	if posts, sockets := f.counts(); posts != 2 || sockets != 0 {
		t.Fatalf("%d over HTTP, %d on a socket; want both over HTTP", posts, sockets)
	}
}

// Copilot's error frame names no status ({"type":"error","error":
// {"code":"bad_request",...}}, the real account's answer to a
// previous_response_id from another socket): the turn is asked over HTTP,
// whose reply has the status resting and retries read.
func TestCopilotSocketErrorFrameGoesOverHTTP(t *testing.T) {
	f := &fakeSocketBackend{keep: true}
	f.frames = func(socketTurn) []string {
		return []string{`{"type":"error","error":{"type":"invalid_request_error","code":"bad_request","message":"previous_response_id is not supported"}}`}
	}
	copilotSocketFake(t, f)
	copilotPost(t, New(), copilotTurn("gpt-5.5", userPing))
	if posts, sockets := f.counts(); posts != 1 || sockets != 1 {
		t.Fatalf("%d over HTTP, %d on a socket; want the socket's refusal asked again over HTTP", posts, sockets)
	}
}

// Copilot's reasoning ids are Base64 ("+" and "/" in them), and such
// reasoning is left out of the next turn (plainItemIDs): the real
// account's gpt-5.3-codex turn came back reasoning then message, and the
// next went whole until a turn without its reasoning counted as carrying
// on. Copilot keeps that reasoning itself, with the response.
func TestCopilotSocketDeltaWithoutItsReasoning(t *testing.T) {
	const sealed = `{"type":"reasoning","id":"Y9Jw8X3JbNrD+gvjyaVn/JJSBK==","summary":[],"encrypted_content":"tInH0RH7w5ID5F7p"}`
	f := &fakeSocketBackend{keep: true}
	reply := keptReply(f)
	f.frames = func(turn socketTurn) []string {
		frames := reply(turn)
		return append([]string{frames[0], `{"type":"response.output_item.done","output_index":0,"item":` + sealed + `}`}, frames[1:]...)
	}
	copilotSocketFake(t, f)
	s := New()
	copilotPost(t, s, copilotTurn("gpt-5.5", userPing))
	copilotPost(t, s, copilotTurn("gpt-5.5", userPing, sealed, asstPong, userAgain))
	if posts, sockets := f.counts(); posts != 0 || sockets != 2 || f.opened() != 1 {
		t.Fatalf("%d over HTTP, %d turns on %d sockets; want both turns on one socket", posts, sockets, f.opened())
	}
	next := f.turn(1).msg
	if id := gjson.GetBytes(next, "previous_response_id").String(); id != "resp-1" {
		t.Errorf("the next turn carries on from %q: %s", id, next)
	}
	if in := gjson.GetBytes(next, "input").Array(); len(in) != 1 || in[0].Get("content.0.text").String() != "again" {
		t.Errorf("the next turn is not its new input alone: %s", next)
	}
}
