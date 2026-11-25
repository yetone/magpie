package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// sdkMessage says what is wrong with a Responses message item for the AI
// SDK, "" when nothing: the message branch of @ai-sdk/openai 4.0.123's
// openaiResponsesResponseSchema (src/responses/openai-responses-api.ts),
// which a reply asked for whole is parsed with, and refused whole as
// "Invalid JSON response" when one field fails:
//
//	type: 'message', role: 'assistant', id: string,
//	phase: enum('commentary', 'final_answer').nullish(),
//	content: array of
//	  { type: 'output_text', text: string,
//	    logprobs: array(...).nullish(),
//	    annotations: array(url_citation | file_citation |
//	                       container_file_citation | file_path) }
//	| { type: 'refusal', refusal: string }
func sdkMessage(it map[string]any) string {
	if it["role"] != "assistant" {
		return fmt.Sprintf("role %v", it["role"])
	}
	if _, ok := it["id"].(string); !ok {
		return "no id"
	}
	switch it["phase"] {
	case nil, "commentary", "final_answer":
	default:
		return fmt.Sprintf("phase %v", it["phase"])
	}
	parts, ok := it["content"].([]any)
	if !ok {
		return "content not a list"
	}
	for i, p := range parts {
		pm, _ := p.(map[string]any)
		switch pm["type"] {
		case "output_text":
			if _, ok := pm["text"].(string); !ok {
				return fmt.Sprintf("content[%d] has no text", i)
			}
			if _, ok := pm["annotations"].([]any); !ok {
				return fmt.Sprintf("content[%d] has no annotations list", i)
			}
			if lp, ok := pm["logprobs"]; ok && lp != nil {
				if _, ok := lp.([]any); !ok {
					return fmt.Sprintf("content[%d] logprobs %v", i, lp)
				}
			}
		case "refusal":
			if _, ok := pm["refusal"].(string); !ok {
				return fmt.Sprintf("content[%d] has no refusal", i)
			}
		default:
			return fmt.Sprintf("content[%d] type %v", i, pm["type"])
		}
	}
	return ""
}

// sdkReply says what is wrong with a whole Responses reply's messages for
// the AI SDK, "" when nothing, and fails when it has none.
func sdkReply(t *testing.T, b []byte) string {
	t.Helper()
	var res struct {
		Output []map[string]any `json:"output"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	n := 0
	for i, it := range res.Output {
		if it["type"] != "message" {
			continue
		}
		n++
		if why := sdkMessage(it); why != "" {
			return fmt.Sprintf("output[%d]: %s", i, why)
		}
	}
	if n == 0 {
		t.Fatalf("no message in %s", b)
	}
	return ""
}

// streamShape says what is wrong with a Responses stream's whole parts and
// items: each content_part's part and each output_item's message carries
// what a reply asked for whole does, and so does response.completed's
// output.
func streamShape(t *testing.T, body string) string {
	t.Helper()
	seen := map[string]bool{}
	for _, ev := range events(strings.ReplaceAll(body, "\r\n", "\n")) {
		typ, _ := ev["type"].(string)
		switch typ {
		case "response.content_part.added", "response.content_part.done":
			seen[typ] = true
			if why := sdkMessage(map[string]any{"role": "assistant", "id": "m", "content": []any{ev["part"]}}); why != "" {
				return typ + ": " + why
			}
		case "response.output_item.added", "response.output_item.done":
			if it, _ := ev["item"].(map[string]any); it["type"] == "message" {
				seen[typ] = true
				if why := sdkMessage(it); why != "" {
					return typ + ": " + why
				}
			}
		case "response.completed":
			seen[typ] = true
			res, _ := json.Marshal(ev["response"])
			if why := sdkReply(t, res); why != "" {
				return typ + ": " + why
			}
		}
	}
	for _, typ := range []string{"response.content_part.added", "response.content_part.done", "response.output_item.added", "response.output_item.done", "response.completed"} {
		if !seen[typ] {
			t.Fatalf("no %s message in\n%s", typ, body)
		}
	}
	return ""
}

func textFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Winnie7572 on X: a model a plugin serves on Responses (Open Design's
// auto/deepseek-v4.1-flash, glm-5.3-flash), asked for one body, came back
// with output_text parts without annotations, as the plugin's upstream
// streamed them, and the AI SDK refused the reply as "Invalid JSON
// response". The relayed reply's parts get the list, whole and streamed.
func TestRelayedTextPartsHaveAnnotations(t *testing.T) {
	t.Run("whole", func(t *testing.T) {
		in := textFixture(t, "responses_text_no_annotations.json")
		if sdkReply(t, []byte(in)) == "" {
			t.Fatal("the fixture already has its annotations")
		}
		if why := sdkReply(t, []byte(tidied(t, false, in))); why != "" {
			t.Fatal(why)
		}
	})
	t.Run("stream", func(t *testing.T) {
		in := textFixture(t, "responses_text_no_annotations.sse")
		got := tidied(t, true, in)
		if why := streamShape(t, got); why != "" {
			t.Fatalf("%s\n%s", why, got)
		}
		if n, want := len(events(got)), len(events(in)); n != want {
			t.Fatalf("%d events, want %d\n%s", n, want, got)
		}
	})
	t.Run("stream, CRLF", func(t *testing.T) {
		got := tidied(t, true, strings.ReplaceAll(textFixture(t, "responses_text_no_annotations.sse"), "\n", "\r\n"))
		if why := streamShape(t, got); why != "" {
			t.Fatalf("%s\n%s", why, got)
		}
	})
	t.Run("gathered", func(t *testing.T) {
		g := &gathered{h: http.Header{}, status: 200}
		g.h.Set("Content-Type", "text/event-stream")
		io.WriteString(g, tidied(t, true, textFixture(t, "responses_text_no_annotations.sse")))
		rec := httptest.NewRecorder()
		if code, _ := g.finish(rec, provider.Provider{Name: "p"}, true, "", ""); code != 200 {
			t.Fatalf("%d %s", code, rec.Body)
		}
		if why := sdkReply(t, rec.Body.Bytes()); why != "" {
			t.Fatalf("%s\n%s", why, rec.Body)
		}
	})
	t.Run("a list the upstream wrote is kept", func(t *testing.T) {
		note := `{"type":"url_citation","start_index":0,"end_index":2,"url":"https://example.com","title":"x"}`
		in := strings.Replace(textFixture(t, "responses_text_no_annotations.json"), `"text":"Hi"}`, `"text":"Hi","annotations":[`+note+`]}`, 1)
		got := tidied(t, false, in)
		var res struct {
			Output []struct {
				Content []struct {
					Annotations []map[string]any `json:"annotations"`
				} `json:"content"`
			} `json:"output"`
		}
		if err := json.Unmarshal([]byte(got), &res); err != nil || len(res.Output) != 2 || len(res.Output[1].Content) != 1 ||
			len(res.Output[1].Content[0].Annotations) != 1 || res.Output[1].Content[0].Annotations[0]["url"] != "https://example.com" {
			t.Fatalf("%v: %s", err, got)
		}
	})
}

// The reporter's case through the gateway: a plugin's Responses model,
// whose upstream streams parts without annotations, asked for one body
// (gathered) and asked for a stream.
func TestPluginReplyTextPartsHaveAnnotations(t *testing.T) {
	pid := besideFake(t, "opendesign", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, textFixture(t, "responses_text_no_annotations.sse"))
	}))
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint("stream ", stream), func(t *testing.T) {
			body := fmt.Sprintf(`{"model":"%s/fake-resp","stream":%v,"input":"Say hi in one word."}`, pid, stream)
			rec := httptest.NewRecorder()
			New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
			if rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			why := ""
			if stream {
				why = streamShape(t, rec.Body.String())
			} else {
				why = sdkReply(t, rec.Body.Bytes())
			}
			if why != "" {
				t.Fatalf("%s\n%s", why, rec.Body)
			}
		})
	}
}

// Every other way a reply reaches a Responses client has the same shape:
// a Responses upstream's whole reply relayed, and a Chat upstream's reply
// translated, whole and streamed.
func TestRepliesTextPartsHaveAnnotations(t *testing.T) {
	t.Run("relayed whole", func(t *testing.T) {
		fresh(t)
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, textFixture(t, "responses_text_no_annotations.json"))
		}))
		t.Cleanup(up.Close)
		if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"deepseek-v4.1-flash"}, Responses: up.URL + "/v1"}); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"relay/deepseek-v4.1-flash","stream":false,"input":"hi"}`)))
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		if why := sdkReply(t, rec.Body.Bytes()); why != "" {
			t.Fatalf("%s\n%s", why, rec.Body)
		}
	})
	t.Run("relayed stream", func(t *testing.T) {
		fresh(t)
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, textFixture(t, "responses_text_no_annotations.sse"))
		}))
		t.Cleanup(up.Close)
		if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"deepseek-v4.1-flash"}, Responses: up.URL + "/v1"}); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"relay/deepseek-v4.1-flash","stream":true,"input":"hi"}`)))
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		if why := streamShape(t, rec.Body.String()); why != "" {
			t.Fatalf("%s\n%s", why, rec.Body)
		}
	})
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint("translated from chat, stream ", stream), func(t *testing.T) {
			fresh(t)
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				if strings.Contains(string(b), `"stream":true`) {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, sse(
						`data: {"id":"c1","object":"chat.completion.chunk","model":"glm-5.3-flash","choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"}}]}`,
						`data: {"id":"c1","object":"chat.completion.chunk","model":"glm-5.3-flash","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`,
						`data: [DONE]`))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"glm-5.3-flash","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
			}))
			t.Cleanup(up.Close)
			if err := provider.Save(provider.Provider{ID: "chat", Name: "Chat", Key: "k", Models: []string{"glm-5.3-flash"}, Chat: up.URL + "/v1"}); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":"chat/glm-5.3-flash","stream":%v,"input":"hi"}`, stream))))
			if rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			why := ""
			if stream {
				why = streamShape(t, rec.Body.String())
			} else {
				why = sdkReply(t, rec.Body.Bytes())
			}
			if why != "" {
				t.Fatalf("%s\n%s", why, rec.Body)
			}
		})
	}
}
