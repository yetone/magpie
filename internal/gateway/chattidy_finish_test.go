package gateway

import (
	"strings"
	"testing"
)

// 喜欢一个人 on Discord: a relay in front of deepseek-v4.1-flash made the
// calls and ended the reply "stop", and a Chat client that reads
// finish_reason ended its turn with them not run. A relayed Chat reply
// that made calls ends "tool_calls", streamed and whole; one that made
// none keeps its "stop", and the other reasons stay as they came.
func TestChatCallsEndToolCalls(t *testing.T) {
	stream := `{"model":"a/m","stream":true,"messages":[{"role":"user","content":"go"}],` + execChatTool + `}`
	_, calls, finish, raw := relayedChat(t, stream, chunkOf("Running it."), nativeExec, stopChunk)
	if calls["exec_command"] != `{"cmd":"ls -la"}` || finish != "tool_calls" || !strings.Contains(raw, `"total_tokens":5`) {
		t.Fatalf("stream: calls %v finish %q\n%s", calls, finish, raw)
	}
	// with no tools offered, the same
	_, _, finish, raw = relayedChat(t, `{"model":"a/m","stream":true,"messages":[{"role":"user","content":"go"}]}`, nativeExec, stopChunk)
	if finish != "tool_calls" {
		t.Fatalf("stream, no tools offered: finish %q\n%s", finish, raw)
	}
	_, _, finish, raw = relayedChat(t, stream, chunkOf("Hi."), stopChunk)
	if finish != "stop" {
		t.Fatalf("stream, no call: finish %q\n%s", finish, raw)
	}
	_, _, finish, raw = relayedChat(t, stream, nativeExec, strings.Replace(stopChunk, `"stop"`, `"length"`, 1))
	if finish != "length" {
		t.Fatalf("stream cut short: finish %q\n%s", finish, raw)
	}

	whole := func(content, reason string) string {
		return `{"id":"c1","object":"chat.completion","created":1791370000,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":` + content + `,"tool_calls":[{"id":"call_up","type":"function","function":{"name":"exec_command","arguments":"{\"cmd\":\"ls -la\"}"}}]},"finish_reason":"` + reason + `"}]}`
	}
	for _, body := range []string{
		`{"model":"a/m","messages":[{"role":"user","content":"go"}],` + execChatTool + `}`,
		`{"model":"a/m","messages":[{"role":"user","content":"go"}]}`,
	} {
		for _, c := range []struct{ content, reason, want string }{
			{`"Running it."`, "stop", "tool_calls"},
			{`null`, "stop", "tool_calls"},
			{`null`, "length", "length"},
		} {
			_, wcalls, finish, raw := wholeChat(t, body, "application/json", whole(c.content, c.reason))
			if len(wcalls) != 1 || finish != c.want {
				t.Fatalf("whole %s %s: calls %+v finish %q\n%s", c.content, c.reason, wcalls, finish, raw)
			}
		}
	}
	_, _, finish, raw = wholeChat(t, `{"model":"a/m","messages":[{"role":"user","content":"go"}],`+execChatTool+`}`, "application/json", wholeChatUp("Hi."))
	if finish != "stop" {
		t.Fatalf("whole, no call: finish %q\n%s", finish, raw)
	}
}

// #4944 (Hermes through magpie): a stream whose reply is text alone ends,
// on the reporter's upstream, on a frame that says finish_reason
// "tool_calls" and carries no tool_calls key in any delta — the reporter's
// bare call of the model ends its real calls with just that shape
// ("delta":{"reasoning_content":null},"finish_reason":"tool_calls",
// "matched_stop":1), and its text-only turns end there too. A client that
// reads finish_reason took every such turn for one whose calls were
// dropped and re-prompted, three times a reply. A finish of tool_calls
// with no call the client could read ends stop, whether or not a delta
// ever carried the key.
func TestChatNoCallToolCallsFinishEndsStop(t *testing.T) {
	up := []string{
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"The user said hi."}}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"收到"}}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning_content":null},"logprobs":null,"finish_reason":"tool_calls","matched_stop":1}]}`,
	}
	for _, body := range []string{
		`{"model":"a/m","stream":true,"messages":[{"role":"user","content":"只回复两个字：收到"}],"reasoning_effort":"max",` + execChatTool + `}`,
		`{"model":"a/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
	} {
		_, calls, finish, raw := relayedChat(t, body, up...)
		if finish != "stop" || len(calls) != 0 {
			t.Fatalf("stream: calls %v finish %q\n%s", calls, finish, raw)
		}
	}
	// the finish naming a call with the final delta's "tool_calls": null
	// beside it (#3259's shape) ends stop the same way
	_, _, finish, raw := relayedChat(t, `{"model":"a/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		`data: {"choices":[{"index":0,"delta":{"content":"hi"}}]}`,
		`data: {"choices":[{"index":0,"delta":{"content":null,"tool_calls":null},"finish_reason":"tool_calls"}]}`)
	if finish != "stop" {
		t.Fatalf("stream, tool_calls null and no call: finish %q\n%s", finish, raw)
	}
}

// A legacy function_call a client may still be reading keeps a finish
// that says the turn ended in a call, "tool_calls" or not (#4659); a
// "function_call": null is the contract's own nothing and is not one.
func TestChatRelayLegacyCallFinish(t *testing.T) {
	tools := `{"model":"a/m","stream":true,"messages":[{"role":"user","content":"weather?"}],"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}]}`
	_, _, finish, raw := relayedChat(t, tools,
		`data: {"choices":[{"index":0,"delta":{"function_call":{"name":"get_weather","arguments":""}}}]}`,
		`data: {"choices":[{"index":0,"delta":{"function_call":{"name":"","arguments":"{\"city\":\"北京\"}"}}}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
	if finish != "tool_calls" {
		t.Fatalf("stream, a legacy call: finish %q\n%s", finish, raw)
	}
	_, _, finish, raw = relayedChat(t, tools,
		`data: {"choices":[{"index":0,"delta":{"content":"hi","function_call":null}}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
	if finish != "stop" {
		t.Fatalf("stream, function_call null and no call: finish %q\n%s", finish, raw)
	}
}
