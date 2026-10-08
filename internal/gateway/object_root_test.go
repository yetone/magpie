package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// rootUnionUpstream answers as Grok behind a relay does (AiHubMix's
// grok-4.7, 2026-10-08, its bytes): a request offering a function whose
// parameters have no object root is refused, else answered, on Chat or
// Responses as asked.
func rootUnionUpstream(mu *sync.Mutex, got *[]map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var q map[string]any
		json.Unmarshal(b, &q)
		mu.Lock()
		*got = append(*got, q)
		mu.Unlock()
		tools, _ := q["tools"].([]any)
		for _, t := range tools {
			tm, _ := t.(map[string]any)
			ps, _ := tm["parameters"].(map[string]any)
			if fn, ok := tm["function"].(map[string]any); ok {
				ps, _ = fn["parameters"].(map[string]any)
			}
			if ps != nil && (ps["type"] != "object" || ps["anyOf"] != nil || ps["oneOf"] != nil) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(400)
				io.WriteString(w, `{"error":{"message":"Failed to start sampling: [invalid_client_tool_schema] mcp__codex_app__automation_update: tool parameter root must be an object type (root schema is an anyOf/oneOf union with a non-object branch) (tid: 2026100808425178410684649237042)","type":"upstream_error","param":"400","code":"bad_response_status_code"}}`)
				return
			}
		}
		answer(w, r)
	}
}

// answer replies "ok" as a Chat stream or a Responses body.
func answer(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/chat/completions") {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"}}]}\n\n"+
			"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"r1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":9,"output_tokens":1}}`)
}

// Codex desktop offers codex_app's automation_update, whose parameters are
// a union at the root (the fixture is what ChatGPT.app 26.930's zod
// builds), flat as mcp__codex_app__automation_update. Grok on a relay's
// key, on its Chat API or its Responses, refused every turn ("tool
// parameter root must be an object type", #1271). It is asked again with
// the parameters folded to an object root, and the next turn goes folded
// at once; a vendor that takes the union still gets it as sent.
func TestRootUnionFoldedWhereRefused(t *testing.T) {
	schema, err := os.ReadFile("../provider/testdata/codex_automation_update_schema.json")
	if err != nil {
		t.Fatal(err)
	}
	body := `{"model":"%s","stream":false,"store":false,"tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}},` +
		`{"type":"function","name":"mcp__codex_app__automation_update","strict":false,"parameters":` + string(schema) + `}],` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
	for name, api := range map[string]string{"passed through": "responses", "translated": "chat"} {
		t.Run(name, func(t *testing.T) {
			fresh(t)
			var mu sync.Mutex
			var refusing, taking []map[string]any
			strict := httptest.NewServer(rootUnionUpstream(&mu, &refusing))
			t.Cleanup(strict.Close)
			lax := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				var q map[string]any
				json.Unmarshal(b, &q)
				mu.Lock()
				taking = append(taking, q)
				mu.Unlock()
				answer(w, r)
			}))
			t.Cleanup(lax.Close)
			for _, p := range []provider.Provider{
				{ID: "relay", Name: "Relay", Key: "k", Models: []string{"grok-4.7"}},
				{ID: "lax", Name: "Lax", Key: "k", Models: []string{"m1"}},
			} {
				url := strict.URL + "/v1"
				if p.ID == "lax" {
					url = lax.URL + "/v1"
				}
				if api == "chat" {
					p.Chat = url
				} else {
					p.Responses = url
				}
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
			}
			s := New()
			ask := func(model string) *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(strings.Replace(body, "%s", model, 1))))
				return rec
			}
			params := func(q map[string]any) map[string]any {
				for _, t := range q["tools"].([]any) {
					tm := t.(map[string]any)
					if fn, ok := tm["function"].(map[string]any); ok {
						tm = fn
					}
					if tm["name"] == "mcp__codex_app__automation_update" {
						return tm["parameters"].(map[string]any)
					}
				}
				return nil
			}
			rec := ask("relay/grok-4.7")
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ok") {
				t.Fatalf("relay: %d %s", rec.Code, rec.Body)
			}
			if len(refusing) != 2 {
				t.Fatalf("relay was asked %d times", len(refusing))
			}
			ps := params(refusing[1])
			props, _ := ps["properties"].(map[string]any)
			if ps["type"] != "object" || ps["anyOf"] != nil || props["prompt"] == nil || props["mode"] == nil {
				t.Fatalf("asked again with %v", ps)
			}
			if rec = ask("relay/grok-4.7"); rec.Code != 200 || len(refusing) != 3 || params(refusing[2])["anyOf"] != nil {
				t.Fatalf("next turn: %d, asked %d times", rec.Code, len(refusing))
			}
			if rec = ask("lax/m1"); rec.Code != 200 || len(taking) != 1 || params(taking[0])["anyOf"] == nil {
				t.Fatalf("lax: %d, asked %d times", rec.Code, len(taking))
			}
		})
	}
}

// xAI's own API takes no union at a tool's root on any model, so its
// requests go folded from the first (#1271); another vendor's go as sent
// until one is refused.
func TestRootUnionFoldedForXAI(t *testing.T) {
	fresh(t)
	s := New()
	xai := provider.Provider{ID: "xai", Chat: "https://api.x.ai/v1", Responses: "https://api.x.ai/v1"}
	other := provider.Provider{ID: "other", Chat: "https://api.example.com/v1"}
	if !s.foldsRoots(xai, "grok-4.7", provider.Responses) || !s.foldsRoots(xai, "grok-4.7", provider.Chat) {
		t.Fatal("xAI's API not folded")
	}
	if s.foldsRoots(other, "m1", provider.Chat) {
		t.Fatal("another vendor folded before any refusal")
	}
}
