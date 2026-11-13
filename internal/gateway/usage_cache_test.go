package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// cachingVendor answers in one wire format, whole or streamed, with a
// usage that says what it read from its prompt cache and what it wrote to
// it, each in that format's own words (#1839).
type cachingVendor struct{ wire string }

func (v cachingVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	if v.wire == "" { // as the path asks: a plugin's models speak each their own
		switch {
		case strings.HasSuffix(r.URL.Path, "/messages"):
			v.wire = "anthropic"
		case strings.Contains(r.URL.Path, "enerateContent"):
			v.wire = "gemini"
		case strings.HasSuffix(r.URL.Path, "/responses"):
			v.wire = "responses"
		default:
			v.wire = "chat"
		}
	}
	stream := strings.Contains(string(b), `"stream":true`) || strings.Contains(r.URL.Path, "streamGenerateContent")
	var whole string
	var events []string
	switch v.wire {
	case "anthropic":
		whole = `{"id":"msg_1","type":"message","role":"assistant","model":"m1","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":7,"cache_read_input_tokens":300,"cache_creation_input_tokens":40,"cache_creation":{"ephemeral_5m_input_tokens":40,"ephemeral_1h_input_tokens":0}}}`
		// Anthropic tells the cache in message_start; message_delta's
		// usage carries the output alone
		events = []string{
			"event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m1","content":[],"usage":{"input_tokens":12,"output_tokens":1,"cache_read_input_tokens":300,"cache_creation_input_tokens":40,"cache_creation":{"ephemeral_5m_input_tokens":40,"ephemeral_1h_input_tokens":0}}}}`,
			"event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
			"event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":0}`,
			"event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
			"event: message_stop\ndata: " + `{"type":"message_stop"}`,
		}
	case "chat", "chat-anthropic-names", "chat-deepseek", "chat-zero-details", "chat-moonshot", "chat-mistral", "chat-mistral-zero-details":
		u := map[string]string{
			// OpenAI's: prompt_tokens counts the cached ones too
			"chat": `{"prompt_tokens":352,"completion_tokens":7,"total_tokens":359,"prompt_tokens_details":{"cached_tokens":300}}`,
			// a relay in front of Claude, in Anthropic's own words
			"chat-anthropic-names": `{"prompt_tokens":12,"completion_tokens":7,"total_tokens":19,"cache_read_input_tokens":300,"cache_creation_input_tokens":40}`,
			// DeepSeek's hits and misses
			"chat-deepseek": `{"prompt_tokens":352,"completion_tokens":7,"total_tokens":359,"prompt_cache_hit_tokens":300,"prompt_cache_miss_tokens":52}`,
			// a relay that puts DeepSeek's hits beside a 0 of OpenAI's
			"chat-zero-details": `{"prompt_tokens":352,"completion_tokens":7,"total_tokens":359,"prompt_tokens_details":{"cached_tokens":0},"prompt_cache_hit_tokens":300,"prompt_cache_miss_tokens":52}`,
			// Moonshot's top-level cached_tokens beside a 0 of OpenAI's
			"chat-moonshot": `{"prompt_tokens":352,"completion_tokens":7,"total_tokens":359,"prompt_tokens_details":{"cached_tokens":0},"cached_tokens":300}`,
			// Mistral's older top-level num_cached_tokens, alone and beside
			// a 0 of OpenAI's, counted in prompt_tokens (daoche_wuzui on X)
			"chat-mistral":              `{"prompt_tokens":352,"completion_tokens":7,"total_tokens":359,"num_cached_tokens":300}`,
			"chat-mistral-zero-details": `{"prompt_tokens":352,"completion_tokens":7,"total_tokens":359,"prompt_tokens_details":{"cached_tokens":0},"num_cached_tokens":300}`,
		}[v.wire]
		whole = `{"id":"c1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":` + u + `}`
		events = []string{
			`data: {"id":"c1","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","model":"m1","choices":[],"usage":` + u + `}`,
			`data: [DONE]`,
		}
	case "responses":
		u := `{"input_tokens":352,"output_tokens":7,"total_tokens":359,"input_tokens_details":{"cached_tokens":300},"output_tokens_details":{"reasoning_tokens":0}}`
		resp := `{"id":"r1","object":"response","status":"completed","model":"m1","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hi","annotations":[]}]}],"usage":` + u + `}`
		whole = resp
		events = []string{
			"event: response.created\ndata: " + `{"type":"response.created","response":{"id":"r1","object":"response","status":"in_progress","model":"m1","output":[]}}`,
			"event: response.output_item.added\ndata: " + `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","status":"in_progress","content":[]}}`,
			"event: response.output_text.delta\ndata: " + `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"hi"}`,
			"event: response.output_item.done\ndata: " + `{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hi","annotations":[]}]}}`,
			"event: response.completed\ndata: " + `{"type":"response.completed","response":` + resp + `}`,
		}
	case "gemini":
		u := `{"promptTokenCount":352,"candidatesTokenCount":7,"totalTokenCount":359,"cachedContentTokenCount":300}`
		whole = `{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP","index":0}],"usageMetadata":` + u + `,"modelVersion":"m1","responseId":"g1"}`
		events = []string{
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"index":0}],"modelVersion":"m1","responseId":"g1"}`,
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP","index":0}],"usageMetadata":` + u + `,"modelVersion":"m1","responseId":"g1"}`,
		}
	}
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, whole)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, ev := range events {
		io.WriteString(w, ev+"\n\n")
	}
}

// Every route records what the vendor read from its prompt cache and what
// it wrote to it, whatever the vendor speaks and whatever the agent asked
// in, passed through or translated, whole or streamed (#1839: a
// subscription's records showed in near zero beside a large out, the
// cache nowhere).
func TestUsageRecordsCacheTokensOnEveryRoute(t *testing.T) {
	type want struct{ in, read, write int }
	vendors := []struct {
		wire  string
		proto provider.Protocol
		want  want
	}{
		{"anthropic", provider.Anthropic, want{12, 300, 40}},
		{"chat", provider.Chat, want{52, 300, 0}},
		{"chat-anthropic-names", provider.Chat, want{12, 300, 40}},
		{"chat-deepseek", provider.Chat, want{52, 300, 0}},
		{"chat-zero-details", provider.Chat, want{52, 300, 0}},
		{"chat-moonshot", provider.Chat, want{52, 300, 0}},
		{"chat-mistral", provider.Chat, want{52, 300, 0}},
		{"chat-mistral-zero-details", provider.Chat, want{52, 300, 0}},
		{"responses", provider.Responses, want{52, 300, 0}},
		{"gemini", provider.Gemini, want{52, 300, 0}},
	}
	agents := []struct {
		name, path, body string
		stream           bool
	}{
		{"messages", "/v1/messages", `{"model":"fake/m1","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`, false},
		{"messages stream", "/v1/messages", `{"model":"fake/m1","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`, true},
		{"chat", "/v1/chat/completions", `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`, false},
		{"chat stream", "/v1/chat/completions", `{"model":"fake/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`, true},
		{"responses", "/v1/responses", `{"model":"fake/m1","input":"hi"}`, false},
		{"responses stream", "/v1/responses", `{"model":"fake/m1","stream":true,"input":"hi"}`, true},
		{"gemini", "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, false},
		{"gemini stream", "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, true},
	}
	for _, v := range vendors {
		for _, a := range agents {
			t.Run(v.wire+" to "+a.name, func(t *testing.T) {
				fresh(t)
				up := httptest.NewServer(cachingVendor{v.wire})
				t.Cleanup(up.Close)
				p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}}
				switch v.proto {
				case provider.Chat:
					p.Chat = up.URL + "/v1"
				case provider.Responses:
					p.Responses = up.URL + "/v1"
				case provider.Anthropic:
					p.Anthropic = up.URL
				case provider.Gemini:
					p.Gemini = up.URL + "/v1beta"
				}
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
				rec := httptest.NewRecorder()
				New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", a.path, strings.NewReader(a.body)))
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hi") {
					t.Fatalf("%d %s", rec.Code, rec.Body)
				}
				u := lastUsage(t)
				if u.Input != v.want.in || u.CacheRead != v.want.read || u.CacheWrite != v.want.write || u.Output != 7 {
					t.Errorf("usage in %d, out %d, cache read %d, cache write %d; want in %d, out 7, cache read %d, cache write %d",
						u.Input, u.Output, u.CacheRead, u.CacheWrite, v.want.in, v.want.read, v.want.write)
				}
			})
		}
	}
}

// A subscription moved onto its plugin records the cache as the built-in
// did: the plugin's fetch hands back the vendor's own reply, whose usage
// the gateway reads in the wire format the plugin's model speaks.
func TestPluginUsageRecordsCacheTokens(t *testing.T) {
	movedFake(t, "factory", cachingVendor{})
	for _, c := range []struct {
		model           string
		in, read, write int
	}{
		{"fake-1", 52, 300, 0},       // @ai-sdk/openai-compatible: Chat
		{"fake-claude", 12, 300, 40}, // @ai-sdk/anthropic: Messages
		{"fake-gemini", 52, 300, 0},  // @ai-sdk/google: generateContent
	} {
		for _, stream := range []bool{false, true} {
			body := `{"model":"factory/` + c.model + `","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
			if stream {
				body = strings.Replace(body, `{"model"`, `{"stream":true,"model"`, 1)
			}
			rec := httptest.NewRecorder()
			New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hi") {
				t.Fatalf("%s stream=%v: %d %s", c.model, stream, rec.Code, rec.Body)
			}
			u := lastUsage(t)
			if u.Input != c.in || u.CacheRead != c.read || u.CacheWrite != c.write || u.Output != 7 {
				t.Errorf("%s stream=%v: usage in %d, out %d, cache read %d, cache write %d; want in %d, out 7, cache read %d, cache write %d",
					c.model, stream, u.Input, u.Output, u.CacheRead, u.CacheWrite, c.in, c.read, c.write)
			}
		}
	}
}

// The Claude subscription, answered by Claude Code itself, hands the
// gateway the cache its stream-json tells: what serve records for the
// request is this usage, for the agent's Messages and for a classifier's
// Chat alike.
func TestClaudeSubscriptionUsageKeepsCacheTokens(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	dir := t.TempDir()
	testenv.Program(t, filepath.Join(dir, "claude"), `#!/bin/sh
while read -r line; do
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-haiku-4-5","usage":{"input_tokens":12,"cache_read_input_tokens":300,"cache_creation_input_tokens":40,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":40},"output_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	runsEndWithTest(t, dir)
	s := New()
	t.Cleanup(s.subscription.abortAll)
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	for _, c := range []struct {
		proto      provider.Protocol
		path, body string
	}{
		{provider.Anthropic, "/v1/messages", `{"model":"claude-haiku-4-5","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`},
		{provider.Chat, "/v1/chat/completions", `{"model":"claude-haiku-4-5","temperature":0,"messages":[{"role":"system","content":"classify"},{"role":"user","content":"hi"}]}`},
	} {
		var u Usage
		rec := httptest.NewRecorder()
		if code, msg := s.serveClaudeSubscription(rec, httptest.NewRequest("POST", c.path, strings.NewReader(c.body)), c.proto, p, "claude-haiku-4-5", []byte(c.body), &u); code != 200 {
			t.Fatalf("%s: %d %s", c.proto, code, msg)
		}
		if u.Input != 12 || u.Output != 7 || u.CacheRead != 300 || u.CacheWrite != 40 || u.CacheWrite1h != 40 {
			t.Errorf("%s: usage %+v; want in 12, out 7, cache read 300, cache write 40 (all for an hour)", c.proto, u)
		}
	}
}
