package gateway

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// GLM on a Chat API reasoning, its filter then ending the reply with
// nothing said: Zhipu's finish_reason "sensitive" (yhwangl on X: the
// thinking was shown, the answer never came, and the next model wasn't
// asked).
var glmThoughtThenSensitive = sse(
	`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"first thought"}}]}`,
	`data: {"id":"c1","choices":[{"index":0,"delta":{"reasoning_content":" second thought"}}]}`,
	`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"sensitive"}],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}}`,
	`data: [DONE]`)

// The same, ended by OpenAI's content_filter.
var chatThoughtThenFiltered = strings.Replace(glmThoughtThenSensitive, `"sensitive"`, `"content_filter"`, 1)

// The same, ended by Zhipu's 1301 error mid-stream.
var glmThoughtThen1301 = sse(
	`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"first thought"}}]}`,
	`data: {"id":"c1","choices":[{"index":0,"delta":{"reasoning_content":" second thought"}}]}`,
	`data: {"error":{"code":"1301","message":"系统检测到输入或生成内容可能包含不安全或敏感内容，请您避免输入易产生敏感内容的提示语，感谢您的配合。"}}`)

// An Anthropic API's reasoning, its thinking block still open when the
// filter's stop_reason "refusal" ends it.
var glmAnthropicThoughtThenRefused = sse(
	`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m1","role":"assistant","model":"glm-5.1","content":[],"usage":{"input_tokens":3}}}`,
	`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
	`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"first thought"}}`,
	`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":" second thought"}}`,
	`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"refusal","stop_sequence":null},"usage":{"output_tokens":5}}`,
	`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)

const chatStreamAsk = `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`

// dataOf reads an SSE body's data, one per event, [DONE] as it is.
func dataOf(t *testing.T, body string) []string {
	t.Helper()
	var out []string
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		if d, ok := strings.CutPrefix(sc.Text(), "data:"); ok {
			out = append(out, strings.TrimSpace(d))
		}
	}
	return out
}

// A group's first member that reasons as it comes (a vendor whose filter
// isn't known to refuse after reasoning, GLM's way) and is refused by its
// filter before a word of its answer is said hands the request to the
// next member, whose answer follows the reasoning in the same stream, as
// one reply: Claude Code's, Codex's and a Chat client's. Nobody rests for
// it. Before, the agent had the reasoning, then the refusal, and the next
// member was never asked (yhwangl on X).
func TestRefusalAfterShownThinkingGoesToTheNext(t *testing.T) {
	for _, up := range []struct {
		name  string
		proto provider.Protocol
		body  string
	}{
		{"chat sensitive", provider.Chat, glmThoughtThenSensitive},
		{"chat content_filter", provider.Chat, chatThoughtThenFiltered},
		{"chat 1301", provider.Chat, glmThoughtThen1301},
		{"anthropic refusal", provider.Anthropic, glmAnthropicThoughtThenRefused},
	} {
		for _, client := range []struct {
			name, path, ask string
			check           func(t *testing.T, body string)
		}{
			{"messages", "/v1/messages", claudeCodeAsk, checkAnthropicSpliced},
			{"chat", "/v1/chat/completions", chatStreamAsk, checkChatSpliced},
			{"responses", "/v1/responses", codexAsk, checkResponsesSpliced},
		} {
			t.Run(up.name+" to "+client.name, func(t *testing.T) {
				fresh(t)
				a := &scripted{replies: []reply{{200, "text/event-stream", up.body}}}
				b := &scripted{replies: []reply{{200, "text/event-stream", anthropicAnswer}}}
				scriptedOn(t, "a", up.proto, a)
				scriptedOn(t, "b", provider.Anthropic, b)
				refusalGroup(t, "a/m", "b/m")
				s := New()
				code, body := sendTo(s, client.path, client.ask)
				if code != 200 || a.n != 1 || b.n != 1 {
					t.Fatalf("%d (a %d, b %d): %s", code, a.n, b.n, body)
				}
				if !strings.Contains(body, "first thought") || !strings.Contains(body, " second thought") || !strings.Contains(body, "from b") {
					t.Fatalf("not the reasoning and then the next member's answer: %s", body)
				}
				for _, w := range []string{"sensitive", "content_filter", "refusal", "1301", "safety filter"} {
					if strings.Contains(body, w) {
						t.Fatalf("the refusal (%s) reached the agent: %s", w, body)
					}
				}
				client.check(t, body)
				r := lastRoute(s)
				if len(r.Tries) != 2 || r.Tries[0].Fail != failRefused || r.Tries[0].Rest != nil || r.Tries[1].Fail != "" {
					t.Fatalf("tries: %+v", r.Tries)
				}
				if s.resting("a") {
					t.Fatal("the refused member was set aside")
				}
			})
		}
	}
}

// One message, the reasoning's block stopped before the next member's
// text block, numbered after it.
func checkAnthropicSpliced(t *testing.T, body string) {
	t.Helper()
	starts, blocks, open := 0, 0, map[int64]bool{}
	for _, d := range dataOf(t, body) {
		switch gjson.Get(d, "type").String() {
		case "message_start":
			starts++
		case "content_block_start":
			i := gjson.Get(d, "index").Int()
			if i != int64(blocks) || len(open) > 0 {
				t.Fatalf("block %d begun as block %d, %v still open: %s", blocks, i, open, body)
			}
			blocks++
			open[i] = true
		case "content_block_delta":
			if !open[gjson.Get(d, "index").Int()] {
				t.Fatalf("a delta for a block not open: %s", d)
			}
		case "content_block_stop":
			delete(open, gjson.Get(d, "index").Int())
		}
	}
	if starts != 1 || blocks != 2 || len(open) != 0 || strings.Count(body, `"type":"message_stop"`) != 1 {
		t.Fatalf("message_start %d, blocks %d, open %v: %s", starts, blocks, open, body)
	}
}

// One stream, one [DONE].
func checkChatSpliced(t *testing.T, body string) {
	t.Helper()
	ev := dataOf(t, body)
	if strings.Count(body, "[DONE]") != 1 || ev[len(ev)-1] != "[DONE]" {
		t.Fatalf("not one stream: %s", body)
	}
	for _, d := range ev[:len(ev)-1] {
		if !json.Valid([]byte(d)) {
			t.Fatalf("a chunk that isn't JSON: %q", d)
		}
	}
}

// One response begun, the next member's message after the reasoning's
// item, the sequence numbers going on from the reasoning's.
func checkResponsesSpliced(t *testing.T, body string) {
	t.Helper()
	created, last, items := 0, int64(-1), map[string]int64{}
	for _, d := range dataOf(t, body) {
		typ := gjson.Get(d, "type").String()
		if typ == "response.created" {
			created++
		}
		if sq := gjson.Get(d, "sequence_number"); sq.Exists() {
			if sq.Int() != last+1 {
				t.Fatalf("sequence_number %d after %d: %s", sq.Int(), last, body)
			}
			last = sq.Int()
		}
		if typ == "response.output_item.added" {
			items[gjson.Get(d, "item.type").String()] = gjson.Get(d, "output_index").Int()
		}
	}
	if created != 1 || strings.Count(body, `"type":"response.completed"`) != 1 {
		t.Fatalf("response.created %d: %s", created, body)
	}
	if r, ok := items["reasoning"]; !ok || items["message"] != r+1 {
		t.Fatalf("output items %v: %s", items, body)
	}
}

// With nobody behind it, the refusal after shown reasoning is the
// agent's, as before.
func TestRefusalAfterShownThinkingWithNoOneElse(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{200, "text/event-stream", glmThoughtThenSensitive}}}
	scriptedOn(t, "a", provider.Chat, a)
	refusalGroup(t, "a/m")
	s := New()
	code, body := sendTo(s, "/v1/chat/completions", chatStreamAsk)
	if code != 200 || a.n != 1 || !strings.Contains(body, "first thought") || !strings.Contains(body, `"finish_reason":"sensitive"`) {
		t.Fatalf("%d (a %d): %s", code, a.n, body)
	}
	if r := lastRoute(s); len(r.Tries) != 1 || r.Tries[0].Rest != nil {
		t.Fatalf("tries: %+v", r.Tries)
	}
}

// A refusal once the answer's text began is the agent's: what was said
// can't be taken back, and nobody else is asked.
func TestRefusalAfterThinkingAndTextStays(t *testing.T) {
	fresh(t)
	late := sse(
		`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"first thought"}}]}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"half"}}]}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"sensitive"}]}`,
		`data: [DONE]`)
	a := &scripted{replies: []reply{{200, "text/event-stream", late}}}
	b := &scripted{replies: []reply{{200, "text/event-stream", anthropicAnswer}}}
	scriptedOn(t, "a", provider.Chat, a)
	scriptedOn(t, "b", provider.Anthropic, b)
	refusalGroup(t, "a/m", "b/m")
	s := New()
	code, body := sendTo(s, "/v1/chat/completions", chatStreamAsk)
	if code != 200 || body != late || b.n != 0 {
		t.Fatalf("%d (b %d): %s", code, b.n, body)
	}
	if r := lastRoute(s); len(r.Tries) != 1 || r.Tries[0].Rest != nil {
		t.Fatalf("tries: %+v", r.Tries)
	}
}

// Two members in a row refused after their reasoning: the third answers,
// its block numbered after both.
func TestRefusalHandedOverTwice(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{200, "text/event-stream", glmAnthropicThoughtThenRefused}}}
	b := &scripted{replies: []reply{{200, "text/event-stream", glmAnthropicThoughtThenRefused}}}
	c := &scripted{replies: []reply{{200, "text/event-stream", anthropicAnswer}}}
	scriptedOn(t, "a", provider.Anthropic, a)
	scriptedOn(t, "b", provider.Anthropic, b)
	scriptedOn(t, "c", provider.Anthropic, c)
	refusalGroup(t, "a/m", "b/m", "c/m")
	s := New()
	code, body := sendTo(s, "/v1/messages", claudeCodeAsk)
	if code != 200 || a.n != 1 || b.n != 1 || c.n != 1 || !strings.Contains(body, "from b") || strings.Contains(body, "refusal") {
		t.Fatalf("%d (a %d, b %d, c %d): %s", code, a.n, b.n, c.n, body)
	}
	if strings.Count(body, `"type":"message_start"`) != 1 || !strings.Contains(body, `{"type":"content_block_start","index":2,"content_block":{"type":"text"`) {
		t.Fatalf("not one message of three blocks: %s", body)
	}
}

// The vendors' words for their filter refusing, beside OpenAI's and
// Anthropic's: Zhipu's, DashScope's and Volcengine Ark's.
func TestSensitiveContentRefusals(t *testing.T) {
	for _, raw := range []string{
		`{"error":{"code":"1301","message":"系统检测到输入或生成内容可能包含不安全或敏感内容"}}`,
		`{"error":{"code":"data_inspection_failed","type":"data_inspection_failed","message":"Input data may contain inappropriate content."}}`,
		`{"code":"DataInspectionFailed","message":"Output data may contain inappropriate content."}`,
		`{"error":{"code":"OutputTextSensitiveContentDetected","message":"The request failed because the output text may contain sensitive information."}}`,
		`{"error":{"code":"InputTextSensitiveContentDetected","message":"x"}}`,
	} {
		if _, ok := policyRefusal([]byte(raw)); !ok {
			t.Errorf("not a refusal: %s", raw)
		}
	}
	for _, raw := range []string{
		`{"error":{"code":"1301","message":"rate limited"}}`,
		`{"error":{"code":"1113","message":"余额不足"}}`,
	} {
		if _, ok := policyRefusal([]byte(raw)); ok {
			t.Errorf("a refusal: %s", raw)
		}
	}
	if stopFromChat("sensitive") != "filter" {
		t.Error("Zhipu's sensitive is not the filter's stop")
	}
}

// A group's Claude, GPT or Gemini member trusted with its reasoning
// (holdsThinking, #4166) lets it through as it comes, and what ends the
// try before a word of the answer still goes to the next member, as it
// did while the reasoning was held (#248): a refusal and a retryable
// error alike, whose answer follows the reasoning in the same stream, as
// one reply, for Claude Code, Codex and a Chat client. An error the next
// member wouldn't answer otherwise (a request at fault) is the agent's.
func TestTrustedThinkingFailsOverOnEveryPath(t *testing.T) {
	chatAnswered := sse(
		`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"hm"}}]}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"from a"}}]}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}}`,
		`data: [DONE]`)
	chatThought := strings.Join(strings.SplitAfter(glmThoughtThenSensitive, "\n\n")[:2], "")
	anthropicThought := strings.Join(strings.SplitAfter(glmAnthropicThoughtThenRefused, "\n\n")[:4], "")
	anthropicError := func(typ, msg string) string {
		return anthropicThought + sse(`event: error`+"\n"+`data: {"type":"error","error":{"type":"`+typ+`","message":"`+msg+`"}}`)
	}
	for _, up := range []struct {
		name, model string
		proto       provider.Protocol
		answered    string
		refused     string
		overloaded  string
		invalid     string
	}{
		{"claude", "claude-opus-5-5", provider.Anthropic, anthropicThoughtThenAnswered, glmAnthropicThoughtThenRefused,
			anthropicError("overloaded_error", "Overloaded"), anthropicError("invalid_request_error", "invalid tool schema")},
		{"gpt", "gpt-5.5", provider.Chat, chatAnswered, chatThoughtThenFiltered,
			chatThought + sse(`data: {"error":{"message":"Overloaded","type":"server_error"}}`),
			chatThought + sse(`data: {"error":{"message":"invalid tool schema","type":"invalid_request_error"}}`)},
		{"gemini", "gemini-3-pro", provider.Chat, chatAnswered, chatThoughtThenFiltered,
			chatThought + sse(`data: {"error":{"message":"Overloaded","type":"server_error"}}`),
			chatThought + sse(`data: {"error":{"message":"invalid tool schema","type":"invalid_request_error"}}`)},
	} {
		for _, end := range []string{"refused", "overloaded", "invalid"} {
			for _, client := range []struct {
				name, path, ask string
				check           func(t *testing.T, body string)
			}{
				{"messages", "/v1/messages", claudeCodeAsk, checkAnthropicSpliced},
				{"chat", "/v1/chat/completions", chatStreamAsk, checkChatSpliced},
				{"responses", "/v1/responses", codexAsk, checkResponsesSpliced},
			} {
				t.Run(up.name+" "+end+" to "+client.name, func(t *testing.T) {
					fresh(t)
					last := map[string]string{"refused": up.refused, "overloaded": up.overloaded, "invalid": up.invalid}[end]
					a := &scripted{replies: []reply{
						{200, "text/event-stream", up.answered},
						{200, "text/event-stream", up.answered},
						{200, "text/event-stream", up.answered},
						{200, "text/event-stream", last},
					}}
					scriptedOn(t, "a", up.proto, a)
					pa, err := provider.Find("a")
					if err != nil {
						t.Fatal(err)
					}
					pa.Models = []string{up.model}
					if err := provider.Save(*pa); err != nil {
						t.Fatal(err)
					}
					b := &scripted{replies: []reply{{200, "text/event-stream", anthropicAnswer}}}
					scriptedOn(t, "b", provider.Anthropic, b)
					refusalGroup(t, "a/"+up.model, "b/m")
					s := New()
					for i := range thoughtCleanMost {
						if code, body := sendTo(s, client.path, client.ask); code != 200 || !strings.Contains(body, "from a") {
							t.Fatalf("warm-up %d: %d %s (a %d)", i+1, code, body, a.n)
						}
					}
					code, body := sendTo(s, client.path, client.ask)
					// trusted: the reasoning went through as it came
					if code != 200 || a.n != thoughtCleanMost+1 || !strings.Contains(body, "first thought") {
						t.Fatalf("%d (a %d, b %d): %s", code, a.n, b.n, body)
					}
					r := lastRoute(s)
					if end == "invalid" {
						if b.n != 0 || !strings.Contains(body, "invalid tool schema") || len(r.Tries) != 1 {
							t.Fatalf("a request at fault isn't the agent's (b %d): %s %+v", b.n, body, r.Tries)
						}
						return
					}
					if b.n != 1 || !strings.Contains(body, "from b") {
						t.Fatalf("the next member didn't answer (b %d): %s", b.n, body)
					}
					for _, w := range []string{"content_filter", `"refusal"`, "Overloaded", "overloaded"} {
						if strings.Contains(body, w) {
							t.Fatalf("the failure (%s) reached the agent: %s", w, body)
						}
					}
					client.check(t, body)
					if len(r.Tries) != 2 || r.Tries[1].Fail != "" {
						t.Fatalf("tries: %+v", r.Tries)
					}
					if end == "refused" && (r.Tries[0].Fail != failRefused || r.Tries[0].Rest != nil) {
						t.Fatalf("refused try: %+v", r.Tries[0])
					}
					if end == "overloaded" && r.Tries[0].Status != 529 {
						t.Fatalf("overloaded try: %+v", r.Tries[0])
					}
				})
			}
		}
	}
}
