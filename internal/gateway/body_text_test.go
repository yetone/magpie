package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// strictUp is a vendor that refuses a body with bytes that aren't UTF-8
// or a lone surrogate's escape, as DeepSeek does ("Failed to deserialize
// the JSON body into the target type: … invalid unicode code point"),
// and otherwise answers OK on whichever API it was asked, keeping the
// bodies it was sent.
type strictUp struct {
	mu     sync.Mutex
	bodies [][]byte
}

// loneEscape is a surrogate escape, with its pair when it has one.
var loneEscape = regexp.MustCompile(`(?i)\\ud[89ab][0-9a-f]{2}(?:\\ud[c-f][0-9a-f]{2})?|\\ud[c-f][0-9a-f]{2}`)

func lonely(b []byte) bool {
	// an escaped backslash starts no escape
	b = bytes.ReplaceAll(b, []byte(`\\`), []byte("__"))
	for _, m := range loneEscape.FindAll(b, -1) {
		if len(m) != 12 {
			return true
		}
	}
	return false
}

func (u *strictUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.bodies = append(u.bodies, b)
	u.mu.Unlock()
	if !utf8.Valid(b) || lonely(b) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"message":"Failed to deserialize the JSON body into the target type: messages[0]: invalid unicode code point at line 1 column 60","type":"invalid_request_error"}}`)
		return
	}
	stream := gjson.GetBytes(b, "stream").Bool()
	switch {
	case strings.HasSuffix(r.URL.Path, "/chat/completions"):
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(`data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"OK"}}]}`,
				`data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
				`data: [DONE]`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	case strings.HasSuffix(r.URL.Path, "/responses"):
		item := `{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"OK","annotations":[]}]}`
		done := `{"id":"resp_1","object":"response","status":"completed","model":"m","output":[` + item + `],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(`data: {"type":"response.created","response":{"id":"resp_1","object":"response","status":"in_progress","output":[]}}`,
				`data: {"type":"response.output_item.done","output_index":0,"item":`+item+`}`,
				`data: {"type":"response.completed","response":`+done+`}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, done)
	default: // Anthropic's /v1/messages
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
				`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}`,
				`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
				`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
				`event: message_stop`+"\n"+`data: {"type":"message_stop"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}
}

func (u *strictUp) last() []byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.bodies) == 0 {
		return nil
	}
	return u.bodies[len(u.bodies)-1]
}

// strictOn saves a provider "up" on proto, served by a strictUp.
func strictOn(t *testing.T, proto provider.Protocol) *strictUp {
	t.Helper()
	u := &strictUp{}
	srv := httptest.NewServer(u)
	t.Cleanup(srv.Close)
	p := provider.Provider{ID: "up", Name: "Up", Key: "k", Models: []string{"m"}}
	switch proto {
	case provider.Anthropic:
		p.Anthropic = srv.URL
	case provider.Responses:
		p.Responses = srv.URL + "/v1"
	default:
		p.Chat = srv.URL + "/v1"
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return u
}

// badText is what laincatx on X sent: GBK bytes (你好), a lone surrogate
// written as UTF-8, and lone surrogate escapes, each as a JSON string's
// content between "a " and " b".
var badText = []struct{ name, raw string }{
	{"gbk", "\xc4\xe3\xba\xc3"},
	{"utf8 surrogate", "\xed\xa0\x80"},
	{"high escape", `\ud800`},
	{"low escape", `\udc00`},
	{"high then not low", `\ud800\u0041`},
	{"cut at the end", "\xe4\xbd"},
}

// inbound is each API an agent asks magpie on, with the text in the
// user's message and in a tool's result, where a file's bytes arrive.
func inbound(text string, stream bool) []struct{ path, body string } {
	s := "false"
	if stream {
		s = "true"
	}
	return []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"up/m","stream":` + s + `,"messages":[{"role":"user","content":"a ` + text + ` b"}]}`},
		{"/v1/chat/completions", `{"model":"up/m","stream":` + s + `,"messages":[{"role":"user","content":"read it"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"a ` + text + ` b"}],"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}]}`},
		{"/v1/responses", `{"model":"up/m","stream":` + s + `,"input":[{"role":"user","content":"a ` + text + ` b"}]}`},
		{"/v1/responses", `{"model":"up/m","stream":` + s + `,"input":[{"role":"user","content":"read it"},{"type":"function_call","call_id":"call_1","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"a ` + text + ` b"}],"tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}`},
		{"/v1/messages", `{"model":"up/m","max_tokens":100,"stream":` + s + `,"messages":[{"role":"user","content":"a ` + text + ` b"}]}`},
		{"/v1/messages", `{"model":"up/m","max_tokens":100,"stream":` + s + `,"messages":[{"role":"user","content":"read it"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"a ` + text + ` b"}]}],"tools":[{"name":"read","input_schema":{"type":"object"}}]}`},
	}
}

// Text that isn't text reaches every vendor as U+FFFD, on every API in
// and out, so a strict vendor (DeepSeek, OpenAI) answers as a lenient one
// does and the turn goes on; the rest of the string arrives as it was
// sent, and nothing rests (laincatx on X).
func TestBadTextReachesVendorsAsReplacement(t *testing.T) {
	for _, up := range []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic} {
		for _, bad := range badText {
			for _, stream := range []bool{false, true} {
				for _, ask := range inbound(bad.raw, stream) {
					fresh(t)
					u := strictOn(t, up)
					rec := httptest.NewRecorder()
					req := httptest.NewRequest("POST", ask.path, strings.NewReader(ask.body))
					req.Header.Set("Content-Type", "application/json")
					New().Handler().ServeHTTP(rec, req)
					got := u.last()
					if rec.Code != 200 || !utf8.Valid(got) || lonely(got) {
						t.Fatalf("%s upstream, %s, stream %v, %s %s: %d\nsent %q\n%s", up, bad.name, stream, ask.path, ask.body, rec.Code, got, rec.Body.String())
					}
					if !bytes.Contains(got, []byte("�")) && !bytes.Contains(bytes.ToLower(got), []byte(`\ufffd`)) {
						t.Fatalf("%s upstream, %s, %s: no U+FFFD in %q", up, bad.name, ask.path, got)
					}
					restingUntil.Lock()
					n := len(restingUntil.m)
					restingUntil.Unlock()
					if n != 0 {
						t.Fatalf("%s upstream, %s, %s: %d resting", up, bad.name, ask.path, n)
					}
				}
			}
		}
	}
}

// What is text already goes as it was: a surrogate pair's escapes (an
// emoji as Python's json.dumps writes it), an escaped backslash before
// "ud800", which is the six characters \ud800 and no escape, and CJK.
func TestGoodTextGoesAsSent(t *testing.T) {
	fresh(t)
	u := strictOn(t, provider.Chat)
	body := `{"model":"up/m","stream":false,"messages":[{"role":"user","content":"a \ud83d\ude00 \\ud800 你好 b"}]}`
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got := gjson.GetBytes(u.last(), "messages.0.content").String(); got != "a 😀 \\ud800 你好 b" {
		t.Fatalf("sent %q", got)
	}
}

func TestTextBody(t *testing.T) {
	json := http.Header{"Content-Type": {"application/json"}}
	for _, c := range []struct {
		h        http.Header
		in, want string
	}{
		{json, `{"a":"x` + "\xc4\xe3" + `y"}`, `{"a":"x` + "\uFFFD\uFFFD" + `y"}`},
		{json, `{"a":"\ud800"}`, `{"a":"\ufffd"}`},
		{json, `{"a":"\uDBFF\uDFFF"}`, `{"a":"\uDBFF\uDFFF"}`},
		{json, `{"a":"\ud800\ud800\udc00"}`, `{"a":"\ufffd\ud800\udc00"}`},
		{json, `{"a":"\\ud800"}`, `{"a":"\\ud800"}`},
		{json, `{"a":"\\\ud800"}`, `{"a":"\\\ufffd"}`},
		{json, `{"a":"\u00e9 \u4f60"}`, `{"a":"\u00e9 \u4f60"}`},
		{json, `{"a":"\ud80"}`, `{"a":"\ud80"}`},
		{json, `{"a":"x\`, `{"a":"x\`},
		{nil, ` [{"a":"` + "\xff" + `"}]`, ` [{"a":"` + "\uFFFD" + `"}]`},
		{json, `{"a":"\ud800`, `{"a":"\ufffd`},
		// not JSON, or not text: as it came
		{http.Header{"Content-Type": {"multipart/form-data; boundary=x"}}, "{\xff", "{\xff"},
		{http.Header{"Content-Type": {"audio/wav"}}, "{\xff", "{\xff"},
		{json, "RIFF\xff", "RIFF\xff"},
	} {
		if got := string(textBody(c.h, []byte(c.in))); got != c.want {
			t.Errorf("%q: %q, want %q", c.in, got, c.want)
		}
	}
	// a valid body is the same slice, not a copy
	in := []byte(`{"a":"😀 ok"}`)
	if got := textBody(json, in); &got[0] != &in[0] {
		t.Error("a valid body was copied")
	}
}

// The same over Codex's socket, whose turns are frames, not bodies, and
// on Gemini's API.
func TestBadTextOnSocketAndGemini(t *testing.T) {
	for _, bad := range badText {
		fresh(t)
		u := strictOn(t, provider.Responses)
		_, ts := sockServer(t)
		c, _, err := dialSock(t, ts, "/v1/responses", nil)
		if err != nil {
			t.Fatal(err)
		}
		got := c.turn(`{"type":"response.create","model":"up/m","stream":true,"store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"a ` + bad.raw + ` b"}]}]}`)
		sent := u.last()
		if last(got).Get("type").String() != "response.completed" || !utf8.Valid(sent) || lonely(sent) {
			t.Fatalf("socket, %s: %v\nsent %q", bad.name, got, sent)
		}

		fresh(t)
		u = strictOn(t, provider.Chat)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1beta/models/up/m:generateContent", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"a `+bad.raw+` b"}]}]}`))
		req.Header.Set("Content-Type", "application/json")
		New().Handler().ServeHTTP(rec, req)
		if sent := u.last(); rec.Code != 200 || !utf8.Valid(sent) || lonely(sent) {
			t.Fatalf("gemini, %s: %d %s\nsent %q", bad.name, rec.Code, rec.Body.String(), sent)
		}
	}
}
