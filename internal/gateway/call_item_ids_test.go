package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A custom tool's call a translated vendor's model made (Codex's
// apply_patch) goes back to Codex with a ctc_ id, the one kind OpenAI takes
// for it; its call_id is the model's own.
func TestCustomToolCallHasCtcID(t *testing.T) {
	req := &Request{Model: "m", Namespaced: map[string]nsTool{"apply_patch": {Custom: true, Name: "apply_patch"}}}
	rec := httptest.NewRecorder()
	enc := encoder(provider.Responses, newSSEWriter(rec), req)
	enc.event(Event{Kind: KToolStart, ID: "p1", Name: "apply_patch"})
	enc.event(Event{Kind: KToolArgs, Text: `{"input":"*** Begin Patch"}`})
	enc.finish()
	var item map[string]any
	for _, ev := range events(rec.Body.String()) {
		if ev["type"] == "response.output_item.done" {
			item = ev["item"].(map[string]any)
		}
	}
	if id, _ := item["id"].(string); item["type"] != "custom_tool_call" || !strings.HasPrefix(id, "ctc_") || item["call_id"] != "p1" {
		t.Fatalf("streamed = %v", item)
	}

	res := Result{Parts: []Part{{Kind: ToolCall, ID: "p2", Name: "apply_patch", Args: json.RawMessage(`{"input":"x"}`)}}}
	var out struct {
		Output []map[string]any `json:"output"`
	}
	if err := json.Unmarshal(render(provider.Responses, res, req), &out); err != nil {
		t.Fatal(err)
	}
	if id, _ := out.Output[0]["id"].(string); len(out.Output) != 1 || out.Output[0]["type"] != "custom_tool_call" || !strings.HasPrefix(id, "ctc_") {
		t.Fatalf("rendered = %v", out.Output)
	}
}

// openaiIDs answers as OpenAI does: a 400 for a tool_search_call or a
// custom_tool_call whose id isn't of its own kind, else a turn.
func openaiIDs(got *[]string) http.HandlerFunc {
	kinds := map[any]string{"tool_search_call": "tsc", "custom_tool_call": "ctc"}
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*got = append(*got, string(b))
		var q struct {
			Input []map[string]any `json:"input"`
		}
		json.Unmarshal(b, &q)
		for i, it := range q.Input {
			if id, _ := it["id"].(string); kinds[it["type"]] != "" && id != "" && !strings.HasPrefix(id, kinds[it["type"]]) {
				n := strconv.Itoa(i)
				w.WriteHeader(400)
				io.WriteString(w, `{"error":{"message":"Invalid 'input[`+n+`].id': '`+id+`'. Expected an ID that begins with '`+kinds[it["type"]]+`'.","type":"invalid_request_error","param":"input[`+n+`].id","code":"invalid_value"}}`)
				return
			}
		}
		w.Header()["Content-Type"] = nil
		io.WriteString(w, sse(`data: {"type":"response.output_text.delta","delta":"on it"}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"on it"}]}}`,
			`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`))
	}
}

// otherVendorsCalls is a conversation that searched and patched on another
// vendor's model (fc_ ids) and patched on OpenAI's (a ctc_ one).
const otherVendorsCalls = `[{"type":"message","role":"user","content":[{"type":"input_text","text":"fix it"}]},
  {"type":"tool_search_call","id":"fc_18da5b9a10256a3e00000011","call_id":"s1","status":"completed","execution":"client","arguments":{"query":"mail"}},
  {"type":"tool_search_output","call_id":"s1","status":"completed","execution":"client","tools":[]},
  {"type":"custom_tool_call","id":"fc_18da5b9a10256a3e00000012","call_id":"p1","name":"apply_patch","input":"*** Begin Patch"},
  {"type":"custom_tool_call_output","call_id":"p1","output":"done"},
  {"type":"custom_tool_call","id":"ctc_openai","call_id":"p2","name":"apply_patch","input":"x"},
  {"type":"custom_tool_call_output","call_id":"p2","output":"done"}]`

// sentAsOpenAI is what the upstream must have been given: the calls' ids
// of their own kinds, the call_ids their outputs name them by as they came.
var sentAsOpenAI = []string{`"tsc_18da5b9a10256a3e00000011"`, `"ctc_18da5b9a10256a3e00000012"`, `"ctc_openai"`,
	`"call_id":"s1"`, `"call_id":"p1"`, `"call_id":"p2"`, "fix it"}

// The same conversation going on through a magpie model or a routing group
// to OpenAI's API on a key (Lullaby on Discord: "Invalid 'input[155].id':
// 'fc_18da5b9a10256a3e00000011'. Expected an ID that begins with 'tsc'",
// and 'ctc' after it, Codex's auto-compaction, a compaction_trigger turn
// there, failing with them) goes with those ids. A ChatGPT account's
// request keeps no ids at all (provider.codexInput).
func TestMagpieModelOpenAICallIDs(t *testing.T) {
	fresh(t)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	var got []string
	// OpenAI, reached through the provider's proxy so the provider's host
	// is OpenAI's own
	openai := httptest.NewServer(openaiIDs(&got))
	t.Cleanup(openai.Close)
	if err := provider.Save(provider.Provider{ID: "openai", Name: "OpenAI", Key: "k", Models: []string{"gpt-5.5"},
		Responses: "http://api.openai.com/v1", Proxy: openai.URL}); err != nil {
		t.Fatal(err)
	}
	refusalGroup(t, "openai/gpt-5.5")
	s := New()
	for _, model := range []string{"openai/gpt-5.5", "group/g"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"`+model+`","stream":true,"store":false,"input":`+otherVendorsCalls+`}`))
		req.Header.Set("Authorization", "Bearer chatgpt-token")
		req.Header.Set("chatgpt-account-id", "acct-1")
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "on it") || len(got) == 0 {
			t.Fatalf("%s: %d %s", model, rec.Code, rec.Body)
		}
		last := got[len(got)-1]
		for _, want := range sentAsOpenAI {
			if !strings.Contains(last, want) {
				t.Errorf("%s: upstream lacks %s: %s", model, want, last)
			}
		}
	}
}

func TestResponsesRelayCallIDs(t *testing.T) {
	fresh(t)
	var got []string
	relay := httptest.NewServer(openaiIDs(&got))
	t.Cleanup(relay.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"m1"},
		Responses: relay.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	refusalGroup(t, "relay/m1")
	s := New()
	for _, model := range []string{"relay/m1", "group/g"} {
		for _, tc := range []struct {
			name    string
			path    string
			compact bool
		}{
			{"responses", "/v1/responses", false},
			{"codex", CodexPath + "/responses", false},
			{"compact", CodexPath + "/responses", true},
		} {
			t.Run(model+"/"+tc.name, func(t *testing.T) {
				input := otherVendorsCalls
				if tc.compact {
					input = strings.TrimSuffix(input, "]") + `,{"type":"compaction_trigger"}]`
				}
				body := `{"model":"` + model + `","stream":true,"store":false,"tools":[{"type":"custom","name":"apply_patch"}],"input":` + input + `}`
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("POST", tc.path, strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer chatgpt-token")
				req.Header.Set("chatgpt-account-id", "acct-1")
				before := len(got)
				s.Handler().ServeHTTP(rec, req)
				if rec.Code != 200 || len(got) != before+1 {
					t.Fatalf("status %d, upstream calls %d: %s", rec.Code, len(got)-before, rec.Body)
				}
				last := got[len(got)-1]
				for _, want := range sentAsOpenAI {
					if !strings.Contains(last, want) {
						t.Errorf("upstream lacks %s: %s", want, last)
					}
				}
				if tc.compact {
					var sent map[string]json.RawMessage
					if err := json.Unmarshal([]byte(last), &sent); err != nil {
						t.Fatal(err)
					}
					if sent["tools"] != nil || !strings.Contains(last, "CONTEXT CHECKPOINT COMPACTION") {
						t.Errorf("not a compaction request: %s", last)
					}
					if !strings.Contains(rec.Body.String(), `"type":"compaction"`) || !strings.Contains(rec.Body.String(), magpieCompaction) {
						t.Errorf("not a compaction response: %s", rec.Body)
					}
				} else if !strings.Contains(rec.Body.String(), "on it") {
					t.Errorf("missing response text: %s", rec.Body)
				}
			})
		}
	}
}

// Codex's own model, on a turn and on a compaction, gives a custom tool's
// call another vendor answered a ctc_ id too.
func TestCodexOwnModelCustomCallIDs(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	var got []string
	chatgpt(t, openaiIDs(&got))
	for _, path := range []string{"/responses", "/responses/compact"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", CodexPath+path, strings.NewReader(`{"model":"gpt-5.5","stream":true,"store":false,"input":`+otherVendorsCalls+`}`))
		req.Header.Set("Authorization", "Bearer chatgpt-token")
		req.Header.Set("chatgpt-account-id", "acct-1")
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		last := got[len(got)-1]
		for _, want := range sentAsOpenAI {
			if !strings.Contains(last, want) {
				t.Errorf("%s: upstream lacks %s: %s", path, want, last)
			}
		}
	}
}

// A custom tool's call already OpenAI's leaves the request byte for byte.
func TestCustomCallIDsUntouched(t *testing.T) {
	b := `{"model":"gpt-5.5",  "input":[{"type":"custom_tool_call","id":"ctc_1","call_id":"p","name":"apply_patch","input":"x"},{"type":"custom_tool_call","call_id":"q","name":"apply_patch","input":"y"}]}`
	if got := callItemIDs([]byte(b)); string(got) != b {
		t.Errorf("%s -> %s", b, got)
	}
}
