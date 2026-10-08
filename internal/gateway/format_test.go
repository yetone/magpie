package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// calendarSchema is the schema the OpenAI SDK sends for the docs'
// CalendarEvent model: strict, every property required, nothing else
// allowed.
const calendarSchema = `{"type":"object","properties":{"name":{"title":"Name","type":"string"},"date":{"title":"Date","type":"string"},"participants":{"items":{"type":"string"},"title":"Participants","type":"array"}},"required":["name","date","participants"],"title":"CalendarEvent","additionalProperties":false}`

const calendarAnswer = `{"name":"Science Fair","date":"Friday","participants":["Alice","Bob"]}`

// sdkResponsesParse is the body the OpenAI Python SDK's
// client.responses.parse(text_format=CalendarEvent) posts (frostming on
// Discord: through magpie the answer came back unstructured).
const sdkResponsesParse = `{"input":[{"role":"system","content":"Extract the event information."},{"role":"user","content":"Alice and Bob are going to a science fair on Friday."}],"model":"%s","text":{"format":{"type":"json_schema","strict":true,"name":"CalendarEvent","schema":` + calendarSchema + `}}}`

// sdkChatParse is the body client.chat.completions.parse(response_format=
// CalendarEvent) posts.
const sdkChatParse = `{"messages":[{"role":"system","content":"Extract the event information."},{"role":"user","content":"Alice and Bob are going to a science fair on Friday."}],"model":"%s","response_format":{"type":"json_schema","json_schema":{"schema":` + calendarSchema + `,"name":"CalendarEvent","strict":true}},"stream":false}`

// formatVendor serves Chat, Responses and Messages, answering every request
// with calendarAnswer, and keeps what each was sent. refuse makes its Chat
// endpoint turn away a json_schema format as DeepSeek's does.
type formatVendor struct {
	mu     sync.Mutex
	bodies []map[string]any
	paths  []string
	refuse bool
}

func (v *formatVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	json.Unmarshal(b, &m)
	v.mu.Lock()
	v.bodies = append(v.bodies, m)
	v.paths = append(v.paths, r.URL.Path)
	refuse := v.refuse
	v.mu.Unlock()
	text, _ := json.Marshal(calendarAnswer)
	w.Header().Set("Content-Type", "text/event-stream")
	switch {
	case strings.HasSuffix(r.URL.Path, "/chat/completions"):
		if rf, _ := m["response_format"].(map[string]any); refuse && rf["type"] == "json_schema" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"This response_format type is unavailable now","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}`)
			return
		}
		io.WriteString(w, sse(`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":`+string(text)+`}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":5,"total_tokens":10}}`,
			`data: [DONE]`))
	case strings.HasSuffix(r.URL.Path, "/responses"):
		io.WriteString(w, sse(`data: {"type":"response.output_text.delta","delta":`+string(text)+`}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":`+string(text)+`}]}}`,
			`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`))
	default:
		io.WriteString(w, sse(`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":`+string(text)+`}}`,
			`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
			`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			`event: message_stop`+"\n"+`data: {"type":"message_stop"}`))
	}
}

func (v *formatVendor) sent() (string, map[string]any) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.bodies) == 0 {
		return "", nil
	}
	return v.paths[len(v.paths)-1], v.bodies[len(v.bodies)-1]
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// sameFormat is whether a and b are the same JSON value, whatever their keys'
// order.
func sameFormat(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal([]byte(a), &x); err != nil {
		t.Fatalf("%v: %s", err, a)
	}
	if err := json.Unmarshal([]byte(b), &y); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return jsonOf(x) == jsonOf(y)
}

// A client's structured output reaches the upstream in the upstream's own
// words, whichever API the client spoke and the upstream speaks
// (frostming: a Responses request's text.format to OpenRouter, which
// magpie speaks Chat to, came back as plain text).
func TestStructuredOutputCrossesEveryAPI(t *testing.T) {
	fresh(t)
	up := &formatVendor{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	// OpenRouter's shape: Chat, and Messages for Claude
	if err := provider.Save(provider.Provider{ID: "router", Name: "Router", Key: "k", Chat: srv.URL + "/api/v1", Anthropic: srv.URL + "/api",
		Models: []string{"openai/gpt-4o-mini", "anthropic/claude-sonnet-4.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "resp", Name: "Resp", Key: "k", Responses: srv.URL + "/v1", Models: []string{"gpt-5"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "chat", Name: "Chat", Key: "k", Chat: srv.URL + "/v1", Models: []string{"gpt-4o-mini"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "ant", Name: "Ant", Key: "k", Anthropic: srv.URL, Models: []string{"claude-sonnet-4-5"}}); err != nil {
		t.Fatal(err)
	}
	chatSchema := `{"type":"json_schema","json_schema":{"name":"CalendarEvent","schema":` + calendarSchema + `,"strict":true}}`
	respSchema := `{"type":"json_schema","name":"CalendarEvent","schema":` + calendarSchema + `,"strict":true}`
	antSchema := `{"type":"json_schema","schema":` + calendarSchema + `}`
	anthropicBody := `{"model":"%s","max_tokens":1024,"messages":[{"role":"user","content":"Alice and Bob are going to a science fair on Friday."}],"output_config":{"format":{"type":"json_schema","schema":` + calendarSchema + `}}}`
	for _, c := range []struct {
		name, path, body, model string
		wantPath                string // the upstream endpoint
		field                   string // where the upstream reads the format
		want                    string // what it says there; "" for nothing
		system                  string // what the system prompt has to say, if anything
	}{
		{"responses.parse to a Chat upstream (frostming)", "/v1/responses", sdkResponsesParse, "router/openai/gpt-4o-mini",
			"/api/v1/chat/completions", "response_format", chatSchema, ""},
		{"responses json_object to a Chat upstream", "/v1/responses",
			`{"model":"%s","input":"List three colors as JSON.","text":{"format":{"type":"json_object"},"verbosity":"low"}}`, "router/openai/gpt-4o-mini",
			"/api/v1/chat/completions", "response_format", `{"type":"json_object"}`, ""},
		{"responses.parse to Messages", "/v1/responses", sdkResponsesParse, "ant/claude-sonnet-4-5",
			"/v1/messages", "output_config.format", antSchema, ""},
		{"responses.parse to a Claude model on OpenRouter", "/v1/responses", sdkResponsesParse, "router/anthropic/claude-sonnet-4.5",
			"/api/v1/messages", "output_config.format", antSchema, ""},
		{"chat.completions.parse to a Responses upstream", "/v1/chat/completions", sdkChatParse, "resp/gpt-5",
			"/v1/responses", "text.format", respSchema, ""},
		{"chat.completions.parse to Messages", "/v1/chat/completions", sdkChatParse, "ant/claude-sonnet-4-5",
			"/v1/messages", "output_config.format", antSchema, ""},
		{"chat json_object to Messages, which takes a schema only", "/v1/chat/completions",
			`{"model":"%s","messages":[{"role":"user","content":"List three colors as JSON."}],"response_format":{"type":"json_object"}}`, "ant/claude-sonnet-4-5",
			"/v1/messages", "output_config.format", "", "Respond with a single JSON object and nothing else."},
		{"Anthropic's schema to a Chat upstream", "/v1/messages", anthropicBody, "chat/gpt-4o-mini",
			"/v1/chat/completions", "response_format", `{"type":"json_schema","json_schema":{"name":"response","schema":` + calendarSchema + `}}`, ""},
		{"Anthropic's schema to a Responses upstream", "/v1/messages", anthropicBody, "resp/gpt-5",
			"/v1/responses", "text.format", `{"type":"json_schema","name":"response","schema":` + calendarSchema + `}`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, out := post(t, c.path, strings.Replace(c.body, "%s", c.model, 1))
			if code != 200 {
				t.Fatalf("status %d: %s", code, out)
			}
			path, sent := up.sent()
			if path != c.wantPath {
				t.Fatalf("went to %s, want %s", path, c.wantPath)
			}
			var got any = sent
			for _, k := range strings.Split(c.field, ".") {
				m, _ := got.(map[string]any)
				got = m[k]
			}
			if c.want == "" {
				if got != nil {
					t.Errorf("%s = %s, want none", c.field, jsonOf(got))
				}
			} else if got == nil || !sameFormat(t, jsonOf(got), c.want) {
				t.Errorf("%s = %s\nwant %s\nsent %s", c.field, jsonOf(got), c.want, jsonOf(sent))
			}
			if c.system != "" && !strings.Contains(jsonOf(sent["system"]), c.system) {
				t.Errorf("system = %s, want it to say %q", jsonOf(sent["system"]), c.system)
			}
			// the answer reaches the client as the JSON it asked for
			if !strings.Contains(out, `\"participants\":[\"Alice\",\"Bob\"]`) {
				t.Errorf("reply has no answer: %s", out)
			}
		})
	}
}

// A Responses client's verbosity goes on beside its format to a Responses
// upstream, the format said once.
func TestResponsesTextKeepsVerbosityBesideFormat(t *testing.T) {
	r, err := parseResponses([]byte(`{"model":"gpt-5","input":"hi","text":{"verbosity":"low","format":{"type":"json_schema","name":"CalendarEvent","strict":true,"schema":` + calendarSchema + `}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Format == nil || r.Format.Name != "CalendarEvent" || r.Format.Strict == nil || !*r.Format.Strict {
		t.Fatalf("format = %+v", r.Format)
	}
	var built struct {
		Text map[string]json.RawMessage `json:"text"`
	}
	json.Unmarshal(buildResponses(r, "gpt-5", "api.openai.com", false), &built)
	if string(built.Text["verbosity"]) != `"low"` || !sameFormat(t, string(built.Text["format"]), `{"type":"json_schema","name":"CalendarEvent","strict":true,"schema":`+calendarSchema+`}`) {
		t.Fatalf("text = %v", built.Text)
	}
	// plain text is the default, and stays as it was sent
	r, _ = parseResponses([]byte(`{"model":"gpt-5","input":"hi","text":{"format":{"type":"text"}}}`))
	if r.Format != nil || string(r.Text) != `{"format":{"type":"text"}}` {
		t.Fatalf("plain text: format %+v, text %s", r.Format, r.Text)
	}
}

// An upstream that turns the format away (DeepSeek: "This response_format
// type is unavailable now") is asked again with the format in the system
// prompt, and so from then on, rather than the client getting a 400 for a
// field the model could have been told in words.
func TestRefusedFormatToldInWords(t *testing.T) {
	fresh(t)
	up := &formatVendor{refuse: true}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "ds", Name: "DS", Key: "k", Chat: srv.URL + "/v1", Models: []string{"deepseek-chat"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	for i := range 2 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(strings.Replace(sdkResponsesParse, "%s", "ds/deepseek-chat", 1))))
		if rec.Code != 200 {
			t.Fatalf("turn %d: status %d: %s", i, rec.Code, rec.Body.String())
		}
		_, sent := up.sent()
		if sent["response_format"] != nil {
			t.Fatalf("turn %d: response_format still sent: %s", i, jsonOf(sent["response_format"]))
		}
		if !strings.Contains(jsonOf(sent["messages"]), "matching this JSON schema") {
			t.Fatalf("turn %d: format not told in words: %s", i, jsonOf(sent["messages"]))
		}
	}
	up.mu.Lock()
	n := len(up.bodies)
	up.mu.Unlock()
	if n != 3 {
		t.Fatalf("%d requests, want 3: one refused, then two told in words", n)
	}
}

// Gemini's structured output (responseMimeType with responseJsonSchema)
// is read into the format, and a Gemini upstream (Code Assist, Factory's
// generateContent) is asked for one in those fields.
func TestStructuredOutputOnGemini(t *testing.T) {
	r, err := parseChat([]byte(strings.Replace(sdkChatParse, "%s", "gemini-3-flash", 1)))
	if err != nil {
		t.Fatal(err)
	}
	var built struct {
		Request struct {
			GenerationConfig map[string]json.RawMessage `json:"generationConfig"`
		} `json:"request"`
	}
	json.Unmarshal(buildCodeAssist(r, "gemini-3-flash", "gemini"), &built)
	gc := built.Request.GenerationConfig
	if string(gc["responseMimeType"]) != `"application/json"` || !sameFormat(t, string(gc["responseJsonSchema"]), calendarSchema) {
		t.Fatalf("generationConfig = %v", gc)
	}
	g, err := parseGemini([]byte(`{"contents":[{"role":"user","parts":[{"text":"Alice and Bob are going to a science fair on Friday."}]}],"generationConfig":{"responseMimeType":"application/json","responseJsonSchema":` + calendarSchema + `}}`))
	if err != nil {
		t.Fatal(err)
	}
	var chat struct {
		ResponseFormat json.RawMessage `json:"response_format"`
	}
	json.Unmarshal(buildChat(g, "gpt-4o-mini", "openrouter.ai", false), &chat)
	if !sameFormat(t, string(chat.ResponseFormat), `{"type":"json_schema","json_schema":{"name":"response","schema":`+calendarSchema+`}}`) {
		t.Fatalf("response_format = %s", chat.ResponseFormat)
	}
}
