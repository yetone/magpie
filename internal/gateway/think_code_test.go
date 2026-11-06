package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The reply in Kane-Kuroneko's #2634, as its bytes were: thinking that
// writes the close tag out as an inline-code example, then the answer.
const exampleClose = "<think>开始分析。使用 `<think>...</think>` 标签。\n继续分析。</think>\n最终正文。"

// The boundary #2634 saw from Qoder's plugin: the answer began at the
// backtick after the first </think>, and the reasoning was "...\n".
const qoderBoundary = "<think>...\n</think>` 标签或 JSON 格式，Qoder 插件将其转为 OpenAI `tool_calls`"

// chatChunks is a Chat stream giving text one Unicode character a delta,
// each reasoning piece before it in reasoning_content.
func chatChunks(text string, reasoning ...string) string {
	var lines []string
	add := func(delta map[string]any, finish any) {
		b, _ := json.Marshal(map[string]any{"id": "c1", "object": "chat.completion.chunk", "model": "m1",
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
		lines = append(lines, "data: "+string(b))
	}
	for _, r := range reasoning {
		add(map[string]any{"role": "assistant", "reasoning_content": r}, nil)
	}
	for _, r := range text {
		add(map[string]any{"role": "assistant", "content": string(r)}, nil)
	}
	add(map[string]any{}, "stop")
	lines = append(lines, `data: {"id":"c1","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":5}}`, "data: [DONE]")
	return sse(lines...)
}

// A <think> example written in Markdown code, inline or fenced, is the
// thinking's words; only the close outside code ends it, however the text
// falls across chunks.
func TestChatThinkCloseInCode(t *testing.T) {
	run := func(parts ...string) (think, text string) {
		var d chatDecoder
		for i, p := range parts {
			ch := map[string]any{"index": 0, "delta": map[string]any{"content": p}}
			if i == len(parts)-1 {
				ch["finish_reason"] = "stop"
			}
			b, _ := json.Marshal(map[string]any{"id": "x", "choices": []any{ch}})
			d.decode(string(b), func(ev Event) {
				switch ev.Kind {
				case KThink:
					think += ev.Text
				case KText:
					text += ev.Text
				}
			})
		}
		return
	}
	runes := func(s string) []string {
		var out []string
		for _, r := range s {
			out = append(out, string(r))
		}
		return out
	}
	want := "开始分析。使用 `<think>...</think>` 标签。\n继续分析。"
	for _, c := range []struct {
		name        string
		parts       []string
		think, text string
	}{
		{"#2634 whole", []string{exampleClose}, want, "最终正文。"},
		{"#2634 a character a chunk", runes(exampleClose), want, "最终正文。"},
		{"#2634 a byte a chunk", strings.Split(exampleClose, ""), want, "最终正文。"},
		{"a double-backtick span", []string{"<think>a ``x`</think>`` b</think>c"}, "a ``x`</think>`` b", "c"},
		{"a fenced block", runes("<think>like\n```xml\n</think>\n```\nso</think>\nok"), "like\n```xml\n</think>\n```\nso", "ok"},
		{"a span ends with its line", []string{"<think>a `b\n</think>\nok"}, "a `b\n", "ok"},
		{"backticks after the close", runes("<think>a</think>\n`x` y"), "a", "`x` y"},
		{"a close tag split by a chunk", []string{"<think>a `", "x` b</th", "ink>c"}, "a `x` b", "c"},
		{"backticks held at the end", []string{"<think>a ``"}, "a ``", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if think, text := run(c.parts...); think != c.think || text != c.text {
				t.Errorf("think %q text %q, want %q %q", think, text, c.think, c.text)
			}
		})
	}
}

// A reply whose thinking came in reasoning_content gives none in its text:
// a <think> there is what the model said (#2634).
func TestChatThinkTagAfterReasoningField(t *testing.T) {
	var d chatDecoder
	var think, text string
	readSSE(strings.NewReader(chatChunks(qoderBoundary, "先想一想。")), func(_, data string) error {
		return d.decode(data, func(ev Event) {
			switch ev.Kind {
			case KThink:
				think += ev.Text
			case KText:
				text += ev.Text
			}
		})
	})
	if think != "先想一想。" || text != qoderBoundary {
		t.Errorf("think %q text %q, want the reasoning field's and the text as it came", think, text)
	}
}

// #2634 through each route that reads a built-in Chat upstream's text:
// Codex's Responses and Claude Code's Messages get the example's close in
// the reasoning and the answer whole.
func TestThinkExampleReachesClientsWhole(t *testing.T) {
	f := &fake{t: t, reply: chatChunks(exampleClose)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1/responses", `{"model":"fake/m1","stream":true,"input":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("responses: %d %s", code, body)
	}
	reasoning, text := thinkAndText(body)
	if !strings.Contains(reasoning, "`<think>...</think>` 标签。\n继续分析。") || text != "最终正文。" {
		t.Errorf("responses: reasoning %q, message %q", reasoning, text)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"fake/m1","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("anthropic-version", "2023-06-01")
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("messages: %d %s", rec.Code, rec.Body)
	}
	var thinking, said string
	for _, ev := range events(rec.Body.String()) {
		if ev["type"] != "content_block_delta" {
			continue
		}
		d, _ := ev["delta"].(map[string]any)
		if s, ok := d["thinking"].(string); ok {
			thinking += s
		}
		if s, ok := d["text"].(string); ok {
			said += s
		}
	}
	if !strings.Contains(thinking, "`<think>...</think>` 标签。\n继续分析。") || said != "最终正文。" {
		t.Errorf("messages: thinking %q, text %q", thinking, said)
	}
}

// thinkAndText is a Responses stream's reasoning summary and message
// text, from its finished items.
func thinkAndText(body string) (reasoning, text string) {
	for _, ev := range events(body) {
		if ev["type"] != "response.output_item.done" {
			continue
		}
		item, _ := ev["item"].(map[string]any)
		key, parts := "text", item["content"]
		if item["type"] == "reasoning" {
			parts = item["summary"]
		}
		list, _ := parts.([]any)
		for _, p := range list {
			m, _ := p.(map[string]any)
			s, _ := m[key].(string)
			if item["type"] == "reasoning" {
				reasoning += s
			} else {
				text += s
			}
		}
	}
	return
}

// A plugin's Chat reply reaches Codex as OpenCode would show it: Qoder's
// plugin gives its reasoning apart, so text that begins with <think> is
// the answer, all of it, never cut at the first </think> (#2634).
func TestPluginThinkTagIsText(t *testing.T) {
	pid := besideFake(t, "qoder", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, chatChunks(qoderBoundary))
	}))
	code, body := post(t, "/v1/responses", `{"model":"`+pid+`/fake-1","stream":true,"input":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if reasoning, text := thinkAndText(body); reasoning != "" || text != qoderBoundary {
		t.Errorf("reasoning %q, message %q; want the text as the plugin gave it", reasoning, text)
	}
}

// A title, description or prediction Codex asked for loses a leading
// thinking block the same way: up to the close outside code.
func TestAfterThoughtCloseInCode(t *testing.T) {
	if got := afterThought(exampleClose); got != "最终正文。" {
		t.Errorf("afterThought = %q", got)
	}
	if got := descriptionJSON("<think>`</think>` is a tag</think>\nFix the parser"); !strings.Contains(got, "Fix the parser") || strings.Contains(got, "is a tag") {
		t.Errorf("descriptionJSON = %q", got)
	}
	if got := afterThought("<think>never closed"); got != "<think>never closed" {
		t.Errorf("afterThought of an open block = %q", got)
	}
	if got := afterThought("\r\n<think>a</think>\r\nb"); got != "b" {
		t.Errorf("afterThought after a line end = %q", got)
	}
	// the other tags a streamed reply's thinking is read between (#5106)
	for _, c := range []string{"<thinking>a</thinking>\nb", "<thought>a</thought>b", " <thinking>\na\n</thinking>\n\nb"} {
		if got := afterThought(c); got != "b" {
			t.Errorf("afterThought(%q) = %q, want b", c, got)
		}
	}
	if got := afterThought("<thinking>never closed"); got != "<thinking>never closed" {
		t.Errorf("an unclosed <thinking> = %q, want it as it was", got)
	}
}
