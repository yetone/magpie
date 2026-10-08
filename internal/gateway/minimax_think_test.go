package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// miniMaxStream is a reply as MiniMax's Chat API streams it (its
// OpenAI-compatible docs for MiniMax-M2): the thinking at the head of the
// text between <think> tags, the tags split across chunks, then the answer.
const miniMaxStream = `data: {"id":"0581e2a0","choices":[{"index":0,"delta":{"content":"<th","role":"assistant","name":"MiniMax AI","audio_content":""}}],"created":1759900000,"model":"MiniMax-M2.7","object":"chat.completion.chunk"}

data: {"id":"0581e2a0","choices":[{"index":0,"delta":{"content":"ink>\nThe user greets me","role":"assistant","name":"MiniMax AI","audio_content":""}}],"created":1759900000,"model":"MiniMax-M2.7","object":"chat.completion.chunk"}

data: {"id":"0581e2a0","choices":[{"index":0,"delta":{"content":". Reply briefly.\n</thi","role":"assistant","name":"MiniMax AI","audio_content":""}}],"created":1759900000,"model":"MiniMax-M2.7","object":"chat.completion.chunk"}

data: {"id":"0581e2a0","choices":[{"index":0,"delta":{"content":"nk>\n\nHello!","role":"assistant","name":"MiniMax AI","audio_content":""}}],"created":1759900000,"model":"MiniMax-M2.7","object":"chat.completion.chunk"}

data: {"id":"0581e2a0","choices":[{"finish_reason":"stop","index":0,"delta":{"content":"","role":"assistant","name":"MiniMax AI","audio_content":""}}],"created":1759900000,"model":"MiniMax-M2.7","object":"chat.completion.chunk","usage":{"total_tokens":40,"prompt_tokens":20,"completion_tokens":20}}

data: [DONE]

`

func miniMaxSetup(t *testing.T, f *fake) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "minimax", Name: "MiniMax", Key: "k", Models: []string{"MiniMax-M2.7"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
}

// Codex asks MiniMax on its Chat API, which gives the thinking in the text
// between <think> tags. Codex folds it only as a reasoning item, so the
// tags' content goes to Codex as reasoning and the answer alone as the
// message (pluo, #1267: the thinking read as the answer's text).
func TestMiniMaxThinkTagsReachCodexAsReasoning(t *testing.T) {
	f := &fake{t: t, reply: miniMaxStream}
	miniMaxSetup(t, f)
	code, body := post(t, "/v1/responses", `{"model":"minimax/MiniMax-M2.7","stream":true,"input":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("code %d: %s", code, body)
	}
	var reasoning, text string
	var order []string
	for _, ev := range events(body) {
		if ev["type"] != "response.output_item.done" {
			continue
		}
		item, _ := ev["item"].(map[string]any)
		order = append(order, item["type"].(string))
		b, _ := json.Marshal(item)
		switch item["type"] {
		case "reasoning":
			reasoning += string(b)
		case "message":
			text += string(b)
		}
	}
	if !strings.Contains(reasoning, "The user greets me. Reply briefly.") {
		t.Errorf("no reasoning item with the thinking; items %v:\n%s", order, body)
	}
	if !strings.Contains(text, `"Hello!"`) || strings.Contains(text, "think") || strings.Contains(text, "greets") {
		t.Errorf("message %s, want the answer alone", text)
	}
	if len(order) != 2 || order[0] != "reasoning" || order[1] != "message" {
		t.Errorf("items %v, want reasoning then message", order)
	}
}

// The thinking goes back to MiniMax where it came from, in the assistant's
// text between <think> tags, as MiniMax asks for its interleaved thinking;
// a relay serving MiniMax's models, or another model on Chat, is sent no
// thinking, as before.
func TestMiniMaxThinkingGoesBackInTags(t *testing.T) {
	r := &Request{Messages: []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}},
		{Role: "assistant", Parts: []Part{{Kind: Thinking, Text: "\nThe user greets me.\n"}, {Kind: Text, Text: "Hello!"}}},
		{Role: "user", Parts: []Part{{Kind: Text, Text: "again"}}},
	}}
	for _, c := range []struct{ model, host, want string }{
		{"MiniMax-M2.7", "api.minimax.io", "<think>\nThe user greets me.\n</think>\n\nHello!"},
		{"MiniMax-M2.7", "api.minimaxi.com", "<think>\nThe user greets me.\n</think>\n\nHello!"},
		{"minimax/minimax-m3", "openrouter.ai", "Hello!"},
		{"kimi-k3", "api.moonshot.cn", "Hello!"},
	} {
		var q struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.Unmarshal(buildChat(r, c.model, c.host, false), &q); err != nil {
			t.Fatal(err)
		}
		if got := q.Messages[1]["content"]; got != c.want {
			t.Errorf("%s at %s: assistant content %q, want %q", c.model, c.host, got, c.want)
		}
	}
}

// A <think> block at the head of a Chat reply is thinking however the tags
// fall across chunks; one later in the text, or a tag that only starts
// like it, is the answer as it came.
func TestChatThinkTags(t *testing.T) {
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
	for _, c := range []struct {
		name        string
		parts       []string
		think, text string
	}{
		{"whole", []string{"<think>Greeting.</think>\n\nHello"}, "Greeting.", "Hello"},
		{"one byte a chunk", strings.Split("<think>ab</think>cd", ""), "ab", "cd"},
		{"a thought's close doesn't end it", []string{"<think>a</thought>b</think>c"}, "a</thought>b", "c"},
		{"a think tag later", []string{"Hi <think>no</think>"}, "", "Hi <think>no</think>"},
		{"<thin is text", []string{"<thin", "g>"}, "", "<thing>"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if think, text := run(c.parts...); think != c.think || text != c.text {
				t.Errorf("think %q text %q, want %q %q", think, text, c.think, c.text)
			}
		})
	}
}
