package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Codex's input after a turn that ended on its tool results — interrupted,
// or failed with no answer — goes on with the user's next message right
// after the function_call_output, as codex 0.171.0 sent it on a resumed
// conversation (the items below, captured from it). The parse joined that
// message to the results, so turnIn read the user's new turn as the tool
// loop going on: the conversation stayed on the last turn's account, and
// its rules "wait for the next turn", on every turn after (zyh6036 on X:
// Smart Fusion's Quick Scout kept every message on MiniMax). A Chat tool
// message or an Anthropic tool_result followed by the user's words was a
// new turn already.
func TestTurnInCountsCodexMessageAfterItsLastResults(t *testing.T) {
	const head = `{"model":"m","stream":true,"store":false,"client_metadata":{"turn_id":"019e0b2c-turn-2","thread_id":"019e0b2c-thread"},"input":[` +
		`{"type":"message","role":"developer","content":[{"type":"input_text","text":"You are a coding agent running in the Codex CLI."}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/w</cwd>\n</environment_context>"}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"run echo hi"}]},` +
		`{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":[\"echo\",\"hi\"]}"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"unsupported call: shell"}`
	const words = `,{"type":"message","role":"user","content":[{"type":"input_text","text":"继续"}]}`
	const notice = `,{"type":"message","role":"developer","content":[{"type":"input_text","text":"<image_resize_notice>\nImage 1 of 1 was resized.\n</image_resize_notice>"}]}`
	const aborted = `,{"type":"message","role":"user","content":[{"type":"input_text","text":"<turn_aborted>\nThe user interrupted the previous turn on purpose.\n</turn_aborted>"}]}`
	const call2 = `,{"type":"function_call","call_id":"call_2","name":"shell","arguments":"{}"},{"type":"function_call_output","call_id":"call_2","output":"ok"}`
	for _, tc := range []struct {
		name   string
		body   string
		turn   int
		within bool
	}{
		// the tool loop going on: the account the turn began on holds
		{"results", head + `]}`, 1, true},
		{"a notice after the results", head + notice + `]}`, 1, true},
		{"the interrupt's marker alone", head + aborted + `]}`, 1, true},
		// the user's next message: a turn of theirs, for the rules to judge
		{"the user's words after the results", head + words + `]}`, 2, false},
		{"after the interrupt's marker", head + aborted + words + `]}`, 2, false},
		{"after a notice", head + notice + words + `]}`, 2, false},
		// and that turn's own tool loop goes on within it
		{"the new turn's results", head + words + call2 + `]}`, 2, true},
	} {
		turn, within := turnOf(provider.Responses, []byte(tc.body))
		if turn != tc.turn || within != tc.within {
			t.Errorf("%s: turn %d within %v, want %d %v", tc.name, turn, within, tc.turn, tc.within)
		}
	}
}

// The route of zyh6036's turn: magpie restarted since the last turn, which
// ended on its tool results, and the user's next message over Responses is
// judged by the rules, not left to "wait for the next turn" where the last
// one went.
func TestRuleJudgesCodexMessageAfterItsLastResults(t *testing.T) {
	s, a, b := ruled(t, provider.Rule{Use: "b/big", Tokens: 20000})
	body := `{"model":"group/r","client_metadata":{"turn_id":"turn-2","thread_id":"thread"},"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":` + quote(long(25000)) + `}]},` +
		`{"type":"function_call","call_id":"c1","name":"ls","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"c1","output":"file.txt"},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"继续"}]}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	req.Header.Set("x-session-id", "sess")
	s.Handler().ServeHTTP(rec, req)
	r := lastRoute(s)
	if rec.Code != 200 || r.Rule == nil || r.Rule.Waits || r.Rule.N != 1 || r.Rule.Turn != 2 {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), r.Rule)
	}
	if a.n() != 0 || b.n() != 1 {
		t.Fatalf("a %d b %d", a.n(), b.n())
	}
}
