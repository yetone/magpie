package gateway

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// claudeCodeHead is the start of a Claude Code request as it sends one to
// a gateway: its system blocks with cache_control, a tool, its metadata.
const claudeCodeHead = `{"model":"fake/m1","max_tokens":32000,"stream":true,` +
	`"system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude.","cache_control":{"type":"ephemeral"}}],` +
	`"tools":[{"name":"Bash","description":"Run a command","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}},` +
	`{"name":"Read","description":"Read a file","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}}}}],` +
	`"metadata":{"user_id":"user_abc_account__session_1"},"messages":`

// pairedExchange is a Claude Code conversation whose tool exchange pairs:
// two parallel calls, both answered, the results first, then the user's
// next words; spacing, escapes and key order as a client may send them.
const pairedExchange = claudeCodeHead + `[` +
	`{"role":"user","content":[{"type":"text","text":"list the files & read go.mod"}]},` +
	`{"role":"assistant","content":[{"type":"thinking","thinking":"two calls","signature":"EqQBCkYIBxgC"},` +
	`{"type":"tool_use","id":"toolu_01A","name":"Bash","input":{"command":"ls"}},` +
	`{"type":"tool_use","id":"toolu_01B","name":"Read","input":{"file_path":"go.mod"}}]},` +
	`{"content":[{"tool_use_id":"toolu_01A","type":"tool_result","content":"go.mod\nmain.go"},` +
	`{"type":"tool_result","tool_use_id":"toolu_01B","content":[{"type":"text","text":"module x"}],"cache_control":{"type":"ephemeral"}},` +
	`{"type":"text","text":"now build it"}],"role":"user"}` +
	`]}`

// cutExchange is the conversation Claude Code holds after a reply was cut
// with its parallel calls told (or stopped while they ran): toolu_01B has
// no result, and the user's next words follow. Anthropic answers it
// "messages.2: `tool_use` ids were found without `tool_result` blocks
// immediately after: toolu_01B", and Claude Code shows "400 due to tool
// use concurrency issues" on every later turn (miwanzi on Discord).
const cutExchange = claudeCodeHead + `[` +
	`{"role":"user","content":[{"type":"text","text":"list the files and read go.mod"}]},` +
	`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01A","name":"Bash","input":{"command":"ls"}},` +
	`{"type":"tool_use","id":"toolu_01B","name":"Read","input":{"file_path":"go.mod"}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01A","content":"go.mod\nmain.go"}]},` +
	`{"role":"assistant","content":[{"type":"text","text":"Done."}]},` +
	`{"role":"user","content":"go on"}` +
	`]}`

// TestToolBlocksPairedLeavesAPairedBodyAsItCame holds that a request whose
// exchange pairs is not rewritten: the same bytes, keys in their order and
// escapes as written.
func TestToolBlocksPairedLeavesAPairedBodyAsItCame(t *testing.T) {
	in := []byte(pairedExchange)
	if code, msg := refusesExchange(in); code != 0 {
		t.Fatalf("the fixture doesn't pair: %s", msg)
	}
	if out := toolBlocksPaired(in); !bytes.Equal(out, in) {
		t.Fatalf("a paired body was rewritten:\n%s\n%s", in, out)
	}
	// and with no tool exchange at all
	plain := []byte(claudeCodeHead + `[{"role":"user","content":"hi"}]}`)
	if out := toolBlocksPaired(plain); !bytes.Equal(out, plain) {
		t.Fatalf("a body without tools was rewritten: %s", out)
	}
}

// A parallel call left without its result gets the synthetic one, after
// the result its sibling had, and nothing else of the request changes.
func TestToolBlocksPairedAnswersACutCall(t *testing.T) {
	in := []byte(cutExchange)
	if code, msg := refusesExchange(in); code != 400 || !strings.Contains(msg, "`tool_use` ids were found without `tool_result` blocks immediately after: toolu_01B") {
		t.Fatalf("the fixture isn't refused as Anthropic refuses it: %d %s", code, msg)
	}
	out := toolBlocksPaired(in)
	if code, msg := refusesExchange(out); code != 0 {
		t.Fatalf("still refused: %s\n%s", msg, out)
	}
	msgs := rawMessagesOf(t, out)
	if len(msgs) != 5 {
		t.Fatalf("%d messages, want 5: %s", len(msgs), out)
	}
	orig := rawMessagesOf(t, in)
	for _, i := range []int{0, 1, 3, 4} {
		if !bytes.Equal(msgs[i], orig[i]) {
			t.Fatalf("message %d changed:\n%s\n%s", i, orig[i], msgs[i])
		}
	}
	var m aMessageProbe
	if err := json.Unmarshal(msgs[2], &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Content) != 2 || m.Content[0].ToolUseID != "toolu_01A" || m.Content[1].ToolUseID != "toolu_01B" || !m.Content[1].IsError {
		t.Fatalf("results = %s", msgs[2])
	}
	var said string
	json.Unmarshal(m.Content[1].Content, &said)
	if said != wantInterrupted {
		t.Fatalf("synthetic result says %q", said)
	}
	// the rest of the body as it came
	if !bytes.HasPrefix(out, []byte(claudeCodeHead)) {
		t.Fatalf("the members before messages changed: %s", out)
	}
}

// A result whose call isn't in the message before (a turn the client lost,
// a result kept after its call was compacted away) becomes the text it
// said: "unexpected `tool_use_id` found in `tool_result` blocks".
func TestToolBlocksPairedOrphanResultBecomesText(t *testing.T) {
	in := []byte(claudeCodeHead + `[` +
		`{"role":"user","content":"read go.mod"},` +
		`{"role":"assistant","content":[{"type":"text","text":"Reading."}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01Gone","content":"module x"},{"type":"text","text":"and?"}]}` +
		`]}`)
	if code, msg := refusesExchange(in); code != 400 || !strings.Contains(msg, "unexpected `tool_use_id` found in `tool_result` blocks: toolu_01Gone") {
		t.Fatalf("the fixture isn't refused as Anthropic refuses it: %d %s", code, msg)
	}
	out := toolBlocksPaired(in)
	if code, msg := refusesExchange(out); code != 0 {
		t.Fatalf("still refused: %s\n%s", msg, out)
	}
	msgs := rawMessagesOf(t, out)
	var m aMessageProbe
	json.Unmarshal(msgs[2], &m)
	if len(m.Content) != 2 || m.Content[0].Type != "text" || m.Content[0].Text != "module x" || m.Content[1].Text != "and?" {
		t.Fatalf("message = %s", msgs[2])
	}
}

// A call that ends the conversation, a reply cut right after it, is
// answered by a user message of its own; and a call followed by another
// assistant message has its result put between them.
func TestToolBlocksPairedCallsWithNoUserTurnAfter(t *testing.T) {
	in := []byte(claudeCodeHead + `[` +
		`{"role":"user","content":"go"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_2","name":"Bash","input":{"command":"pwd"}}]}` +
		`]}`)
	out := toolBlocksPaired(in)
	if code, msg := refusesExchange(out); code != 0 {
		t.Fatalf("still refused: %s\n%s", msg, out)
	}
	var roles []string
	for _, m := range rawMessagesOf(t, out) {
		var p aMessageProbe
		json.Unmarshal(m, &p)
		roles = append(roles, p.Role)
	}
	if got := strings.Join(roles, " "); got != "user assistant user assistant user" {
		t.Fatalf("roles = %s", got)
	}
}

// A second answer to one call is left out, and a result after the turn's
// words goes first, which Anthropic wants of a user message holding
// results.
func TestToolBlocksPairedSecondAnswerAndOrder(t *testing.T) {
	in := []byte(claudeCodeHead + `[` +
		`{"role":"user","content":"go"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]},` +
		`{"role":"user","content":[{"type":"text","text":"here"},{"type":"tool_result","tool_use_id":"toolu_1","content":"first"},{"type":"tool_result","tool_use_id":"toolu_1","content":"again"}]}` +
		`]}`)
	out := toolBlocksPaired(in)
	var m aMessageProbe
	json.Unmarshal(rawMessagesOf(t, out)[2], &m)
	if len(m.Content) != 2 || m.Content[0].Type != "tool_result" || string(m.Content[0].Content) != `"first"` || m.Content[1].Text != "here" {
		t.Fatalf("message = %s", out)
	}
}

// Through the gateway: Claude Code's conversation with a cut parallel call,
// relayed to a Messages upstream that refuses a broken exchange as
// Anthropic does, is answered; before, every turn of it was refused 400.
// A paired one reaches the upstream with the bytes the client sent.
func TestMessagesRelayMendsABrokenToolExchange(t *testing.T) {
	f := &fake{t: t, refuse: refusesExchange, reply: sse(
		`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"m1","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		`data: {"type":"message_stop"}`,
	)}
	setup(t, provider.Anthropic, f)
	if code, out := post(t, "/v1/messages", cutExchange); code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	if f.path != "/v1/messages" {
		t.Fatalf("not relayed to Messages: %s", f.path)
	}
	if code, msg := refusesExchange(f.got); code != 0 {
		t.Fatalf("the upstream was asked a broken exchange: %s", msg)
	}
	if !strings.Contains(string(f.got), `"tool_use_id":"toolu_01B"`) {
		t.Fatalf("no result for the cut call reached the upstream: %s", f.got)
	}

	// the paired one: the upstream reads what the client sent, the
	// model's name swapped, every byte through the messages as sent (a
	// model named like no Claude gets "thinking" disabled after them,
	// as before this change)
	f.got = nil
	if code, out := post(t, "/v1/messages", pairedExchange); code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	want := strings.Replace(pairedExchange, `"model":"fake/m1"`, `"model":"m1"`, 1)
	want = strings.TrimSuffix(want, "}")
	if !strings.HasPrefix(string(f.got), want) {
		t.Fatalf("a paired request was rewritten on its way:\nsent %s\ngot  %s", want, f.got)
	}
}

// count_tokens is asked of the conversation as it will be sent: a vendor
// that counts validates the exchange as its messages endpoint does.
func TestCountTokensMendsABrokenToolExchange(t *testing.T) {
	f := &fake{t: t, refuse: refusesExchange, ctype: "application/json", reply: `{"input_tokens":42}`}
	setup(t, provider.Anthropic, f)
	body := strings.Replace(cutExchange, `"stream":true,`, ``, 1)
	code, out := post(t, "/v1/messages/count_tokens", body)
	if code != 200 || !strings.Contains(out, `"input_tokens":42`) {
		t.Fatalf("status %d: %s (upstream got %s)", code, out, f.got)
	}
	if code, msg := refusesExchange(f.got); code != 0 {
		t.Fatalf("the upstream counted a broken exchange: %s", msg)
	}
}

// rawMessagesOf is a request's messages as they are in its bytes.
func rawMessagesOf(t *testing.T, body []byte) []json.RawMessage {
	t.Helper()
	var q struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &q); err != nil {
		t.Fatalf("doesn't parse: %v\n%s", err, body)
	}
	return q.Messages
}

// refusesExchange is validateExchange of a request whose messages may have
// a string for their content, as Claude Code sends a user's words.
func refusesExchange(body []byte) (int, string) {
	var q map[string]json.RawMessage
	if err := json.Unmarshal(body, &q); err != nil {
		return validateExchange(body)
	}
	var msgs []map[string]any
	if err := json.Unmarshal(q["messages"], &msgs); err != nil {
		return validateExchange(body)
	}
	for _, m := range msgs {
		if s, ok := m["content"].(string); ok {
			m["content"] = []any{map[string]any{"type": "text", "text": s}}
		}
	}
	q["messages"], _ = json.Marshal(msgs)
	b, _ := json.Marshal(q)
	return validateExchange(b)
}
