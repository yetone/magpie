package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
		{"off at its lowest", aiStudioHost, "gemini-3.8-flash", Request{GeminiCompat: true, ThinkOff: true, OffLevel: "low"}, want{"low", ""}},
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

// aiStudioRefusal is AI Studio's 400 for a thinking level the model hasn't.
// The status and message are the literal bytes of its 400 for minimal on
// gemini-3.8-flash, captured 2026-10-06 at its OpenAI-compatible API; the
// capture kept only those two, so the envelope around them is Vertex AI's
// (vertexRefusal), and other levels are assumed to be refused in the same
// words.
func aiStudioRefusal(level string) string {
	return `[{"error":{"code":400,"message":"Thinking level ` + strings.ToUpper(level) + ` is not supported for this model. Please retry with other thinking level.","status":"INVALID_ARGUMENT"}}]`
}

// vertexRefusal is Vertex AI's 400 for minimal on gemini-3.8-flash, at its
// OpenAI-compatible API, as captured 2026-10-06.
const vertexRefusal = `[{
  "error": {
    "code": 400,
    "message": "Thinking level is unsupported: THINKING_LEVEL_MINIMAL",
    "status": "INVALID_ARGUMENT"
  }
}
]
`

// Google's 400 for a thinking level the model hasn't names that level, in
// AI Studio's words, Vertex AI's or generateContent's; Gemini's fields
// turned away, or a value refused in other words, isn't it.
func TestGeminiRefusedLevel(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{aiStudioRefusal("minimal"), "minimal"},
		{aiStudioRefusal("medium"), "medium"},
		{vertexRefusal, "minimal"},
		// gemini-3.1-pro-preview's message for thinking_level minimal at
		// Vertex AI, as captured 2026-10-06 up to its "Learn more" link
		{`{"error":{"code":400,"message":"Unable to submit request because thinking_level MINIMAL is not supported by this model.","status":"INVALID_ARGUMENT"}}`, "minimal"},
		{`{"error":{"message":"Invalid JSON payload received. Unknown name \"extra_body\": Cannot find field.","code":400}}`, ""},
		// a level named, not turned away
		{`{"error":{"code":400,"message":"thinking_level LOW cannot be set with thinking_budget","status":"INVALID_ARGUMENT"}}`, ""},
		{`{"error":{"message":"Command Code: Invalid option: expected one of \"low\"|\"medium\"|\"high\"|\"xhigh\"|\"max\""}}`, ""},
		{`{"error":{"message":"Unsupported value: 'high' is not supported with this model.","param":"text.verbosity","code":"unsupported_value"}}`, ""},
	} {
		if got := geminiRefusedLevel([]byte(c.body)); got != c.want {
			t.Errorf("geminiRefusedLevel(%s) = %q, want %q", c.body, got, c.want)
		}
	}
}

// geminiThinkingConfig is the extra_body.google.thinking_config a Chat
// request carries, or nil.
func geminiThinkingConfig(b map[string]any) map[string]any {
	extra, _ := b["extra_body"].(map[string]any)
	google, _ := extra["google"].(map[string]any)
	tc, _ := google["thinking_config"].(map[string]any)
	return tc
}

// geminiLevel is the thinking level a Chat request asks Gemini for, in
// thinking_config or as reasoning_effort.
func geminiLevel(b map[string]any) string {
	if l, ok := geminiThinkingConfig(b)["thinking_level"].(string); ok {
		return l
	}
	l, _ := b["reasoning_effort"].(string)
	return l
}

// geminiUpstream stands for Gemini's OpenAI-compatible API, as provider
// ai-studio serving models. A request refusal gives a 400 for, by its model
// and the level it asks, is answered with it; any other with 你好！, after a
// thought where the thoughts are asked for. It returns the requests sent to
// it so far.
func geminiUpstream(t *testing.T, models []string, refusal func(model, level string) string) func() []map[string]any {
	t.Helper()
	var mu sync.Mutex
	var bodies []map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		model, _ := b["model"].(string)
		if no := refusal(model, geminiLevel(b)); no != "" {
			w.Header().Set("Content-Type", "application/json; charset=UTF-8")
			w.WriteHeader(400)
			io.WriteString(w, no)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		content := "你好！"
		if geminiThinkingConfig(b)["include_thoughts"] == true {
			content = "<thought>The user says hi.</thought>你好！"
		}
		for _, c := range []string{
			`{"id":"x","model":` + strconv.Quote(model) + `,"choices":[{"index":0,"delta":{"role":"assistant","content":` + strconv.Quote(content) + `}}]}`,
			`{"id":"x","model":` + strconv.Quote(model) + `,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`,
		} {
			io.WriteString(w, "data: "+c+"\n\n")
		}
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "ai-studio", Name: "AI Studio", Key: "k", Models: models, Chat: up.URL}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(bodies)
	}
}

// how Claude Code turns reasoning off, and asks to see it at high
const geminiOff, geminiThink = `"thinking":{"type":"disabled"}`, `"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}`

// askGemini is Claude Code saying 你好 to provider ai-studio's model, with
// the thinking given.
func askGemini(s *Server, model, thinking string) (int, string) {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"ai-studio/`+model+`","max_tokens":32000,"stream":true,
		`+thinking+`,"messages":[{"role":"user","content":"你好"}]}`)))
	return rec.Code, rec.Body.String()
}

// Gemini 3 can't stop thinking, and one without minimal turns it away:
// gemini-3.8-flash, asked with reasoning off, answers AI Studio's
// OpenAI-compatible API with aiStudioRefusal's 400 and Vertex AI's with the
// one below. Each names thinking, and was taken for thinking_config turned
// away, so the provider wasn't asked for Gemini's thoughts again, for any
// model, until magpie restarted. Reasoning off goes at the model's lowest
// level instead: at once where its levels are known, as models.dev's list
// for AI Studio gives them, and after the refusal where they aren't; the
// thoughts are still asked for.
func TestGeminiThinkingOffAtItsLowest(t *testing.T) {
	for _, c := range []struct {
		name, refusal string
		known         bool
	}{
		{"levels known", aiStudioRefusal("minimal"), true},
		{"AI Studio, levels not known", aiStudioRefusal("minimal"), false},
		{"Vertex AI, levels not known", vertexRefusal, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			sent := geminiUpstream(t, []string{"gemini-3.8-flash"}, func(_, level string) string {
				if level == "minimal" {
					return c.refusal
				}
				return ""
			})
			if c.known {
				if err := provider.SetModelEfforts("ai-studio/gemini-3.8-flash", []string{"low", "medium", "high"}); err != nil {
					t.Fatal(err)
				}
			}
			s := New()
			ask := func(thinking string) string {
				code, out := askGemini(s, "gemini-3.8-flash", thinking)
				if code != 200 || !strings.Contains(out, "你好！") {
					t.Fatalf("status %d: %s", code, out)
				}
				return out
			}
			ask(geminiOff)
			var efforts []any
			for _, b := range sent() {
				if b["extra_body"] != nil {
					t.Fatalf("reasoning off asked for the thoughts: %v", b)
				}
				efforts = append(efforts, b["reasoning_effort"])
			}
			if want := []any{"minimal", "low"}; c.known && !slices.Equal(efforts, want[1:]) || !c.known && !slices.Equal(efforts, want) {
				t.Fatalf("reasoning off went at %v", efforts)
			}
			n := len(efforts)
			out := ask(geminiThink)
			bodies := sent()
			last := bodies[len(bodies)-1]
			if tc := geminiThinkingConfig(last); len(bodies) != n+1 || tc["include_thoughts"] != true || tc["thinking_level"] != "high" || last["reasoning_effort"] != nil {
				t.Fatalf("the next turn, thinking, was sent %v (%d requests)", last, len(bodies)-n)
			}
			if !strings.Contains(out, `"thinking_delta"`) || !strings.Contains(out, "The user says hi.") {
				t.Fatalf("no thoughts in the reply:\n%s", out)
			}
			ask(geminiOff)
			if bodies := sent(); len(bodies) != n+2 || bodies[len(bodies)-1]["reasoning_effort"] != "low" {
				t.Fatalf("reasoning off again went at %v (%d requests)", bodies[len(bodies)-1]["reasoning_effort"], len(bodies)-n-1)
			}
		})
	}
}

// Asked for Gemini's thoughts, Vertex AI's OpenAI-compatible API gives each
// of them in its own chunk, marked in the delta's extra_content rather than
// with AI Studio's <thought> tags: these are its bytes, through a proxy on
// this machine. The thoughts are a thinking block, the rest the answer.
func TestGeminiThoughtsMarkedInExtraContent(t *testing.T) {
	fresh(t)
	sse, err := os.ReadFile("testdata/vertex_openai_thoughts.sse")
	if err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write(sse)
	}))
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "vertex-proxy", Name: "Vertex proxy", Key: "k", Models: []string{"gemini-3.8-flash"}, Chat: up.URL}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"vertex-proxy/gemini-3.8-flash","max_tokens":32000,"stream":true,
		"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"你好"}]}`))
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var think, text string
	var blocks []string
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		var ev struct {
			Type         string `json:"type"`
			ContentBlock struct {
				Type string `json:"type"`
			} `json:"content_block"`
			Delta struct {
				Thinking string `json:"thinking"`
				Text     string `json:"text"`
			} `json:"delta"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) != nil {
			continue
		}
		if ev.Type == "content_block_start" {
			blocks = append(blocks, ev.ContentBlock.Type)
		}
		think += ev.Delta.Thinking
		text += ev.Delta.Text
	}
	if !strings.HasPrefix(think, "**Acknowledging Greeting**") || text != "你好！很高兴与你交流。请问有什么我可以帮你的吗？" {
		t.Fatalf("thinking %q, text %q", think, text)
	}
	if len(blocks) != 2 || blocks[0] != "thinking" || blocks[1] != "text" {
		t.Fatalf("blocks %v, want thinking then text", blocks)
	}
}

// Gemini 3 turns away any level the model hasn't, not minimal alone:
// gemini-3-pro-image-preview has low and high, and is taken here to answer
// medium as AI Studio answers minimal on gemini-3.8-flash. That isn't
// thinking_config turned away either. The model is asked again at the
// nearest level it has left, still for its thoughts, and at that level from
// then on, while the provider's other models are asked as before. Where no
// level is left, Google's 400 goes back to the client, each level asked
// once.
func TestGeminiThinkingLevelRefused(t *testing.T) {
	t.Run("medium", func(t *testing.T) {
		fresh(t)
		sent := geminiUpstream(t, []string{"gemini-3-pro-image-preview", "gemini-3.8-flash"}, func(model, level string) string {
			if model == "gemini-3-pro-image-preview" && level == "medium" {
				return aiStudioRefusal(level)
			}
			return ""
		})
		s := New()
		const medium = `"thinking":{"type":"adaptive"},"output_config":{"effort":"medium"}`
		for _, c := range []struct {
			model string
			want  []string
		}{
			{"gemini-3-pro-image-preview", []string{"medium", "high"}},
			{"gemini-3-pro-image-preview", []string{"high"}},
			{"gemini-3.8-flash", []string{"medium"}},
		} {
			n := len(sent())
			code, out := askGemini(s, c.model, medium)
			var levels []string
			for _, b := range sent()[n:] {
				if geminiThinkingConfig(b)["include_thoughts"] != true {
					t.Fatalf("%s: the thoughts weren't asked for: %v", c.model, b)
				}
				levels = append(levels, geminiLevel(b))
			}
			if code != 200 || !strings.Contains(out, "The user says hi.") || !slices.Equal(levels, c.want) {
				t.Fatalf("%s: %d at %v: %s", c.model, code, levels, out)
			}
		}
	})
	t.Run("no level left", func(t *testing.T) {
		fresh(t)
		var asked atomic.Int32
		sent := geminiUpstream(t, []string{"gemini-3-pro-image-preview", "gemini-3.8-flash"}, func(model, level string) string {
			if model != "gemini-3-pro-image-preview" || level == "" {
				return ""
			}
			if asked.Add(1) > 8 {
				// a regression asking again and again stops here
				return `{"error":{"message":"asked again and again"}}`
			}
			return aiStudioRefusal(level)
		})
		if err := provider.SetModelEfforts("ai-studio/gemini-3-pro-image-preview", []string{"low", "high"}); err != nil {
			t.Fatal(err)
		}
		s := New()
		code, out := askGemini(s, "gemini-3-pro-image-preview", geminiOff)
		var levels []string
		for _, b := range sent() {
			levels = append(levels, geminiLevel(b))
		}
		// its own levels first, then Gemini 3's others; minimal, asked
		// again with none left, goes back as it came
		if want := []string{"low", "high", "minimal", "medium", "minimal"}; code != 400 || !strings.Contains(out, "MINIMAL is not supported for this model") || !slices.Equal(levels, want) {
			t.Fatalf("%d at %v: %s", code, levels, out)
		}
		if code, out := askGemini(s, "gemini-3.8-flash", geminiThink); code != 200 || !strings.Contains(out, "The user says hi.") {
			t.Fatalf("the provider's thoughts no longer asked for: %d %s", code, out)
		}
	})
}

// Claude Code asks more than one thing at once (the turn, a title, its
// auto mode classifier), and each went to gemini-3.8-flash at minimal
// before Google's 400 for it was remembered: the one turned away after it
// was took the 400 for thinking_config turned away, and the provider wasn't
// asked for Gemini's thoughts again. Each is asked again at low, and the
// thoughts are still asked for.
func TestGeminiMinimalRefusedTwiceAtOnce(t *testing.T) {
	fresh(t)
	var minimal atomic.Int32
	second, retried := make(chan struct{}), make(chan struct{})
	var once sync.Once
	wait := func(c chan struct{}) {
		select {
		case <-c:
		case <-time.After(5 * time.Second):
		}
	}
	sent := geminiUpstream(t, []string{"gemini-3.8-flash"}, func(_, level string) string {
		switch level {
		case "minimal":
			switch minimal.Add(1) {
			case 1: // turned away once the second is in
				wait(second)
			case 2: // and the second once the first was asked again
				close(second)
				wait(retried)
			}
			return aiStudioRefusal(level)
		case "low":
			once.Do(func() { close(retried) })
		}
		return ""
	})
	s := New()
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if code, out := askGemini(s, "gemini-3.8-flash", geminiOff); code != 200 {
				t.Errorf("status %d: %s", code, out)
			}
		})
	}
	wg.Wait()
	var levels []string
	for _, b := range sent() {
		levels = append(levels, geminiLevel(b))
	}
	slices.Sort(levels)
	if !slices.Equal(levels, []string{"low", "low", "minimal", "minimal"}) {
		t.Fatalf("reasoning off went at %v", levels)
	}
	code, out := askGemini(s, "gemini-3.8-flash", geminiThink)
	bodies := sent()
	if last := bodies[len(bodies)-1]; code != 200 || len(bodies) != 5 || geminiThinkingConfig(last)["include_thoughts"] != true || !strings.Contains(out, "The user says hi.") {
		t.Fatalf("the next turn, thinking, was sent %v: %d %s", last, code, out)
	}
}

// A Chat client's own reasoning_effort goes to Gemini as it was sent, and
// gemini-3.8-flash answers minimal with AI Studio's 400: asked again at
// low, and at low from then on, while none, which AI Studio takes from it,
// still goes as sent, and a model that has minimal still gets it. Either
// API's refusal holds for the other: Claude Code's reasoning off then goes
// to gemini-3.8-flash at low at once, and gemini-3.7-flash, which turned it
// away at minimal, is asked a Chat client's minimal at low at once.
func TestPassthroughGeminiMinimalRefused(t *testing.T) {
	fresh(t)
	sent := geminiUpstream(t, []string{"gemini-3.8-flash", "gemini-3.5-flash", "gemini-3.7-flash"}, func(model, level string) string {
		if model != "gemini-3.5-flash" && level == "minimal" {
			return aiStudioRefusal(level)
		}
		return ""
	})
	s := New()
	for _, c := range []struct {
		model, effort string // effort "" is Claude Code turning reasoning off
		want          []string
	}{
		{"gemini-3.8-flash", "minimal", []string{"minimal", "low"}},
		{"gemini-3.8-flash", "minimal", []string{"low"}},
		{"gemini-3.8-flash", "none", []string{"none"}},
		{"gemini-3.8-flash", "", []string{"low"}},
		{"gemini-3.5-flash", "minimal", []string{"minimal"}},
		{"gemini-3.7-flash", "", []string{"minimal", "low"}},
		{"gemini-3.7-flash", "minimal", []string{"low"}},
	} {
		n := len(sent())
		var code int
		var out string
		if c.effort == "" {
			code, out = askGemini(s, c.model, geminiOff)
		} else {
			code, out = postTo(t, s, "/v1/chat/completions", `{"model":"ai-studio/`+c.model+`","stream":true,"reasoning_effort":"`+c.effort+`","messages":[{"role":"user","content":"你好"}]}`)
		}
		var levels []string
		for _, b := range sent()[n:] {
			levels = append(levels, geminiLevel(b))
		}
		if code != 200 || !strings.Contains(out, "你好！") || !slices.Equal(levels, c.want) {
			t.Fatalf("%s at %q: %d at %v: %s", c.model, c.effort, code, levels, out)
		}
	}
}
