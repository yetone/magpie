package gateway

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A defective upstream stitches the fragments of a streamed tool call's
// arguments with CR: the JSON \r escape in the text is a CR it put at a
// token boundary (yetone/magpie#4256), and the client reads one broken
// line a CR. The deltas a client stitches say what it should have.

// stitched is the arguments a defective relay sends, its CR at the token
// boundaries, and clean is what the client should read: every stitched CR
// gone, the \r\n pair (a line ending), the \n and the \\r (a backslash
// then an r, no CR at all) as they were.
const (
	stitched = `{"a":"x\ry\r\nz","n":"p\nq","b":"m\\rs","t":"tail\r"}`
	clean    = `{"a":"xy\r\nz","n":"p\nq","b":"m\\rs","t":"tail"}`
)

// crInput reads what the client was given of a tool call's input: its
// partial_json pieces, stitched, as a JSON string's insides would show.
func crInput(t *testing.T, body string) string {
	t.Helper()
	var pieces []string
	for _, ev := range events(body) {
		if ev["type"] != "content_block_delta" {
			continue
		}
		d, _ := ev["delta"].(map[string]any)
		if d == nil || d["type"] != "input_json_delta" {
			continue
		}
		p, _ := d["partial_json"].(string)
		pieces = append(pieces, p)
	}
	return strings.Join(pieces, "")
}

// TestRelayedToolArgsLoseStitchedCR is the route the issue came from: a
// Messages upstream whose own stream is relayed as it came, its fragments
// stitched with CR.
func TestRelayedToolArgsLoseStitchedCR(t *testing.T) {
	fresh(t)
	f := &fake{t: t, reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"glm-5.3-flash","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"AskUserQuestion","input":{}}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"a\":\"x\\r"}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"y\\r\\nz\",\"n\":\"p\\nq\",\"b\":\"m\\\\rs\",\"t\":\"tail\\r\"}"}}`,
		`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
		`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":1,"output_tokens":3}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`,
	)}
	crModel(t, provider.Anthropic, setup(t, provider.Anthropic, f), "glm-5.3-flash")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"glm-5.3-flash","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := crInput(t, rec.Body.String()); got != clean {
		t.Fatalf("client read %s, want %s", got, clean)
	}
	var input struct {
		A, N, B, T string
	}
	if err := json.Unmarshal([]byte(clean), &input); err != nil {
		panic(err)
	}
	if input.A != "xy\r\nz" || input.N != "p\nq" || input.B != `m\rs` || input.T != "tail" {
		t.Fatalf("input %q", input)
	}
}

// TestTranslatedToolArgsLoseStitchedCR is the same call from an upstream
// magpie speaks Chat to, translated for the Messages client.
func TestTranslatedToolArgsLoseStitchedCR(t *testing.T) {
	fresh(t)
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"AskUserQuestion","arguments":"{\"a\":\"x\\r"}}]}}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"y\\r\\nz\",\"n\":\"p\\nq\",\"b\":\"m\\\\rs\",\"t\":\"tail\\r\"}"}}]}}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	)}
	crModel(t, provider.Chat, setup(t, provider.Chat, f), "glm-5.3-flash")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"glm-5.3-flash","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := crInput(t, rec.Body.String()); got != clean {
		t.Fatalf("client read %s, want %s", got, clean)
	}
}

// TestCRDropper walks the filter over a stream's pieces the way fragments
// arrive: an escape cut in half, the \r\n pair cut in half, a call's last
// piece ending in the defect.
func TestCRDropper(t *testing.T) {
	for _, c := range []struct {
		pieces, want string
	}{
		{`x\ry`, `xy`},               // the defect, between fragments
		{`x\r\ny`, `x\r\ny`},         // a line ending, kept
		{`x\` + "\ny", `x\` + "\ny"}, // a backslash before a real newline, kept
		{`p\nq`, `p\nq`},             // a line feed, kept
		{`a\\rb`, `a\\rb`},           // a backslash then an r: no CR, kept
	} {
		var d crDropper
		got := d.write(c.pieces) + d.flush()
		if got != c.want {
			t.Errorf("whole %s: got %s, want %s", c.pieces, got, c.want)
		}
	}
	for _, c := range []struct {
		pieces []string
		want   string
	}{
		{[]string{`x\`, `ry`}, `xy`},                                  // the escape cut in half
		{[]string{`x\r\`, `ny`}, `x\r\ny`},                            // the pair cut in half
		{[]string{`x\`}, `x\`},                                        // a lone backslash at the end, said as it came
		{[]string{`x\r`}, `x`},                                        // a trailing CR: alone, gone
		{[]string{`\`}, `\`},                                          // a call that opens with half an escape
		{[]string{`{"a":"b\`, "\nnc\"}"}, `{"a":"b\` + "\n" + `nc"}`}, // a real line ending, cut
	} {
		d := crDropper{}
		got := ""
		for _, p := range c.pieces {
			got += d.write(p)
		}
		if got = got + d.flush(); got != c.want {
			t.Errorf("pieces %q: got %q, want %q", c.pieces, got, c.want)
		}
	}
}

// TestCRLeavesHealthyStreamsAlone says a relayed stream with no stitched
// CR comes out byte for byte as it came, whatever size the reads are.
func TestCRLeavesHealthyStreamsAlone(t *testing.T) {
	stream := sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"glm-5.3-flash","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"Edit","input":{}}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"file\":\"a\\nb\\r\\nc\",\"repl\":\"x<y>&z\",\"n\": 12}"}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"说\\r的"}}`,
		`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
		`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":1,"output_tokens":3}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`,
	)
	wholeTidy := &crTidy{proto: provider.Anthropic, sse: true}
	whole := string(wholeTidy.write([]byte(stream))) + string(wholeTidy.flush())
	if whole != stream {
		t.Fatalf("a whole healthy stream changed:\n%s\nwant\n%s", whole, stream)
	}
	for _, n := range []int{1, 2, 3, 7, 13} {
		tidy := &crTidy{proto: provider.Anthropic, sse: true}
		got := ""
		for i := 0; i < len(stream); i += n {
			j := min(i+n, len(stream))
			got += string(tidy.write([]byte(stream[i:j])))
		}
		if got += string(tidy.flush()); got != stream {
			t.Fatalf("reads of %d changed the stream:\n%s\nwant\n%s", n, got, stream)
		}
	}
}

// anthropicStitched is the defective upstream's Messages stream, its call's
// fragments stitched with CR, as the tests of the translated replies share
// it.
var anthropicStitched = sse(
	`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"glm-5.3-flash","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
	`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"AskUserQuestion","input":{}}}`,
	`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"a\":\"x\\r"}}`,
	`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"y\\r\\nz\",\"n\":\"p\\nq\",\"b\":\"m\\\\rs\",\"t\":\"tail\\r\"}"}}`,
	`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
	`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":1,"output_tokens":3}}`,
	`event: message_stop`+"\n"+`data: {"type":"message_stop"}`,
)

// chatStreamStitched is the same call from a Chat upstream, its fragments
// stitched the same.
var chatStreamStitched = sse(
	`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"AskUserQuestion","arguments":"{\"a\":\"x\\r"}}]}}]}`,
	`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"y\\r\\nz\",\"n\":\"p\\nq\",\"b\":\"m\\\\rs\",\"t\":\"tail\\r\"}"}}]}}]}`,
	`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	`data: [DONE]`,
)

// chatWholeStitched is a whole chat reply whose one call's arguments carry
// the defect, as a replica that stitches serves a client that asked for
// one body.
const chatWholeStitched = `{"id":"c1","object":"chat.completion","model":"glm-5.3-flash","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"AskUserQuestion","arguments":"{\"a\":\"x\\ry\\r\\nz\",\"n\":\"p\\nq\",\"b\":\"m\\\\rs\",\"t\":\"tail\\r\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":3}}`

// compact says raw as JSON says an object: sorted keys, no bargains.
func compact(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var v any
	if json.Unmarshal(raw, &v) != nil {
		t.Fatalf("not JSON: %s", raw)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// chatArgs reads what a Chat client was given of a tool call's input: its
// arguments pieces, stitched.
func crChatArgs(t *testing.T, body string) string {
	t.Helper()
	var pieces []string
	for _, ev := range events(body) {
		choices, _ := ev["choices"].([]any)
		for _, c := range choices {
			ch, _ := c.(map[string]any)
			if ch == nil {
				continue
			}
			delta, _ := ch["delta"].(map[string]any)
			if delta == nil {
				continue
			}
			calls, _ := delta["tool_calls"].([]any)
			for _, k := range calls {
				call, _ := k.(map[string]any)
				if call == nil {
					continue
				}
				fn, _ := call["function"].(map[string]any)
				if fn == nil {
					continue
				}
				if a, _ := fn["arguments"].(string); a != "" {
					pieces = append(pieces, a)
				}
			}
		}
	}
	return strings.Join(pieces, "")
}

// responsesArgs reads what a Responses client was given of a tool call's
// input: its function call arguments deltas, stitched.
func responsesArgs(t *testing.T, body string) string {
	t.Helper()
	var pieces []string
	for _, ev := range events(body) {
		if ev["type"] != "response.function_call_arguments.delta" {
			continue
		}
		if d, _ := ev["delta"].(string); d != "" {
			pieces = append(pieces, d)
		}
	}
	return strings.Join(pieces, "")
}

// responsesDoneArgs are the arguments the reply's done events say whole.
func responsesDoneArgs(t *testing.T, body string) []string {
	t.Helper()
	var out []string
	for _, ev := range events(body) {
		switch ev["type"] {
		case "response.output_item.done":
			item, _ := ev["item"].(map[string]any)
			if item == nil || item["type"] != "function_call" {
				continue
			}
			if a, _ := item["arguments"].(string); a != "" {
				out = append(out, a)
			}
		case "response.completed":
			res, _ := ev["response"].(map[string]any)
			if res == nil {
				continue
			}
			items, _ := res["output"].([]any)
			for _, k := range items {
				item, _ := k.(map[string]any)
				if item == nil || item["type"] != "function_call" {
					continue
				}
				if a, _ := item["arguments"].(string); a != "" {
					out = append(out, a)
				}
			}
		}
	}
	return out
}

// geminiStreamCalls are the arguments objects a Gemini stream gave, said
// back as JSON to compare.
func geminiStreamCalls(t *testing.T, body string) []string {
	t.Helper()
	var out []string
	for _, ev := range events(body) {
		cands, _ := ev["candidates"].([]any)
		for _, c := range cands {
			cand, _ := c.(map[string]any)
			if cand == nil {
				continue
			}
			content, _ := cand["content"].(map[string]any)
			if content == nil {
				continue
			}
			ps, _ := content["parts"].([]any)
			for _, p := range ps {
				part, _ := p.(map[string]any)
				if part == nil {
					continue
				}
				fc, _ := part["functionCall"].(map[string]any)
				if fc == nil {
					continue
				}
				raw, _ := json.Marshal(fc["args"])
				out = append(out, compact(t, raw))
			}
		}
	}
	return out
}

// TestChatClientToolArgsLoseStitchedCR is the call a Chat client reads of
// a Messages upstream, translated: its arguments pieces go out of the
// chatEncoder stitched as they came in.
func TestChatClientToolArgsLoseStitchedCR(t *testing.T) {
	fresh(t)
	f := &fake{t: t, reply: anthropicStitched}
	crModel(t, provider.Anthropic, setup(t, provider.Anthropic, f), "glm-5.3-flash")
	code, body := post(t, "/v1/chat/completions",
		`{"model":"glm-5.3-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := crChatArgs(t, body); got != clean {
		t.Fatalf("client read %s, want %s", got, clean)
	}
}

// TestChatReplyToolArgsLoseStitchedCR is the same call, the client asking
// for one body: the reply is relayed as it came (chatWhole), the call's
// arguments read where they travel.
func TestChatReplyToolArgsLoseStitchedCR(t *testing.T) {
	fresh(t)
	f := &fake{t: t, ctype: "application/json", reply: chatWholeStitched}
	crModel(t, provider.Chat, setup(t, provider.Chat, f), "glm-5.3-flash")
	code, body := post(t, "/v1/chat/completions", `{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var reply struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(body), &reply) != nil {
		t.Fatalf("reply: %s", body)
	}
	for _, ch := range reply.Choices {
		for _, call := range ch.Message.ToolCalls {
			if call.Function.Arguments != clean {
				t.Fatalf("client read %s, want %s", call.Function.Arguments, clean)
			}
			return
		}
	}
	t.Fatalf("no call in %s", body)
}

// TestResponsesClientToolArgsLoseStitchedCR is the call a Responses client
// reads of a Messages upstream, translated: the deltas and the done
// events' whole arguments go out of the responsesEncoder stitched as they
// came in.
func TestResponsesClientToolArgsLoseStitchedCR(t *testing.T) {
	fresh(t)
	f := &fake{t: t, reply: anthropicStitched}
	crModel(t, provider.Anthropic, setup(t, provider.Anthropic, f), "glm-5.3-flash")
	code, body := post(t, "/v1/responses", `{"model":"glm-5.3-flash","stream":true,"input":"hi"}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := responsesArgs(t, body); got != clean {
		t.Fatalf("client read %s, want %s", got, clean)
	}
	done := responsesDoneArgs(t, body)
	if len(done) == 0 {
		t.Fatalf("no done arguments in %s", body)
	}
	for _, a := range done {
		if a != clean {
			t.Fatalf("done said %s, want %s", a, clean)
		}
	}
}

// TestResponsesReplyToolArgsLoseStitchedCR is the same call, the client
// asking for one body: renderResponses says the call's arguments as the
// reply brought them.
func TestResponsesReplyToolArgsLoseStitchedCR(t *testing.T) {
	fresh(t)
	// a Chat upstream is asked streamed even for a client that wants one
	// body; the reply is read and said whole
	f := &fake{t: t, reply: chatStreamStitched}
	crModel(t, provider.Chat, setup(t, provider.Chat, f), "glm-5.3-flash")
	code, body := post(t, "/v1/responses", `{"model":"glm-5.3-flash","input":"hi"}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var reply struct {
		Output []struct {
			Type      string `json:"type"`
			Arguments string `json:"arguments"`
		} `json:"output"`
	}
	if json.Unmarshal([]byte(body), &reply) != nil {
		t.Fatalf("reply: %s", body)
	}
	for _, item := range reply.Output {
		if item.Type == "function_call" {
			if item.Arguments != clean {
				t.Fatalf("client read %s, want %s", item.Arguments, clean)
			}
			return
		}
	}
	t.Fatalf("no call in %s", body)
}

// TestGeminiClientToolArgsLoseStitchedCR is the call a Gemini client reads
// of a Chat upstream, translated: Gemini takes a call whole, its arguments
// an object the encoder says once the fragments have all come.
func TestGeminiClientToolArgsLoseStitchedCR(t *testing.T) {
	fresh(t)
	f := &fake{t: t, reply: chatStreamStitched}
	crModel(t, provider.Chat, setup(t, provider.Chat, f), "glm-5.3-flash")
	code, body := post(t, "/v1beta/models/fake/glm-5.3-flash:streamGenerateContent?alt=sse",
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	calls := geminiStreamCalls(t, body)
	if len(calls) != 1 {
		t.Fatalf("calls %q", calls)
	}
	if calls[0] != compact(t, []byte(clean)) {
		t.Fatalf("client read %s, want %s", calls[0], compact(t, []byte(clean)))
	}
}

// TestGeminiReplyToolArgsLoseStitchedCR is the same call, the client
// asking for one body: renderGemini says the call's arguments as the reply
// brought them.
func TestGeminiReplyToolArgsLoseStitchedCR(t *testing.T) {
	fresh(t)
	f := &fake{t: t, reply: chatStreamStitched}
	crModel(t, provider.Chat, setup(t, provider.Chat, f), "glm-5.3-flash")
	code, body := post(t, "/v1beta/models/fake/glm-5.3-flash:generateContent",
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var reply struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					FunctionCall struct {
						Args json.RawMessage `json:"args"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if json.Unmarshal([]byte(body), &reply) != nil {
		t.Fatalf("reply: %s", body)
	}
	n := 0
	for _, cand := range reply.Candidates {
		for _, part := range cand.Content.Parts {
			if part.FunctionCall.Args == nil {
				continue
			}
			if got := compact(t, part.FunctionCall.Args); got != compact(t, []byte(clean)) {
				t.Fatalf("client read %s, want %s", got, compact(t, []byte(clean)))
			}
			n++
		}
	}
	if n == 0 {
		t.Fatalf("no call in %s", body)
	}
}

// TestMessagesReplyToolArgsLoseStitchedCR is the call a Messages client
// that asked for one body reads of a Chat upstream: renderAnthropic says
// the call's input as the reply brought it.
func TestMessagesReplyToolArgsLoseStitchedCR(t *testing.T) {
	fresh(t)
	// a Chat upstream is always asked streamed; the reply is gathered for
	// the client that asked for one body
	f := &fake{t: t, reply: chatStreamStitched}
	crModel(t, provider.Chat, setup(t, provider.Chat, f), "glm-5.3-flash")
	code, body := post(t, "/v1/messages", `{"model":"glm-5.3-flash","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var reply struct {
		Content []struct {
			Type  string          `json:"type"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if json.Unmarshal([]byte(body), &reply) != nil {
		t.Fatalf("reply: %s", body)
	}
	n := 0
	for _, bl := range reply.Content {
		if bl.Type != "tool_use" {
			continue
		}
		if got := compact(t, bl.Input); got != compact(t, []byte(clean)) {
			t.Fatalf("client read %s, want %s", got, compact(t, []byte(clean)))
		}
		n++
	}
	if n == 0 {
		t.Fatalf("no call in %s", body)
	}
}

// TestChatRelayedToolArgsLoseStitchedCR is the call a Chat client reads of
// a Chat upstream, the stream relayed as it came: the arguments travel in
// the lines' own tool_calls, and their stitched CR goes there.
func TestChatRelayedToolArgsLoseStitchedCR(t *testing.T) {
	fresh(t)
	f := &fake{t: t, reply: chatStreamStitched}
	crModel(t, provider.Chat, setup(t, provider.Chat, f), "glm-5.3-flash")
	code, body := post(t, "/v1/chat/completions",
		`{"model":"glm-5.3-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := crChatArgs(t, body); got != clean {
		t.Fatalf("client read %s, want %s", got, clean)
	}
}

// responsesStreamStitched is the same call from a Responses upstream, its
// fragments stitched the same, a function call's arguments in the delta
// events' own strings.
var responsesStreamStitched = sse(
	`data: {"type":"response.created","response":{"id":"resp_1"}}`,
	`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"AskUserQuestion","arguments":""}}`,
	`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"a\":\"x\\ry"}`,
	`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"\\r\\nz\",\"n\":\"p\\nq\",\"b\":\"m\\\\rs\",\"t\":\"tail\\r\"}"}`,
	`data: {"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"arguments":"{\"a\":\"x\\ry\\r\\nz\",\"n\":\"p\\nq\",\"b\":\"m\\\\rs\",\"t\":\"tail\\r\"}"}`,
	`data: {"type":"response.completed"}`,
)

// TestResponsesRelayedToolArgsLoseStitchedCR is the call a Responses
// client reads of a Responses upstream, the stream relayed as it came: a
// function call's fragments travel in the delta events' delta strings,
// and their stitched CR goes there.
func TestResponsesRelayedToolArgsLoseStitchedCR(t *testing.T) {
	fresh(t)
	f := &fake{t: t, reply: responsesStreamStitched}
	crModel(t, provider.Responses, setup(t, provider.Responses, f), "glm-5.3-flash")
	code, body := post(t, "/v1/responses", `{"model":"glm-5.3-flash","stream":true,"input":"hi"}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := responsesArgs(t, body); got != clean {
		t.Fatalf("client read %s, want %s", got, clean)
	}
	for _, a := range responsesDoneArgs(t, body) {
		if a != clean {
			t.Fatalf("done said %s, want %s", a, clean)
		}
	}
}

// anthropicWholeStitched is a whole Messages reply whose one call's input
// object carries the defect in its strings, as a replica that stitches
// serves a client that asked for one body.
var anthropicWholeStitched = `{"id":"msg_1","type":"message","role":"assistant","model":"glm-5.3-flash","content":[{"type":"tool_use","id":"toolu_1","name":"AskUserQuestion","input":{"a":"x\ry\r\nz","n":"p\nq","b":"m\\rs","t":"tail\r"}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":3}}`

// TestAnthropicWholeRelayedToolArgsLoseStitchedCR is the same reply, the
// client asking for one body: the input object is an arguments' own
// object, and the stitched CR of its strings goes.
func TestAnthropicWholeRelayedToolArgsLoseStitchedCR(t *testing.T) {
	fresh(t)
	f := &fake{t: t, ctype: "application/json", reply: anthropicWholeStitched}
	crModel(t, provider.Anthropic, setup(t, provider.Anthropic, f), "glm-5.3-flash")
	code, body := post(t, "/v1/messages", `{"model":"glm-5.3-flash","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var reply struct {
		Content []struct {
			Type  string          `json:"type"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if json.Unmarshal([]byte(body), &reply) != nil {
		t.Fatalf("reply: %s", body)
	}
	for _, block := range reply.Content {
		if block.Type == "tool_use" {
			if got, want := compact(t, block.Input), compact(t, json.RawMessage(clean)); got != want {
				t.Fatalf("client read %s, want %s", got, want)
			}
			return
		}
	}
	t.Fatalf("no call in %s", body)
}

// crModel is setup's provider said again with the model a test's client
// asks for: the cure follows the model (glm-5.3-flash*, #4256), so these
// tests ask for one of those routes, or, for the scope test, another.
func crModel(t *testing.T, proto provider.Protocol, up *httptest.Server, model string) {
	t.Helper()
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{model}}
	switch proto {
	case provider.Chat:
		p.Chat = up.URL + "/v1"
	case provider.Responses:
		p.Responses = up.URL + "/v1"
	case provider.Anthropic:
		p.Anthropic = up.URL
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// healthySplit is a HEALTHY stream's call writing a CRLF file, its one
// argument fragment cut where a vendor's delta boundary lands: the first
// piece ends with the \r of a \r\n pair, the second begins with its \n.
// Nothing here is the defect, and the client reads the pair whole.
const (
	healthySplit1 = `{"content":"line1\r`
	healthySplit2 = `\nline2\r\n"}`
	healthyWhole  = `{"content":"line1\r\nline2\r\n"}`
)

// TestMessagesRelayKeepsHealthyCRLFSplit is yetone's review probe: a plain
// Anthropic-shaped Messages stream relayed as it came, its partial_json
// cut between the \r and the \n of a line ending. The pair survives; the
// client reads the file's line ending as it was written.
func TestMessagesRelayKeepsHealthyCRLFSplit(t *testing.T) {
	fresh(t)
	f := &fake{t: t, reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"glm-5.3-flash","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"Write","input":{}}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"content\":\"line1\\r"}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\\nline2\\r\\n\"}"}}`,
		`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
		`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":1,"output_tokens":3}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`,
	)}
	up := setup(t, provider.Anthropic, f)
	crModel(t, provider.Anthropic, up, "glm-5.3-flash")
	code, body := post(t, "/v1/messages", `{"model":"glm-5.3-flash","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := crInput(t, body); got != healthyWhole {
		t.Fatalf("client read %s, want %s", got, healthyWhole)
	}
}

// TestChatRelayKeepsHealthyCRLFSplit is the same healthy call from a Chat
// upstream relayed as it came, its arguments cut at the same boundary.
func TestChatRelayKeepsHealthyCRLFSplit(t *testing.T) {
	fresh(t)
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Write","arguments":"{\"content\":\"line1\\r"}}]}}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\\nline2\\r\\n\"}"}}]}}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	)}
	up := setup(t, provider.Chat, f)
	crModel(t, provider.Chat, up, "glm-5.3-flash")
	code, body := post(t, "/v1/chat/completions", `{"model":"glm-5.3-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := crChatArgs(t, body); got != healthyWhole {
		t.Fatalf("client read %s, want %s", got, healthyWhole)
	}
}

// TestResponsesRelayKeepsHealthyCRLFSplit is the same healthy call from a
// Responses upstream, its function_call_arguments.delta cut the same way.
func TestResponsesRelayKeepsHealthyCRLFSplit(t *testing.T) {
	fresh(t)
	f := &fake{t: t, reply: sse(
		`data: {"type":"response.created","response":{"id":"resp_1"}}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"Write","arguments":""}}`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"content\":\"line1\\r"}`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"\\nline2\\r\\n\"}"}`,
		`data: {"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"arguments":"{\"content\":\"line1\\r\\nline2\\r\\n\"}"}`,
		`data: {"type":"response.completed"}`,
	)}
	up := setup(t, provider.Responses, f)
	crModel(t, provider.Responses, up, "glm-5.3-flash")
	code, body := post(t, "/v1/responses", `{"model":"glm-5.3-flash","stream":true,"input":"hi"}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := responsesArgs(t, body); got != healthyWhole {
		t.Fatalf("client read %s, want %s", got, healthyWhole)
	}
}

// TestCustomInputRelayKeepsHealthyCRLFSplit is Codex's apply_patch input, a
// custom tool's raw text whose patch writes a CRLF file, its delta cut
// between the \r and the \n: the patch's line ending survives whole.
func TestCustomInputRelayKeepsHealthyCRLFSplit(t *testing.T) {
	fresh(t)
	f := &fake{t: t, reply: sse(
		`data: {"type":"response.created","response":{"id":"resp_1"}}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"custom_tool_call","id":"ct_1","call_id":"call_1","name":"apply_patch","input":""}}`,
		"data: {\"type\":\"response.custom_tool_call_input.delta\",\"item_id\":\"ct_1\",\"output_index\":0,\"delta\":\"line1\\r\"}",
		"data: {\"type\":\"response.custom_tool_call_input.delta\",\"item_id\":\"ct_1\",\"output_index\":0,\"delta\":\"\\nline2\"}",
		`data: {"type":"response.completed"}`,
	)}
	up := setup(t, provider.Responses, f)
	crModel(t, provider.Responses, up, "glm-5.3-flash")
	code, body := post(t, "/v1/responses", `{"model":"glm-5.3-flash","stream":true,"input":"hi"}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var pieces []string
	for _, ev := range events(body) {
		if ev["type"] == "response.custom_tool_call_input.delta" {
			if d, _ := ev["delta"].(string); d != "" {
				pieces = append(pieces, d)
			}
		}
	}
	if got := strings.Join(pieces, ""); got != "line1\r\nline2" {
		t.Fatalf("client read %q, want %q", got, "line1\r\nline2")
	}
}

// TestRelayedTextDeltaMentioningArgumentsStands is a text delta that only
// mentions the arguments' event names and carries a \r: a reply's own
// words, whatever its text says, are not arguments to mend.
func TestRelayedTextDeltaMentioningArgumentsStands(t *testing.T) {
	fresh(t)
	f := &fake{t: t, reply: sse(
		`data: {"type":"response.created","response":{"id":"resp_1"}}`,
		`data: {"type":"response.output_text.delta","delta":"the response.function_call_arguments.delta event said x\\ry"}`,
		`data: {"type":"response.completed"}`,
	)}
	up := setup(t, provider.Responses, f)
	crModel(t, provider.Responses, up, "glm-5.3-flash")
	code, body := post(t, "/v1/responses", `{"model":"glm-5.3-flash","stream":true,"input":"hi"}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var said []string
	for _, ev := range events(body) {
		if ev["type"] == "response.output_text.delta" {
			if d, _ := ev["delta"].(string); d != "" {
				said = append(said, d)
			}
		}
	}
	if len(said) != 1 || said[0] != "the response.function_call_arguments.delta event said x\\ry" {
		t.Fatalf("client read %q, the text as it came", said)
	}
}

// TestForeignModelStitchedStreamGoesAsItCame is the scope: the defect was
// seen on glm-5.3-flash* replicas, so another model's stream carrying the
// defect's very shape goes to its client byte for byte, uncured.
func TestForeignModelStitchedStreamGoesAsItCame(t *testing.T) {
	fresh(t)
	f := &fake{t: t, reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m1","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"AskUserQuestion","input":{}}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"a\":\"x\\r"}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"y\\r\\nz\"}"}}`,
		`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
		`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":1,"output_tokens":3}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`,
	)}
	setup(t, provider.Anthropic, f)
	code, body := post(t, "/v1/messages", `{"model":"m1","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := crInput(t, body); got != `{"a":"x\ry\r\nz"}` {
		t.Fatalf("another model's stream changed: client read %s", got)
	}
}

// TestRelayedSplitDefectStillCured is the defect's own cut: a stitched CR
// at a fragment's end the next fragment does not answer with \n is alone,
// and goes, whatever fragment boundary it sits on.
func TestRelayedSplitDefectStillCured(t *testing.T) {
	fresh(t)
	f := &fake{t: t, reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"glm-5.3-flash","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"AskUserQuestion","input":{}}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"a\":\"x\\r"}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"y\"}"}}`,
		`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
		`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":1,"output_tokens":3}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`,
	)}
	up := setup(t, provider.Anthropic, f)
	crModel(t, provider.Anthropic, up, "glm-5.3-flash-max")
	code, body := post(t, "/v1/messages", `{"model":"glm-5.3-flash-max","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := crInput(t, body); got != `{"a":"xy"}` {
		t.Fatalf("client read %s, want {\"a\":\"xy\"}", got)
	}
}

// crSpy is a stream encoder that records what it is told, for the
// crEncoder's own tests.
type crSpy struct {
	said []Event
}

func (s *crSpy) event(ev Event) { s.said = append(s.said, ev) }
func (s *crSpy) finish()        {}
func (s *crSpy) keepalive()     {}

// TestCREscapeSaidAtCallEnd is the seam a whole argument's last escape is
// cut at: a bare \ the reply ended on is said before the stop, a \r that
// turned out alone is not, and a next call's start hears the tail of the
// one before it first.
func TestCREscapeSaidAtCallEnd(t *testing.T) {
	spy := &crSpy{}
	e := &crEncoder{streamEncoder: spy}
	e.event(Event{Kind: KToolStart, ID: "c1", Name: "n"})
	e.event(Event{Kind: KToolArgs, Text: `{"a":"x\`})
	e.event(Event{Kind: KStop, Stop: "end_turn"})
	var got string
	for _, ev := range spy.said {
		if ev.Kind == KToolArgs {
			got += ev.Text
		}
	}
	// the piece the reply ended on is said before the stop: a bare \ as
	// it came, stitched with the piece before it
	if len(spy.said) != 4 || got != `{"a":"x\` || spy.said[3].Kind != KStop {
		t.Fatalf("events %d, deltas %q", len(spy.said), got)
	}

	// a \r at the very end is alone: it goes, and nothing empty is said
	spy = &crSpy{}
	e = &crEncoder{streamEncoder: spy}
	e.event(Event{Kind: KToolStart, ID: "c1", Name: "n"})
	e.event(Event{Kind: KToolArgs, Text: `{"a":"x\r`})
	e.event(Event{Kind: KStop, Stop: "end_turn"})
	got = ""
	for _, ev := range spy.said {
		if ev.Kind == KToolArgs {
			if ev.Text == "" {
				t.Error("an empty delta was said")
			}
			got += ev.Text
		}
	}
	if len(spy.said) != 3 || got != `{"a":"x` {
		t.Fatalf("a lone trailing \r was said: %d events, deltas %q", len(spy.said), got)
	}

	// two calls: the first one's tail belongs to the first one's block
	spy = &crSpy{}
	e = &crEncoder{streamEncoder: spy}
	e.event(Event{Kind: KToolStart, ID: "c1", Name: "n"})
	e.event(Event{Kind: KToolArgs, Text: `{"a":"x\`})
	e.event(Event{Kind: KToolStart, ID: "c2", Name: "n"})
	var at int
	for i, ev := range spy.said {
		if ev.Kind == KToolStart && ev.ID == "c2" {
			at = i
		}
	}
	if at != 3 || spy.said[2].Kind != KToolArgs || spy.said[2].Text != `\` {
		t.Fatalf("tail of the first call not said before the second opened: %d events", len(spy.said))
	}
}

// TestGLMFlashScope is the gate itself: the routes the defect was seen on,
// under a router's prefix, said in any case; not their bigger family, not
// another vendor.
func TestGLMFlashScope(t *testing.T) {
	for _, c := range []struct {
		id   string
		want bool
	}{
		{"glm-5.3-flash", true},
		{"glm-5.3-flash-max", true},
		{"GLM-5.3-Flash", true},
		{"zhipu/glm-5.3-flash", true},
		{"glm-5.3", false},
		{"glm-5.4-flash", false},
		{"glm-4", false},
		{"grok-4", false},
	} {
		if got := glmFlash(c.id); got != c.want {
			t.Errorf("glmFlash(%q) = %v", c.id, got)
		}
	}
}

// TestCRTidyTwentyFourMBLine is the relay hot path's bill: one huge SSE
// line (a big response.completed) in 32KB reads. Without the escape the
// line comes back as it came, at a scan's cost; with one, the call it
// carries is read once and said again. The reads themselves were
// quadratic once (yetone's review of #4256 measured 2.43s). What is
// bounded is the work the code controls, the bytes searched and the bytes
// allocated, each a small multiple of the line's: a wall-clock bar
// measured the machine, and went red under -race beside heavy shards
// (16.9s on ubuntu CI, #5282).
func TestCRTidyTwentyFourMBLine(t *testing.T) {
	unit := `{"type":"text","text":"hello \"world\" padding "}`
	rep := (24 << 20) / (len(unit) + 1)
	big := func(withCall bool) string {
		var b strings.Builder
		b.WriteString(`data: {"type":"response.completed","response":{"output":[`)
		if withCall {
			b.WriteString(`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"n","arguments":"{\"a\":\"x\\ry\"}"},`)
		}
		for i := 0; i < rep; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(unit)
		}
		b.WriteString(`]}}` + "\n\n")
		return b.String()
	}
	// the mended line is said again with its keys sorted, so it is read
	// back rather than compared byte for byte
	mended := func(t *testing.T, got string) {
		if strings.Count(got, "data: ") != 1 || !strings.HasSuffix(got, "\n\n") {
			t.Fatal("the mended line is not one data line")
		}
		var ev struct {
			Response struct {
				Output []map[string]any `json:"output"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(got, "data: "), "\n\n")), &ev); err != nil {
			t.Fatalf("the mended line does not read: %v", err)
		}
		out := ev.Response.Output
		if len(out) != rep+1 {
			t.Fatalf("%d items came out, want %d", len(out), rep+1)
		}
		if out[0]["id"] != "fc_1" || out[0]["arguments"] != `{"a":"xy"}` {
			t.Fatalf("arguments %q, want {\"a\":\"xy\"}", out[0]["arguments"])
		}
		if out[rep]["text"] != `hello "world" padding ` {
			t.Fatalf("the last text item came out as %v", out[rep])
		}
	}
	for _, c := range []struct {
		name string
		line string
		want string
		// times the line's size it may allocate: the held line grows a
		// quarter at a time (~5x in all), and a mended line is also
		// decoded into maps and said again (27x, 32x under -race); a
		// copy of what is held on each read is ~400x
		room uint64
	}{
		{"no escape", big(false), big(false), 8},
		{"one call mended", big(true), "", 40},
	} {
		t.Run(c.name, func(t *testing.T) {
			tidy := &crTidy{proto: provider.Responses, sse: true}
			line := []byte(c.line)
			outs := make([][]byte, 0, len(line)/(32<<10)+2)
			start := time.Now()
			made := allocated(func() {
				for i := 0; i < len(line); i += 32 << 10 {
					if out := tidy.write(line[i:min(i+32<<10, len(line))]); out != nil {
						outs = append(outs, out)
					}
				}
				if out := tidy.flush(); out != nil {
					outs = append(outs, out)
				}
			})
			t.Logf("24MB line in 32KB reads: %s, %d bytes searched, %d allocated", time.Since(start), tidy.looked, made)
			if got := bytes.Join(outs, nil); c.want == "" {
				mended(t, string(got))
			} else if string(got) != c.want {
				t.Fatal("the line came out other than wanted")
			}
			// a read searched only for its own bytes, the line once
			// for the escape, and the changed line once more
			if n := len(line); tidy.looked > 3*n {
				t.Errorf("a %d-byte line was searched through %d bytes, over 3 times its size", n, tidy.looked)
			}
			if n := uint64(len(line)); made > c.room*n {
				t.Errorf("a %d-byte line allocated %d bytes, over %d times its size", n, made, c.room)
			}
		})
	}
}
