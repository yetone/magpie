package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// geminiContents is the contents a Gemini-speaking route sends for r: Code
// Assist for Gemini CLI and for Antigravity, and the bare generateContent
// body (buildGemini). Zed's Google models go through buildCodeAssist too.
func geminiContents(t *testing.T, r *Request) map[string][]map[string]any {
	t.Helper()
	out := map[string][]map[string]any{}
	for _, agent := range []string{"gemini", "antigravity"} {
		var env struct {
			Request struct {
				Contents []map[string]any `json:"contents"`
			} `json:"request"`
		}
		if err := json.Unmarshal(buildCodeAssist(r, "gemini-2.5-pro", agent), &env); err != nil {
			t.Fatal(err)
		}
		out["codeassist/"+agent] = env.Request.Contents
	}
	b, err := buildGemini(r, "gemini-2.5-pro")
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Contents []map[string]any `json:"contents"`
	}
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	out["gemini"] = g.Contents
	return out
}

func geminiRoles(cs []map[string]any) string {
	var rs []string
	for _, c := range cs {
		rs = append(rs, c["role"].(string))
	}
	return strings.Join(rs, ",")
}

// A history that opens with the model's turn — what an agent's compaction
// or a cut-down context leaves — is turned away by Gemini ("Please ensure
// that function call turn comes immediately after a user turn or after a
// function response turn"). Gemini CLI's hardenHistory prepends a user turn
// saying "[Continuing from previous AI thoughts...]"; the same goes before
// it here, and the model's turn follows as it was.
func TestGeminiHistoryOpeningWithModel(t *testing.T) {
	for name, body := range map[string]string{
		"model text": `{"model":"x","messages":[
			{"role":"system","content":"be brief"},
			{"role":"assistant","content":"I looked at the repo earlier."},
			{"role":"user","content":"go on"}]}`,
		"model functionCall": `{"model":"x","messages":[
			{"role":"system","content":"be brief"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"a\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"A!"},
			{"role":"user","content":"and?"}]}`,
	} {
		r, err := parseChat([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		for route, cs := range geminiContents(t, r) {
			if got := geminiRoles(cs); got != "user,model,user" {
				t.Errorf("%s via %s: roles %s, want user,model,user", name, route, got)
				continue
			}
			lead, _ := json.Marshal(cs[0]["parts"])
			if string(lead) != `[{"text":"[Continuing from previous AI thoughts...]"}]` {
				t.Errorf("%s via %s: lead turn %s", name, route, lead)
			}
			model, _ := json.Marshal(cs[1]["parts"])
			if name == "model text" && string(model) != `[{"text":"I looked at the repo earlier."}]` {
				t.Errorf("%s via %s: model turn %s", name, route, model)
			}
			if name == "model functionCall" && !strings.Contains(string(model), `"functionCall":{"args":{"path":"a"},"id":"call_1","name":"read"}`) {
				t.Errorf("%s via %s: model turn %s", name, route, model)
			}
		}
	}
}

// A history that already opens with the user, and parallel calls an
// Anthropic client split over two assistant messages, go as they did: one
// model turn with both calls, then one user turn with both responses, and
// nothing put before them.
func TestGeminiHistoryOpeningWithUserUnchanged(t *testing.T) {
	r, err := parseAnthropic([]byte(`{"model":"x","max_tokens":100,"messages":[
		{"role":"user","content":"read a and b"},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_a","name":"read","input":{"path":"a"}}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_b","name":"read","input":{"path":"b"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_a","content":"A!"},
			{"type":"tool_result","tool_use_id":"toolu_b","content":"B!"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for route, cs := range geminiContents(t, r) {
		if got := geminiRoles(cs); got != "user,model,user" {
			t.Errorf("%s: roles %s, want user,model,user", route, got)
			continue
		}
		first, _ := json.Marshal(cs[0]["parts"])
		if string(first) != `[{"text":"read a and b"}]` {
			t.Errorf("%s: first turn %s", route, first)
		}
		if n := len(cs[1]["parts"].([]any)); n != 2 {
			t.Errorf("%s: model turn has %d parts, want both calls", route, n)
		}
		res, _ := json.Marshal(cs[2]["parts"])
		if !strings.Contains(string(res), `"id":"toolu_a"`) || !strings.Contains(string(res), `"id":"toolu_b"`) {
			t.Errorf("%s: responses %s", route, res)
		}
	}
}

// Only Gemini needs the user to speak first: a Chat, Responses or Anthropic
// upstream is sent the history as the client gave it.
func TestNonGeminiHistoryOpeningWithModelUntouched(t *testing.T) {
	r, err := parseChat([]byte(`{"model":"x","messages":[
		{"role":"assistant","content":"I looked at the repo earlier."},
		{"role":"user","content":"go on"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, proto := range []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic} {
		b, err := build(proto, r, "some-model", "example.com", false)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "Continuing from previous AI thoughts") {
			t.Errorf("%s: a user turn was put before the history:\n%s", proto, b)
		}
	}
}
