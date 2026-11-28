package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

const copilotInputEnumRefusal = `{"error":{"message":"Copilot: Invalid value: 'other'. Supported values are: 'additional_tools', 'agent_message', 'apply_patch_call', 'message', 'reasoning', and 'web_search_call'.","param":"input[1].type"}}`

func TestCodexOtherItemsNotSentToVendors(t *testing.T) {
	const body = `{"model":"m1","reasoning":{"effort":"low"},"input":[{"type":"message","role":"user","content":"keep other as text"},{"type":"other"},{"type":"configuration_update","reasoning":{"effort":"high"}},{"type":"function_call","name":"exec","call_id":"c1","arguments":"{\"type\":\"other\"}"}]}`
	vendor := provider.Provider{Responses: "https://relay.example/v1"}
	for _, compact := range []bool{false, true} {
		got := forVendor(vendor, []byte(body), compact)
		var q struct {
			Input     []map[string]any `json:"input"`
			Reasoning map[string]any   `json:"reasoning"`
		}
		if err := json.Unmarshal(got, &q); err != nil {
			t.Fatal(err)
		}
		if len(q.Input) != 2 || q.Input[0]["content"] != "keep other as text" || q.Input[1]["arguments"] != `{"type":"other"}` || q.Reasoning["effort"] != "high" {
			t.Errorf("compact %t: vendor received %s", compact, got)
		}
	}
	for _, p := range []provider.Provider{
		{Responses: "https://api.openai.com/v1"},
		{Account: &provider.Account{Agent: "codex"}},
	} {
		if got := forVendor(p, []byte(body), false); string(got) != body {
			t.Errorf("OpenAI received a changed history: %s", got)
		}
	}
	for _, body := range []string{
		`{"input":[{"type":"message","role":"user","content":"other"}]}`,
		`{"input":[{"type":"function_call","name":"other","arguments":"{\"type\":\"other\"}"}]}`,
	} {
		if got := forVendor(vendor, []byte(body), false); string(got) != body {
			t.Errorf("ordinary text or call changed: %s", got)
		}
	}
	// With no other OpenAI-only field, the fast scan must still find the item
	if got := forVendor(vendor, []byte(`{"input":[{"type":"other"}]}`), false); string(got) != `{"input":[]}` {
		t.Errorf("other-only input: %s", got)
	}
}

func TestCopilotInputEnumRefusalIsNarrow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		err    error
		want   bool
	}{
		{"bad request", 400, copilotInputEnumRefusal, nil, true},
		{"unprocessable", 422, copilotInputEnumRefusal, nil, true},
		{"remote magpie", 502, "Remote magpie: " + copilotInputEnumRefusal, nil, true},
		{"stream", 200, copilotInputEnumRefusal, errors.New("stream refused"), true},
		{"successful text", 200, copilotInputEnumRefusal, nil, false},
		{"unauthorized", 401, copilotInputEnumRefusal, nil, false},
		{"forbidden", 403, copilotInputEnumRefusal, nil, false},
		{"rate limit", 429, copilotInputEnumRefusal, nil, false},
		{"effort enum", 400, `{"error":{"message":"Invalid value: 'ultra'. Supported values are: 'low', 'medium', 'high'."}}`, nil, false},
		{"message outside enum", 400, `{"error":{"message":"Invalid value: 'ultra'. Supported values are: 'low', 'high'. The 'message' field is valid."}}`, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{status: tc.status}
			rec.body.WriteString(tc.body)
			if got := inputKindRefused(rec, tc.err); got != tc.want {
				t.Errorf("input kind refusal = %t, want %t: %s", got, tc.want, tc.body)
			}
		})
	}
}

func TestCodexOtherHistoryReachesVendorOnFirstTry(t *testing.T) {
	f := &fake{t: t, reply: wholeSummary(provider.Responses)}
	f.refuse = func(b []byte) (int, string) {
		if strings.Contains(string(b), `"type":"other"`) {
			return http.StatusBadRequest, copilotInputEnumRefusal
		}
		return 0, ""
	}
	setup(t, provider.Responses, f)
	code, body := post(t, "/v1/responses", `{"model":"fake/m1","stream":true,"input":[{"type":"message","role":"user","content":"hello"},{"type":"other"}]}`)
	if code != 200 || f.calls != 1 || strings.Contains(string(f.got), `"type":"other"`) {
		t.Fatalf("status %d, %d calls, sent %s, reply %s", code, f.calls, f.got, body)
	}
}

func TestCodexCompactCopilotEnumAsksAgainAsText(t *testing.T) {
	f := &fake{t: t}
	f.refuse = func(b []byte) (int, string) {
		f.reply = wholeSummary(provider.Responses)
		if !commandCodeTakes(t, b) {
			return http.StatusBadRequest, copilotInputEnumRefusal
		}
		return 0, ""
	}
	setup(t, provider.Responses, f)
	code, body := codexPost(t, compactSubagentRequest)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := compactedText(t, body); got != "SUMMARY" {
		t.Errorf("summary %q, want the model's summary", got)
	}
	if f.calls != 2 || !strings.Contains(string(f.got), "Compare Vault secret rotation") {
		t.Errorf("calls %d, summary request %s", f.calls, f.got)
	}
}
