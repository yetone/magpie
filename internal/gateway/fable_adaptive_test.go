package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// alwaysAdaptiveVendor answers as Anthropic documents for Claude Fable and
// Mythos 5 and 5.1: adaptive thinking is always on, and thinking.type
// enabled with budget_tokens returns a 400
// (platform.claude.com/docs/en/models/fable-5-1/whats-new-fable-5-1).
type alwaysAdaptiveVendor struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (v *alwaysAdaptiveVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	json.Unmarshal(b, &m)
	v.mu.Lock()
	v.bodies = append(v.bodies, m)
	v.mu.Unlock()
	th, _ := m["thinking"].(map[string]any)
	if th["type"] == "enabled" {
		w.WriteHeader(400)
		io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"thinking.type.enabled is not supported for this model. Use thinking.type.adaptive and output_config.effort to control thinking behavior."}}`)
		return
	}
	model, _ := m["model"].(string)
	if m["stream"] == true {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\""+model+"\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"+
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n"+
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"+
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"`+model+`","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
}

func (v *alwaysAdaptiveVendor) last() map[string]any {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.bodies[len(v.bodies)-1]
}

// Claude Fable and Mythos think adaptively only, as Opus 5.5 does: a budget
// relayed from an agent, or one magpie builds from a Chat client's
// reasoning_effort, goes as thinking.type=adaptive with the effort in
// output_config, and agents told how to ask a model are told so.
func TestFableAndMythosNeverGetABudget(t *testing.T) {
	for _, model := range []string{"claude-fable-5-2", "global.anthropic.claude-fable-5-2", "claude-fable-5-1", "claude-fable-5", "claude-mythos-5-1", "claude-mythos-5", "anthropic.claude-fable-5-1"} {
		if !adaptiveOnly(model) {
			t.Errorf("adaptiveOnly(%q) = false", model)
		}
	}

	fresh(t)
	up := &alwaysAdaptiveVendor{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "anth", Name: "Anth", Key: "k", Anthropic: srv.URL,
		Models: []string{"claude-fable-5-1", "claude-mythos-5"}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, path, body, effort, thinking string }{
		{"budget relayed", "/v1/messages",
			`{"model":"anth/claude-fable-5-1","max_tokens":32000,"thinking":{"type":"enabled","budget_tokens":10000},"messages":[{"role":"user","content":"hi"}]}`,
			"medium", `{"display":"summarized","type":"adaptive"}`},
		{"chat's effort", "/v1/chat/completions",
			`{"model":"anth/claude-fable-5-1","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`,
			"high", `{"display":"summarized","type":"adaptive"}`},
		{"mythos, budget relayed", "/v1/messages",
			`{"model":"anth/claude-mythos-5","max_tokens":32000,"thinking":{"type":"enabled","budget_tokens":4000},"messages":[{"role":"user","content":"hi"}]}`,
			"low", `{"display":"summarized","type":"adaptive"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, body := post(t, c.path, c.body)
			if code != 200 {
				t.Fatalf("status %d: %s", code, body)
			}
			got := up.last()
			if th, _ := json.Marshal(got["thinking"]); string(th) != c.thinking {
				t.Errorf("thinking = %s, want %s", th, c.thinking)
			}
			oc, _ := got["output_config"].(map[string]any)
			if e, _ := oc["effort"].(string); e != c.effort {
				t.Errorf("output_config = %v, want effort %q", got["output_config"], c.effort)
			}
		})
	}
}
