package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// displayStrictVendor stands in for an Anthropic-compatible endpoint whose
// non-Claude models take thinking enabled with a budget but turn
// thinking.display away with a 400, as Alibaba Cloud's did for a Qwen
// model (StudyingPython on X). The refusal's words here are a stand-in:
// Alibaba's own weren't seen.
type displayStrictVendor struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (v *displayStrictVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	json.Unmarshal(b, &m)
	v.mu.Lock()
	v.bodies = append(v.bodies, m)
	v.mu.Unlock()
	th, _ := m["thinking"].(map[string]any)
	model, _ := m["model"].(string)
	w.Header().Set("Content-Type", "application/json")
	if _, ok := th["display"]; ok && !strings.Contains(model, "claude") {
		w.WriteHeader(400)
		io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"<400> InternalError.Algo.InvalidParameter: thinking.display is not supported"}}`)
		return
	}
	if m["stream"] == true {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, anthropicSSE("ok"))
		return
	}
	io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"`+model+`","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
}

func (v *displayStrictVendor) last() map[string]any {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.bodies) == 0 {
		return nil
	}
	return v.bodies[len(v.bodies)-1]
}

// DeepSeek Harness's client (pi-ai's anthropic-messages) asks every model
// it takes to reason for thinking enabled with a budget and display
// "summarized". display is Claude's alone, and Alibaba Cloud's Anthropic
// endpoint answered a Qwen model's request with a 400. A non-Claude model
// is sent its budget without display; a Claude, relayed or translated,
// keeps the display it is asked (#1485), and a display the user put in the
// provider's own fields is sent as they wrote it.
func TestDisplayGoesToClaudeOnly(t *testing.T) {
	fresh(t)
	up := &displayStrictVendor{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "bailian", Name: "Bailian Token Plan", Key: "k", Preset: "bailian-token-plan",
		Anthropic: srv.URL + "/apps/anthropic", Models: []string{"qwen3.7-plus", "glm-5", "kimi-k2.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Anthropic: srv.URL,
		Models: []string{"claude-opus-4-7", "claude-sonnet-4-5", "claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	piAI := func(model string) string {
		return `{"model":"` + model + `","max_tokens":32000,"stream":false,"thinking":{"type":"enabled","budget_tokens":1024,"display":"summarized"},"messages":[{"role":"user","content":"hi"}]}`
	}
	for _, c := range []struct {
		name, path, body, thinking string
	}{
		{"Qwen on Bailian", "/v1/messages", piAI("bailian/qwen3.7-plus"), `{"budget_tokens":1024,"type":"enabled"}`},
		{"GLM on Bailian", "/v1/messages", piAI("bailian/glm-5"), `{"budget_tokens":1024,"type":"enabled"}`},
		{"Kimi on Bailian, adaptive asked", "/v1/messages",
			`{"model":"bailian/kimi-k2.5","max_tokens":32000,"thinking":{"type":"adaptive","display":"summarized"},"messages":[{"role":"user","content":"hi"}]}`,
			`{"type":"adaptive"}`},
		{"an older Claude keeps its display", "/v1/messages", piAI("relay/claude-sonnet-4-5"), `{"budget_tokens":1024,"display":"summarized","type":"enabled"}`},
		{"an adaptive Claude keeps it", "/v1/messages", piAI("relay/claude-opus-4-7"), `{"display":"summarized","type":"adaptive"}`},
		{"a Responses client's summary to Claude", "/v1/responses",
			`{"model":"relay/claude-opus-5-5","reasoning":{"effort":"high","summary":"auto"},"input":[{"role":"user","content":"hi"}]}`,
			`{"display":"summarized","type":"adaptive"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			if code, body := post(t, c.path, c.body); code != 200 {
				t.Fatalf("status %d: %s", code, body)
			}
			if th, _ := json.Marshal(up.last()["thinking"]); string(th) != c.thinking {
				t.Errorf("thinking = %s, want %s", th, c.thinking)
			}
		})
	}

	// a display the user forced in the provider's own fields is theirs to send
	withExtraBody(t, "bailian", `{"!thinking":{"type":"enabled","budget_tokens":2048,"display":"omitted"}}`)
	post(t, "/v1/messages", piAI("bailian/qwen3.7-plus"))
	if th, _ := json.Marshal(up.last()["thinking"]); string(th) != `{"budget_tokens":2048,"display":"omitted","type":"enabled"}` {
		t.Errorf("with the user's own fields, thinking = %s", th)
	}
}
