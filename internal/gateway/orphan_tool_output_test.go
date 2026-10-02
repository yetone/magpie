package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Exercise both real ingress handlers and the two Responses forwarding paths.
// A search-capable provider relays; offering web_search to one that cannot
// search makes the gateway translate even when its upstream is Responses.
func TestStandaloneToolOutputResponsesRoutes(t *testing.T) {
	for _, endpoint := range []string{"/v1/responses", CodexPath + "/responses"} {
		for _, relay := range []bool{true, false} {
			route := "translated"
			if relay {
				route = "relayed"
			}
			for _, tc := range []struct {
				name, input string
				standalone  bool
			}{
				{"ordinary", `[{"type":"message","role":"user","content":[{"type":"input_text","text":"ordinary"}]}]`, false},
				{"paired", `[{"type":"function_call","call_id":"call_synthetic","name":"echo","arguments":"{}"},{"type":"function_call_output","call_id":"call_synthetic","output":"paired result"}]`, false},
				{"standalone", `[{"type":"function_call","call_id":"call_synthetic","name":"echo","arguments":"{}"},{"type":"function_call_output","call_id":"call_synthetic","output":"paired result"},{"type":"function_call_output","id":"fco_synthetic","name":"send_message_to_thread","namespace":"codex_app","output":"<codex_delegation>synthetic delivery</codex_delegation>"}]`, true},
				{"custom_missing", `[{"type":"custom_tool_call_output","output":"synthetic delivery"}]`, true},
				{"custom_empty", `[{"type":"custom_tool_call_output","call_id":"","output":"synthetic delivery"}]`, true},
				{"empty_id", `[{"type":"function_call_output","call_id":"","output":"synthetic delivery"}]`, true},
			} {
				t.Run(endpoint+"/"+route+"/"+tc.name, func(t *testing.T) {
					f := &fake{t: t, reply: sse(`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`)}
					f.refuse = func(body []byte) (int, string) {
						// Use the wire spelling of call_id, not Go's default field matching.
						var sent struct {
							Input []map[string]any `json:"input"`
						}
						if err := json.Unmarshal(body, &sent); err != nil {
							t.Fatal(err)
						}
						for _, item := range sent.Input {
							if item["type"] == "function_call_output" && (item["call_id"] == nil || item["call_id"] == "") {
								return 400, `{"error":{"message":"call_id required"}}`
							}
						}
						return 0, ""
					}
					setup(t, provider.Responses, f)
					p, err := provider.Find("fake")
					if err != nil {
						t.Fatal(err)
					}
					p.Searches = relay
					if err := provider.Save(*p); err != nil {
						t.Fatal(err)
					}
					body := `{"model":"fake/m1","stream":true,"tools":[{"type":"web_search"}],"input":` + tc.input + `}`
					var code int
					var response string
					if endpoint == CodexPath+"/responses" {
						code, response = codexPost(t, body)
					} else {
						code, response = post(t, endpoint, body)
					}
					if code != 200 {
						t.Fatalf("status %d: %s", code, response)
					}
					var sent struct {
						Input []map[string]any `json:"input"`
					}
					if err := json.Unmarshal(f.got, &sent); err != nil {
						t.Fatal(err)
					}
					calls, outputs := 0, 0
					for _, item := range sent.Input {
						switch item["type"] {
						case "function_call":
							calls++
							if item["call_id"] != "call_synthetic" {
								t.Fatalf("call changed: %v", item)
							}
						case "function_call_output":
							outputs++
							if item["call_id"] != "call_synthetic" || item["output"] != "paired result" {
								t.Fatalf("paired result changed: %v", item)
							}
						}
					}
					if tc.name == "paired" || tc.name == "standalone" {
						if calls != 1 || outputs != 1 {
							t.Fatalf("lost pair: %s", f.got)
						}
					} else if calls != 0 || outputs != 0 {
						t.Fatalf("invented pair: %s", f.got)
					}
					if tc.standalone {
						if !strings.Contains(string(f.got), "synthetic delivery") {
							t.Fatalf("lost delivery: %s", f.got)
						}
						last := sent.Input[len(sent.Input)-1]
						if last["type"] != "message" || last["role"] != "user" {
							t.Fatalf("not user context: %v", last)
						}
					} else if tc.name == "ordinary" && !strings.Contains(string(f.got), "ordinary") {
						t.Fatalf("lost ordinary message: %s", f.got)
					}
				})
			}
		}
	}
}

func TestOrphanToolOutputsContentAndNoops(t *testing.T) {
	for _, body := range []string{`not json`, `{}`, `{"input":"hello"}`, `{"input":[{"type":"function_call_output","call_id":"paired","output":"ok"}]}`, `{"input":[{"role":"user","content":"ordinary"}]}`} {
		if got := orphanedToolOutputs([]byte(body)); string(got) != body {
			t.Fatalf("unchanged input rewritten: %s", got)
		}
	}
	body := []byte(`{"model":"fake/m1","previous_response_id":"resp_synthetic","input":[{"type":"function_call_output","output":[{"type":"text","text":"look"},{"type":"input_image","image_url":"data:image/png;base64,AQID"}]},{"type":"function_call_output","output":""}]}`)
	original := bytes.Clone(body)
	fixed := orphanedToolOutputs(body)
	if !bytes.Equal(body, original) {
		t.Fatal("mutated caller's request buffer")
	}
	var q struct {
		Previous string `json:"previous_response_id"`
		Input    []struct {
			Content []map[string]any `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(fixed, &q); err != nil {
		t.Fatal(err)
	}
	if q.Previous != "resp_synthetic" || len(q.Input) != 2 || len(q.Input[0].Content) != 2 {
		t.Fatalf("lost context: %s", fixed)
	}
	if q.Input[0].Content[0]["text"] != "look" || q.Input[0].Content[1]["image_url"] != "data:image/png;base64,AQID" || q.Input[1].Content[0]["text"] != "Tool result received." {
		t.Fatalf("lost output content: %s", fixed)
	}
	if !bytes.Equal(orphanedToolOutputs(fixed), fixed) {
		t.Fatal("normalization is not idempotent")
	}
}

// OpenAI's native backend supports standalone outputs; do not rewrite that
// path merely because Codex happened to send it through openai_base_url.
func TestCodexNativeStandaloneOutputUnchanged(t *testing.T) {
	var got []byte
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`))
	})
	body := `{"model":"gpt-5.5","stream":true,"input":[{"type":"function_call_output","name":"send_message_to_thread","namespace":"codex_app","output":"synthetic native notification"}]}`
	code, response := codexPost(t, body)
	if code != 200 {
		t.Fatalf("%d %s", code, response)
	}
	var q struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(got, &q); err != nil {
		t.Fatal(err)
	}
	if len(q.Input) != 1 || q.Input[0]["type"] != "function_call_output" || q.Input[0]["name"] != "send_message_to_thread" || q.Input[0]["namespace"] != "codex_app" {
		t.Fatalf("native notification changed: %s", got)
	}
	if _, ok := q.Input[0]["call_id"]; ok {
		t.Fatalf("native call ID invented: %s", got)
	}
}
