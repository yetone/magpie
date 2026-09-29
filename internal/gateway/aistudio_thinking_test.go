package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// AI Studio's Gemini thinks silently unless asked for its thoughts: Claude
// Desktop waited 20 s for the first word of 你好. A client asking to see the
// model think has them asked for, at its effort, instead of reasoning_effort
// (which Google won't take with them); one that turned thinking off gets the
// least Gemini thinks; other hosts are asked as before.
func TestAIStudioThinking(t *testing.T) {
	type want struct {
		effort string // reasoning_effort, "" for none
		config string // extra_body.google.thinking_config as JSON, "" for none
	}
	for _, c := range []struct {
		name, host, model string
		req               Request
		want              want
	}{
		{"high", aiStudioHost, "gemini-3.8-flash", Request{Thinking: true, Effort: "high"}, want{"", `{"include_thoughts":true,"thinking_level":"high"}`}},
		{"xhigh", aiStudioHost, "gemini-3.8-flash", Request{Thinking: true, Effort: "xhigh"}, want{"", `{"include_thoughts":true,"thinking_level":"high"}`}},
		{"low", aiStudioHost, "gemini-3.1-pro", Request{Thinking: true, Effort: "low"}, want{"", `{"include_thoughts":true,"thinking_level":"low"}`}},
		{"2.5 budget", aiStudioHost, "gemini-2.5-flash", Request{Thinking: true, Effort: "medium"}, want{"", `{"include_thoughts":true,"thinking_budget":8192}`}},
		{"no effort", aiStudioHost, "gemini-3.8-flash", Request{Thinking: true}, want{"", `{"include_thoughts":true}`}},
		{"off", aiStudioHost, "gemini-3.8-flash", Request{ThinkOff: true}, want{"minimal", ""}},
		{"effort unasked to show", aiStudioHost, "gemini-3.8-flash", Request{Effort: "medium"}, want{"medium", ""}},
		{"other host", "api.deepseek.com", "deepseek-chat", Request{Thinking: true, Effort: "high"}, want{"high", ""}},
		{"other host off", "api.deepseek.com", "deepseek-chat", Request{ThinkOff: true}, want{"", ""}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := c.req
			r.Messages = []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "你好"}}}}
			var out struct {
				Effort string `json:"reasoning_effort"`
				Extra  struct {
					Google struct {
						Config json.RawMessage `json:"thinking_config"`
					} `json:"google"`
				} `json:"extra_body"`
			}
			if err := json.Unmarshal(buildChat(&r, c.model, c.host, false), &out); err != nil {
				t.Fatal(err)
			}
			if out.Effort != c.want.effort {
				t.Errorf("reasoning_effort = %q, want %q", out.Effort, c.want.effort)
			}
			if got := string(out.Extra.Google.Config); got != c.want.config {
				t.Errorf("thinking_config = %s, want %s", got, c.want.config)
			}
		})
	}
}

// Asked for its thoughts, Gemini's OpenAI-compatible API may give them in
// the text as a leading <thought>…</thought>, the tags split anywhere across
// chunks: they are thinking, the rest the answer. Text that only looks like
// a tag's start, or has one later, is the answer as it came.
func TestChatThoughtTags(t *testing.T) {
	run := func(parts ...string) (think, text string, order []EventKind) {
		var d chatDecoder
		for i, p := range parts {
			delta := map[string]any{"content": p}
			ch := map[string]any{"index": 0, "delta": delta}
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
				default:
					return
				}
				if len(order) == 0 || order[len(order)-1] != ev.Kind {
					order = append(order, ev.Kind)
				}
			})
		}
		return
	}
	for _, c := range []struct {
		name        string
		parts       []string
		think, text string
	}{
		{"whole", []string{"<thought>Greeting.</thought>Hello"}, "Greeting.", "Hello"},
		{"split tags", []string{"<tho", "ught>Let me", " think</tho", "ught>\n\nHello", " there"}, "Let me think", "Hello there"},
		{"one byte a chunk", strings.Split("<thought>ab</thought>cd", ""), "ab", "cd"},
		{"leading newline", []string{"\n", "<thought>x</thought>", "y"}, "x", "y"},
		{"not a thought", []string{"<b>bold</b>"}, "", "<b>bold</b>"},
		{"a lone <", []string{"<"}, "", "<"},
		{"a tag later", []string{"Hi ", "<thought>no</thought>"}, "", "Hi <thought>no</thought>"},
		{"never closed", []string{"<thought>half", " a thought</th"}, "half a thought</th", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			think, text, order := run(c.parts...)
			if think != c.think || text != c.text {
				t.Errorf("think %q text %q, want %q %q", think, text, c.think, c.text)
			}
			if c.think != "" && c.text != "" && (len(order) != 2 || order[0] != KThink) {
				t.Errorf("order %v, want thinking then text", order)
			}
		})
	}
}

// Claude Desktop sees the thoughts as a thinking block ahead of the answer,
// the first of them the moment it comes.
func TestChatThoughtTagsToAnthropic(t *testing.T) {
	rec := httptest.NewRecorder()
	enc := encoder("anthropic", newSSEWriter(rec), &Request{Model: "m"})
	var d chatDecoder
	for _, c := range []string{
		`{"id":"x","choices":[{"index":0,"delta":{"role":"assistant","content":"<thought>Greeting"}}]}`,
		`{"id":"x","choices":[{"index":0,"delta":{"content":" the user.</thought>你好！"}}]}`,
		`{"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	} {
		d.decode(c, enc.event)
		if strings.Contains(c, "<thought>Greeting") && !strings.Contains(rec.Body.String(), `"thinking_delta"`) {
			t.Fatalf("the first thought not sent at once:\n%s", rec.Body.String())
		}
	}
	enc.finish()
	body := rec.Body.String()
	th, tx := strings.Index(body, `"type":"thinking"`), strings.Index(body, `"type":"text"`)
	if th < 0 || tx < 0 || th > tx {
		t.Fatalf("want a thinking block then a text block:\n%s", body)
	}
	if strings.Contains(body, "<thought>") || strings.Contains(body, "</thought>") {
		t.Fatalf("tags left in the reply:\n%s", body)
	}
}
