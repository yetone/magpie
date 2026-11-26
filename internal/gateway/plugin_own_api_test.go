package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// agyVendor answers every request with Anthropic's Messages, streamed or
// whole as asked, whatever path it came on: the agy-cli plugin's fetch
// runs agy itself and answers on its provider's own API, Anthropic's, for
// any request it is handed. paths are the paths it was asked at.
type agyVendor struct {
	mu    sync.Mutex
	paths []string
}

func (v *agyVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	v.mu.Lock()
	v.paths = append(v.paths, r.URL.Path)
	v.mu.Unlock()
	sse := func(events ...string) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		for _, e := range events {
			io.WriteString(w, e+"\n\n")
		}
	}
	whole := func(body string) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}
	streamed := strings.Contains(string(b), `"stream":true`)
	switch {
	case strings.Contains(r.URL.Path, "enerateContent"): // Gemini's, the google package's
		const reply = `{"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2687,"candidatesTokenCount":26,"totalTokenCount":2713},"responseId":"gem_agy1"}`
		if strings.Contains(r.URL.Path, "stream") {
			sse("data: " + reply)
		} else {
			whole(reply)
		}
	case strings.HasSuffix(r.URL.Path, "/responses"): // OpenAI's Responses
		const done = `{"id":"resp_agy1","object":"response","status":"completed","output":[{"type":"message","id":"m1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"OK","annotations":[]}]}],"usage":{"input_tokens":2687,"output_tokens":26,"total_tokens":2713}}`
		if streamed {
			sse(`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"resp_agy1","object":"response","status":"in_progress","output":[]}}`,
				`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"m1","delta":"OK"}`,
				`event: response.completed`+"\n"+`data: {"type":"response.completed","response":`+done+`}`)
		} else {
			whole(done)
		}
	default: // Anthropic's Messages, as the reporter's agy-cli answered
		if !streamed {
			whole(`{"id":"msg_agy1","type":"message","role":"assistant","model":"x","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":2687,"output_tokens":26}}`)
			return
		}
		var events []string
		for _, e := range []string{
			`{"type":"message_start","message":{"id":"msg_agy1","type":"message","role":"assistant","model":"x","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":2687,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":26,"output_tokens_details":{"thinking_tokens":21}}}`,
			`{"type":"message_stop"}`,
		} {
			events = append(events, "event: "+strings.SplitN(strings.TrimPrefix(e, `{"type":"`), `"`, 2)[0]+"\ndata: "+e)
		}
		sse(events...)
	}
}

func (v *agyVendor) last() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.paths) == 0 {
		return ""
	}
	return v.paths[len(v.paths)-1]
}

// A plugin whose provider is Anthropic's (agy-cli: npm @ai-sdk/anthropic)
// answers a chat client in chat completions, not in the Messages SSE its
// fetch gives (629409ddd on X, #7889). agy-cli 0.1.141 listed an image
// model on openai-compatible beside its Messages models, so the provider
// had a chat address too; a model the plugin answers but doesn't list (the
// reporter's gemini-4-hydrogen-medium, an effort's own id under the
// gemini-4-hydrogen family agy-cli lists) had no API of its own, took the
// provider's first, chat, and the request went to the plugin as it came,
// its Anthropic reply relayed as it was and recorded as 0 in, 0 out. Each
// client API, streamed and whole, gets its own API's reply and the usage
// the plugin's reply counts, for the listed model and the unlisted one, on
// a provider of each package that isn't chat completions.
func TestPluginUnlistedModelTakesTheProviderAPI(t *testing.T) {
	for _, pc := range []struct {
		agy, asked, endpoint string // $FAKE_AGY, the vendor's path, usage's endpoint
	}{
		{"1", "/messages", "/v1/messages"},
		{"openai", "/responses", "/v1/responses"},
		{"google", "enerateContent", "enerateContent"},
	} {
		t.Run(pc.agy, func(t *testing.T) {
			t.Setenv("FAKE_AGY", pc.agy)
			v := &agyVendor{}
			pid := besideFake(t, "agy-cli", v)
			h := New().Handler()
			for _, model := range []string{"fake-hydrogen", "fake-hydrogen-medium"} {
				name := pid + "/" + model
				for _, c := range []struct {
					api, path, body string
					stream          bool
					want            []string // in the reply
				}{
					{"chat", "/v1/chat/completions", `{"model":"` + name + `","stream":true,"messages":[{"role":"user","content":"Reply with OK"}]}`, true,
						[]string{`"choices"`, `"finish_reason":"stop"`, "data: [DONE]"}},
					{"chat", "/v1/chat/completions", `{"model":"` + name + `","messages":[{"role":"user","content":"Reply with OK"}]}`, false,
						[]string{`"choices"`, `"finish_reason":"stop"`, `"chat.completion"`}},
					{"responses", "/v1/responses", `{"model":"` + name + `","stream":true,"input":"Reply with OK"}`, true,
						[]string{"response.output_text.delta", "response.completed"}},
					{"responses", "/v1/responses", `{"model":"` + name + `","input":"Reply with OK"}`, false,
						[]string{`"object":"response"`, `"output_text"`}},
					{"gemini", "/v1beta/models/" + name + ":streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"Reply with OK"}]}]}`, true,
						[]string{`"candidates"`, `"finishReason":"STOP"`}},
					{"gemini", "/v1beta/models/" + name + ":generateContent", `{"contents":[{"role":"user","parts":[{"text":"Reply with OK"}]}]}`, false,
						[]string{`"candidates"`, `"finishReason":"STOP"`}},
				} {
					rec := httptest.NewRecorder()
					h.ServeHTTP(rec, httptest.NewRequest("POST", c.path, strings.NewReader(c.body)))
					out := rec.Body.String()
					what := model + " " + c.api
					if c.stream {
						what += " streamed"
					}
					if rec.Code != 200 {
						t.Errorf("%s: %d %.300s", what, rec.Code, out)
						continue
					}
					if !strings.Contains(v.last(), pc.asked) {
						t.Errorf("%s: the plugin was asked at %s, not on its provider's own API (%s)", what, v.last(), pc.asked)
					}
					for api, raw := range map[string][]string{
						"chat":      {"message_start", "response.output_text.delta", `"candidates"`},
						"responses": {"message_start", `"candidates"`, `"choices"`},
						"gemini":    {"message_start", "response.output_text.delta", `"choices"`},
					} {
						for _, m := range raw {
							if api == c.api && strings.Contains(out, m) {
								t.Errorf("%s: the plugin's reply went to the client in another API (%s): %.300s", what, m, out)
							}
						}
					}
					if !strings.Contains(out, "OK") {
						t.Errorf("%s: no text in the reply: %.300s", what, out)
					}
					for _, w := range c.want {
						if !strings.Contains(out, w) {
							t.Errorf("%s: the reply lacks %s: %.300s", what, w, out)
						}
					}
					u := lastUsage(t)
					if u.Input != 2687 || u.Output != 26 {
						t.Errorf("%s: usage recorded in %d, out %d; want in 2687, out 26", what, u.Input, u.Output)
					}
					if u.ResponseID == "" {
						t.Errorf("%s: usage recorded no response id", what)
					}
					if !strings.Contains(u.Endpoint, pc.endpoint) {
						t.Errorf("%s: usage's endpoint %q isn't the provider's own API's (%s)", what, u.Endpoint, pc.endpoint)
					}
				}
			}
		})
	}
}
