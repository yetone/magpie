package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// A byte-size refusal alone is not evidence of a token-window overflow.
// An independent text estimate well past the window still asks for compaction.
func TestPayloadTooLargeIsNotContextOverflow(t *testing.T) {
	for _, msg := range []string{
		"Factory: Request Entity Too Large",
		`{"error":{"type":"request_too_large","message":"Request exceeds the maximum size"}}`,
	} {
		if tooLong(413, msg) || overflowed(413, []byte(msg)) {
			t.Errorf("byte limit classified as token limit: %s", msg)
		}
		p := provider.Provider{ID: "fake", Name: "Fake", Contexts: map[string]int{"m": 100}}
		// Image bytes don't make a text estimate pass the token margin.
		tokens := estimate(&Request{Messages: []Message{{Role: "user", Parts: []Part{
			{Kind: Text, Text: strings.Repeat("word", 100)},
			{Kind: Image, Data: strings.Repeat(pngA, 4096)},
		}}}})
		if inferred, ok := tooLongUnsaid(p, "m", tokens, 0, 413, []byte(msg)); ok {
			t.Errorf("byte limit inferred as token limit: %s", inferred)
		}
		if inferred, ok := tooLongUnsaid(p, "m", 1000, 0, 413, []byte(msg)); !ok || !strings.Contains(inferred, "prompt is too long") {
			t.Errorf("text estimate past the window lost compaction: %s", inferred)
		}
		if retryable(413, []byte(msg)) {
			t.Error("unchanged oversized request would be retried")
		}
	}
}

// Body limits belong to a vendor, so another member may accept the same
// request. Neither account retry nor resting helps a member with a byte cap.
func TestPayloadTooLargeTriesAnotherMember(t *testing.T) {
	const answer = `{"id":"ok","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	for _, path := range []string{"/v1/messages", CodexPath + "/responses"} {
		for _, refusal := range []struct{ name, body string }{
			{"plain", "Request Entity Too Large"},
			{"typed", `{"error":{"type":"request_too_large","message":"Request exceeds the maximum size"}}`},
		} {
			for _, accepts := range []bool{true, false} {
				name := path + "/" + refusal.name + "/exhausted"
				if accepts {
					name = path + "/" + refusal.name + "/accepts"
				}
				t.Run(name, func(t *testing.T) {
					fresh(t)
					a := &scripted{replies: []reply{{413, "", refusal.body}}}
					b := &scripted{replies: []reply{{413, "", refusal.body}}}
					if accepts {
						b.replies = []reply{{200, "", answer}}
						if strings.HasSuffix(path, "/responses") {
							b.replies = []reply{{200, "text/event-stream", payloadAnswerStream()}}
						}
					}
					scriptedOn(t, "a", provider.Anthropic, a)
					scriptedOn(t, "b", provider.Anthropic, b)
					if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
						t.Fatal(err)
					}
					body := `{"model":"group/g","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
					if strings.HasSuffix(path, "/responses") {
						body = `{"model":"group/g","input":[{"role":"user","content":"hi"}]}`
					}
					s := New()
					w := httptest.NewRecorder()
					s.Handler().ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(body)))
					if a.n != 1 || b.n != 1 {
						t.Fatalf("body limit did not ask each member once: a %d, b %d, status %d", a.n, b.n, w.Code)
					}
					if accepts {
						if w.Code != 200 || !strings.Contains(w.Body.String(), "hello") || strings.Contains(w.Body.String(), "byte limit") {
							t.Fatalf("accepted fallback: %d %s", w.Code, w.Body.String())
						}
					} else if w.Code != 413 || strings.Count(w.Body.String(), "byte limit") != 1 || strings.Contains(w.Body.String(), "context_length_exceeded") {
						t.Fatalf("exhausted byte limits: %d %s", w.Code, w.Body.String())
					}
					route := lastRoute(s)
					if len(route.Tries) != 2 || route.Tries[0].Status != 413 || route.Tries[0].Fail == failOverflow || route.Tries[0].Rest != nil || route.Tries[1].Rest != nil {
						t.Fatalf("byte cap rested a member or became a token limit: %+v", route.Tries)
					}
					restingUntil.Lock()
					n := len(restingUntil.m)
					restingUntil.Unlock()
					if n != 0 {
						t.Fatalf("byte cap persisted %d rests", n)
					}
				})
			}
		}
	}
}

func TestPayloadTooLargeSkipsSameMemberAccounts(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{413, "", "Request Entity Too Large"}}}
	b := &scripted{replies: []reply{{200, "", chatOK}}}
	up := httptest.NewServer(a)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Key: "first", Keys: []provider.KeyAccount{{Key: "second"}}, Chat: up.URL + "/v1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hello") || a.n != 1 || b.n != 1 {
		t.Fatalf("byte cap accounts: %d %s (a %d, b %d)", code, body, a.n, b.n)
	}
}

func TestPayloadTooLargeOverWindowTriesMemberWithRoom(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{413, "", `{"error":{"type":"request_too_large","message":"Request exceeds the maximum size"}}`}}}
	b := &scripted{replies: []reply{{200, "", chatOK}}}
	c := &scripted{replies: []reply{{200, "", chatOK}}}
	for _, member := range []struct {
		id     string
		reply  *scripted
		window int
	}{{"a", a, 2000}, {"b", b, 2000}, {"c", c, 10000}} {
		up := httptest.NewServer(member.reply)
		t.Cleanup(up.Close)
		if err := provider.Save(provider.Provider{ID: member.id, Name: member.id, Key: "synthetic", Chat: up.URL + "/v1", Models: []string{"m"}, Contexts: map[string]int{"m": member.window}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m", "c/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"`+strings.Repeat("word ", 2400)+`"}]}`)
	route := lastRoute(s)
	// text past a's and b's windows isn't sent them at all (roomy): only
	// c, which holds it, is asked
	if code != 200 || !strings.Contains(body, "hello") || a.n != 0 || b.n != 0 || c.n != 1 || len(route.Tries) != 1 || route.Tries[0].ID != "c" {
		t.Fatalf("text overflow: %d %s (a %d, b %d, c %d), tries %+v", code, body, a.n, b.n, c.n, route.Tries)
	}
}

func payloadAnswerStream() string {
	return sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"ok","role":"assistant","content":[],"usage":{"input_tokens":1}}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)
}

func TestPayloadTooLargeErrorShapes(t *testing.T) {
	for _, proto := range []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic, provider.Gemini} {
		t.Run(string(proto), func(t *testing.T) {
			msg := "Factory: request_too_large: Request exceeds the maximum size"
			held := newHoldWriter(httptest.NewRecorder(), true)
			writeError(held, proto, 413, msg)
			if strings.Contains(string(held.errBody()), "byte limit") {
				t.Fatal("byte hint added before other members were tried")
			}
			immediate := httptest.NewRecorder()
			writeError(newHoldWriter(immediate, false), proto, 413, msg)
			if strings.Count(immediate.Body.String(), "byte limit") != 1 {
				t.Fatal("final unheld byte refusal lost its hint")
			}
			for range 2 { // another Magpie can relay a message already carrying the hint
				w := httptest.NewRecorder()
				status := writeError(w, proto, 413, msg)
				var body struct {
					Error struct {
						Type    string `json:"type"`
						Message string `json:"message"`
						Code    any    `json:"code"`
					} `json:"error"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if status != 413 || w.Code != 413 || body.Error.Code == "context_length_exceeded" || strings.Count(body.Error.Message, "byte limit") != 1 {
					t.Fatalf("byte refusal mapped to context overflow: %d %s", status, w.Body.String())
				}
				if proto == provider.Anthropic && body.Error.Type != "request_too_large" {
					t.Fatalf("wrong Anthropic error type: %s", w.Body.String())
				}
				msg = body.Error.Message
			}
		})
	}
}

// Exercise Codex's actual endpoint and a moved Factory routing group:
// Responses is translated to Messages, then the plugin's fetch returns
// a size refusal. No request goes to a real provider.
func TestMovedFactoryPayloadTooLarge(t *testing.T) {
	for _, tc := range []struct{ name, reply string }{
		{"plain", "Request Entity Too Large"},
		{"anthropic", `{"error":{"type":"request_too_large","message":"Request exceeds the maximum size"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog.Reset()
			t.Cleanup(catalog.Reset)
			var calls atomic.Int32
			const system = "Synthetic system instructions."
			toolOutput := strings.Repeat("synthetic tool output ", 256)
			movedFake(t, "factory", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil || !strings.HasSuffix(r.URL.Path, "/messages") || len(body) < 2048 {
					t.Errorf("wrong size-refusal fixture: path %s, bytes %d, error %v", r.URL.Path, len(body), err)
				}
				for _, part := range []string{system, toolOutput, pngA, `"tool_use_id":"call_1"`, `"name":"inspect_fixture"`, "continue"} {
					if !strings.Contains(string(body), part) {
						t.Errorf("size-refusal fixture lost system, schema, tool output, image or final text")
					}
				}
				t.Logf("synthetic Messages body: %d bytes; tool text: %d bytes; image base64: %d bytes", len(body), len(toolOutput), len(pngA))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				io.WriteString(w, tc.reply)
			}))
			var fallbackCalls atomic.Int32
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fallbackCalls.Add(1)
				body, _ := io.ReadAll(r.Body)
				for _, part := range []string{system, toolOutput, pngA, `"tool_use_id":"call_1"`, `"name":"inspect_fixture"`, "continue"} {
					if !strings.Contains(string(body), part) {
						t.Errorf("fallback lost system, schema, tool output, image or final text")
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, payloadAnswerStream())
			}))
			t.Cleanup(fallback.Close)
			if err := provider.Save(provider.Provider{ID: "other", Name: "Other", Key: "synthetic", Anthropic: fallback.URL, Models: []string{"m"}}); err != nil {
				t.Fatal(err)
			}
			if err := catalog.SaveLive("other", fallback.URL, []catalog.Model{{ID: "m", Images: true, ImageInput: imageInputBool(true)}}); err != nil {
				t.Fatal(err)
			}
			if err := provider.SaveGroup(provider.Group{ID: "payload", Name: "Payload", Members: []string{"factory/fake-claude", "other/m"}, Routing: provider.Ordered}); err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(map[string]any{
				"model": "group/payload", "stream": true, "instructions": system,
				"tools": []any{map[string]any{"type": "function", "name": "inspect_fixture", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}},
				"input": []any{
					map[string]any{"role": "user", "content": "Inspect the fixture."},
					map[string]any{"type": "function_call", "name": "inspect_fixture", "call_id": "call_1", "arguments": "{}"},
					map[string]any{"type": "function_call_output", "call_id": "call_1", "output": []any{
						map[string]any{"type": "input_text", "text": toolOutput},
						map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + pngA},
					}},
					map[string]any{"role": "assistant", "content": "fixture received"},
					map[string]any{"role": "user", "content": "continue"},
				},
			})
			t.Logf("synthetic Responses body: %d bytes; input items: 5; tool schemas: 1; images: 1", len(body))
			rec := httptest.NewRecorder()
			s := New()
			s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(string(body))))
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hello") || strings.Contains(rec.Body.String(), "byte limit") || calls.Load() != 1 || fallbackCalls.Load() != 1 {
				t.Fatalf("size fallback: status %d, Factory calls %d, fallback calls %d, tries %+v", rec.Code, calls.Load(), fallbackCalls.Load(), lastRoute(s).Tries)
			}
		})
	}
}

// A vendor's byte cap reaches an agent on the OpenAI routes in words pi
// knows (its overflow.ts matches request_too_large), so pi compacts and
// tries once more instead of stopping — @wilsonxuuuu on X, whose pi got
// an api_error 413 and never compacted.
func TestPayloadTooLargeSaysRequestTooLargeToPi(t *testing.T) {
	const nginx = "<html>\r\n<head><title>413 Request Entity Too Large</title></head>\r\n<body>\r\n<center><h1>413 Request Entity Too Large</h1></center>\r\n<hr><center>nginx</center>\r\n</body>\r\n</html>\r\n"
	piOverflow := regexp.MustCompile(`(?i)request_too_large`)
	fresh(t)
	scriptedOn(t, "a", provider.Chat, &scripted{replies: []reply{{413, "text/html", nginx}}})
	code, body := postAs(t, New(), "", `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 413 || !piOverflow.MatchString(body) || !strings.Contains(body, "byte limit") || strings.Contains(body, "context_length_exceeded") {
		t.Fatalf("byte cap on the chat route: %d %s", code, body)
	}
}
