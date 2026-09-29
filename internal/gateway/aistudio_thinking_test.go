package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
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
		{"high", aiStudioHost, "gemini-3.8-flash", Request{GeminiCompat: true, Thinking: true, Effort: "high"}, want{"", `{"include_thoughts":true,"thinking_level":"high"}`}},
		{"xhigh", aiStudioHost, "gemini-3.8-flash", Request{GeminiCompat: true, Thinking: true, Effort: "xhigh"}, want{"", `{"include_thoughts":true,"thinking_level":"high"}`}},
		{"low", aiStudioHost, "gemini-3.1-pro", Request{GeminiCompat: true, Thinking: true, Effort: "low"}, want{"", `{"include_thoughts":true,"thinking_level":"low"}`}},
		{"2.5 budget", aiStudioHost, "gemini-2.5-flash", Request{GeminiCompat: true, Thinking: true, Effort: "medium"}, want{"", `{"include_thoughts":true,"thinking_budget":8192}`}},
		{"no effort", aiStudioHost, "gemini-3.8-flash", Request{GeminiCompat: true, Thinking: true}, want{"", `{"include_thoughts":true}`}},
		{"off", aiStudioHost, "gemini-3.8-flash", Request{GeminiCompat: true, ThinkOff: true}, want{"minimal", ""}},
		{"effort unasked to show", aiStudioHost, "gemini-3.8-flash", Request{GeminiCompat: true, Effort: "medium"}, want{"medium", ""}},
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

// Gemini's OpenAI-compatible API is AI Studio's own, or a Gemini model
// behind a proxy on this machine or the LAN (X @saoyan25's local proxy in
// front of AI Studio: no thoughts came); a relay elsewhere, or another
// model locally, is not taken for it.
func TestGeminiCompat(t *testing.T) {
	for _, c := range []struct {
		host, model string
		want        bool
	}{
		{aiStudioHost, "gemini-3.8-flash", true},
		{aiStudioHost, "gemma-4", true},
		{"127.0.0.1:8045", "gemini-3.8-flash", true},
		{"localhost:3000", "models/Gemini-3.1-Pro", true},
		{"192.168.1.20:8080", "gemini-2.5-flash", true},
		{"[::1]:9000", "gemini-3.8-flash", true},
		{"mac-mini.local", "gemini-3.8-flash", true},
		{"127.0.0.1:11434", "qwen3", false},
		{"openrouter.ai", "google/gemini-3.8-flash", false},
		{"aihubmix.com", "gemini-3.8-flash", false},
	} {
		if got := geminiCompat(c.host, c.model); got != c.want {
			t.Errorf("geminiCompat(%q, %q) = %v, want %v", c.host, c.model, got, c.want)
		}
	}
}

// Claude Desktop through magpie to a local proxy in front of AI Studio:
// the proxy is asked for Gemini's thoughts and the client sees them as a
// thinking block. A proxy that turns the fields away is asked again as
// before, with reasoning_effort, and not sent them again.
func TestGeminiThoughtsThroughLocalProxy(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		name := "passed on"
		if refuse {
			name = "refused"
		}
		t.Run(name, func(t *testing.T) {
			fresh(t)
			var bodies []map[string]any
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var b map[string]any
				json.NewDecoder(r.Body).Decode(&b)
				bodies = append(bodies, b)
				if _, ok := b["extra_body"]; ok && refuse {
					w.WriteHeader(400)
					io.WriteString(w, `{"error":{"message":"Invalid JSON payload received. Unknown name \"extra_body\": Cannot find field.","code":400}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				content := "<thought>The user says hi.</thought>你好！"
				if _, ok := b["extra_body"]; !ok {
					content = "你好！"
				}
				for _, c := range []string{
					`{"id":"x","model":"gemini-3.8-flash","choices":[{"index":0,"delta":{"role":"assistant","content":` + strconv.Quote(content) + `}}]}`,
					`{"id":"x","model":"gemini-3.8-flash","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`,
				} {
					io.WriteString(w, "data: "+c+"\n\n")
				}
				io.WriteString(w, "data: [DONE]\n\n")
			}))
			t.Cleanup(up.Close)
			p := provider.Provider{ID: "ai-studio", Name: "AI Studio", Key: "k", Models: []string{"gemini-3.8-flash"}, Chat: up.URL}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			s := New()
			ask := func() string {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"ai-studio/gemini-3.8-flash","max_tokens":32000,"stream":true,
					"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"你好"}]}`))
				s.Handler().ServeHTTP(rec, req)
				if rec.Code != 200 {
					t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
				}
				return rec.Body.String()
			}
			out := ask()
			first := bodies[0]
			tc, _ := first["extra_body"].(map[string]any)["google"].(map[string]any)["thinking_config"].(map[string]any)
			if tc["include_thoughts"] != true || tc["thinking_level"] != "high" || first["reasoning_effort"] != nil {
				t.Fatalf("first request asked %v, reasoning_effort %v", first["extra_body"], first["reasoning_effort"])
			}
			if !refuse {
				if len(bodies) != 1 || !strings.Contains(out, `"thinking_delta"`) || !strings.Contains(out, "The user says hi.") || strings.Contains(out, "<thought>") {
					t.Fatalf("%d requests; reply:\n%s", len(bodies), out)
				}
				return
			}
			if len(bodies) != 2 || bodies[1]["extra_body"] != nil || bodies[1]["reasoning_effort"] != "high" {
				t.Fatalf("after the refusal: %d requests, the last %v", len(bodies), bodies[len(bodies)-1])
			}
			if !strings.Contains(out, "你好！") {
				t.Fatalf("reply:\n%s", out)
			}
			ask()
			if len(bodies) != 3 || bodies[2]["extra_body"] != nil || bodies[2]["reasoning_effort"] != "high" {
				t.Fatalf("the next turn was sent %v (%d requests)", bodies[len(bodies)-1], len(bodies))
			}
		})
	}
}
