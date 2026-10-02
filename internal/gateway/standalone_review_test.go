package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestStandaloneChatOrdering(t *testing.T) {
	for _, endpoint := range []string{"/v1/responses", CodexPath + "/responses"} {
		for _, kind := range []string{"function", "custom_tool"} {
			call := func(id string) string {
				if kind == "function" {
					return fmt.Sprintf(`{"type":"function_call","call_id":%q,"name":"echo","arguments":"{}"}`, id)
				}
				return fmt.Sprintf(`{"type":"custom_tool_call","call_id":%q,"name":"echo","input":"echo"}`, id)
			}
			output := func(id, text string) string {
				return fmt.Sprintf(`{"type":%q,"call_id":%q,"output":%q}`, kind+"_call_output", id, text)
			}
			notice := fmt.Sprintf(`{"type":%q,"output":"notice"}`, kind+"_call_output")
			for _, tc := range []struct{ name, input, roles string }{
				{"before_calls", notice + "," + call("A") + "," + output("A", "result A"), "user,assistant,tool"},
				{"before_result", call("A") + "," + notice + "," + output("A", "result A"), "assistant,tool,user"},
				{"between_parallel_results", call("A") + "," + call("B") + "," + output("A", "result A") + "," + notice + "," + output("B", "result B"), "assistant,tool,tool,user"},
				{"empty_id", call("A") + "," + output("", "notice") + "," + output("A", "result A"), "assistant,tool,user"},
			} {
				t.Run(endpoint+"/"+kind+"/"+tc.name, func(t *testing.T) {
					f := &fake{t: t, reply: sse(`data: {"id":"ok","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`, `data: [DONE]`)}
					var roles []string
					f.refuse = func(body []byte) (int, string) {
						var q struct {
							Messages []struct {
								Role  string `json:"role"`
								Calls []struct {
									ID string `json:"id"`
								} `json:"tool_calls"`
								ID string `json:"tool_call_id"`
							} `json:"messages"`
						}
						if err := json.Unmarshal(body, &q); err != nil {
							t.Error(err)
							return 400, `{"error":{"message":"bad JSON"}}`
						}
						pending := map[string]bool{}
						for _, m := range q.Messages {
							roles = append(roles, m.Role)
							if len(pending) > 0 && m.Role != "tool" {
								return 400, `{"error":{"message":"interrupted tool results"}}`
							}
							if m.Role == "tool" {
								if !pending[m.ID] {
									return 400, `{"error":{"message":"unpaired result"}}`
								}
								delete(pending, m.ID)
							}
							for _, c := range m.Calls {
								pending[c.ID] = true
							}
						}
						if len(pending) > 0 {
							return 400, `{"error":{"message":"missing result"}}`
						}
						return 0, ""
					}
					setup(t, provider.Chat, f)
					body := `{"model":"fake/m1","input":[` + tc.input + `]}`
					var code int
					var response string
					if endpoint == "/v1/responses" {
						code, response = post(t, endpoint, body)
					} else {
						code, response = codexPost(t, body)
					}
					if code != 200 {
						t.Fatalf("%d: %s", code, response)
					}
					if got := strings.Join(roles, ","); got != tc.roles {
						t.Fatalf("roles %s, want %s: %s", got, tc.roles, f.got)
					}
					for _, text := range []string{"notice", "result A"} {
						if !bytes.Contains(f.got, []byte(text)) {
							t.Fatalf("lost %s: %s", text, f.got)
						}
					}
				})
			}
		}
	}
}

func TestStandaloneNativeAccounts(t *testing.T) {
	for _, kind := range []string{"function_call_output", "custom_tool_call_output"} {
		for _, id := range []string{"", `,"call_id":""`} {
			for _, tc := range []struct {
				name, path, model, pin, account string
				saved                           bool
			}{
				{"direct", CodexPath + "/responses", "gpt-5.5", "", "acct-1", false},
				{"saved", CodexPath + "/responses", "gpt-5.5", "", "acct-1", true},
				{"fixed", CodexPath + "/responses", "gpt-5.5", "spare@example.com", "acct-2", true},
				{"namespaced", CodexPath + "/responses", "codex/gpt-5.5", "spare@example.com", "acct-2", true},
				{"v1_saved", "/v1/responses", "codex/gpt-5.5", "", "", true},
				{"v1_fixed", "/v1/responses", "codex/gpt-5.5", "spare@example.com", "acct-2", true},
			} {
				for _, stream := range []bool{true, false} {
					if !stream && !tc.saved {
						continue
					}
					t.Run(fmt.Sprintf("%s/%s/%s/stream=%t", kind, id, tc.name, stream), func(t *testing.T) {
						if tc.saved {
							codexSignedIn(t, "spare@example.com")
						} else {
							fresh(t)
							provider.ForgetAccounts()
							t.Cleanup(provider.ForgetAccounts)
						}
						var got []byte
						var account string
						up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							got, _ = io.ReadAll(r.Body)
							account = r.Header.Get("chatgpt-account-id")
							w.Header().Set("Content-Type", "text/event-stream")
							io.WriteString(w, sse(`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`))
						}))
						defer up.Close()
						old := provider.CodexBase
						provider.CodexBase = up.URL + "/backend-api/codex"
						defer func() { provider.CodexBase = old }()
						body := fmt.Sprintf(`{"model":%q,"stream":%t,"input":[{"type":%q,"id":"standalone_synthetic","name":"send_message_to_thread","namespace":"codex_app","output":"native notice"%s}]}`, tc.model, stream, kind, id)
						req := httptest.NewRequest("POST", tc.path, strings.NewReader(body))
						req.Header.Set("Authorization", "Bearer chatgpt-token")
						req.Header.Set("chatgpt-account-id", "acct-1")
						req.Header.Set(AccountHeader, tc.pin)
						rec := httptest.NewRecorder()
						New().Handler().ServeHTTP(rec, req)
						if rec.Code != 200 {
							t.Fatalf("%d %s", rec.Code, rec.Body.String())
						}
						if tc.account != "" && account != tc.account {
							t.Fatalf("account %q, want %q", account, tc.account)
						}
						var sent struct {
							Input []map[string]any `json:"input"`
						}
						json.Unmarshal(got, &sent)
						if len(sent.Input) != 1 {
							t.Fatalf("input: %s", got)
						}
						item := sent.Input[0]
						if item["id"] != "standalone_synthetic" || item["type"] != kind || item["name"] != "send_message_to_thread" || item["namespace"] != "codex_app" || item["output"] != "native notice" {
							t.Fatalf("native notification changed: %v", item)
						}
						value, present := item["call_id"]
						if (id != "") != present || present && value != "" {
							t.Fatalf("native ID changed: %v", item)
						}
					})
				}
			}
		}
	}
}

func TestStandalonePlainEncoding(t *testing.T) {
	for _, kind := range []string{"function_call_output", "custom_tool_call_output"} {
		for _, body := range []string{`{ "z": "<keep>&", "input": [ { "role": "user", "content": "hi" } ], "a": 1 }`, fmt.Sprintf(`{ "input": [{"type":%q,"call_id":"A","output":"<paired>"}], "instructions":"<keep>&" }`, kind)} {
			if got := orphanedToolOutputs([]byte(body)); string(got) != body {
				t.Fatalf("no-op bytes changed: %s", got)
			}
		}
		body := []byte(fmt.Sprintf(`{ "z": 1, "instructions": "<keep>&", "input": [{"type":%q,"output":"<notice>&"}], "a": 2 }`, kind))
		got := orphanedToolOutputs(body)
		for _, literal := range []string{"<keep>&", "<notice>&"} {
			if !bytes.Contains(got, []byte(literal)) {
				t.Fatalf("escaped %s: %s", literal, got)
			}
		}
		if !bytes.Equal(got, orphanedToolOutputs(got)) {
			t.Fatal("not idempotent")
		}
	}
}

// A failed third-party attempt must not carry its normalized body into a
// native ChatGPT fallback for the same request.
func TestStandaloneNativeFallback(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	var third, native []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/v1/responses" {
			third = b
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"rate limited"}}`)
			return
		}
		native = b
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`))
	}))
	defer up.Close()
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = old }()
	if err := provider.Save(provider.Provider{ID: "third", Name: "Third", Key: "synthetic", Models: []string{"m1"}, Responses: up.URL + "/v1", Fallback: []string{"codex/gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	code, response := post(t, "/v1/responses", `{"model":"third/m1","stream":true,"input":[{"type":"function_call_output","name":"send_message_to_thread","namespace":"codex_app","output":"notice"}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, response)
	}
	if !bytes.Contains(third, []byte(`"role":"user"`)) || !bytes.Contains(native, []byte(`"type":"function_call_output"`)) || !bytes.Contains(native, []byte(`"namespace":"codex_app"`)) {
		t.Fatalf("third=%s native=%s", third, native)
	}
}
