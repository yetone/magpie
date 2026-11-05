package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #5315 (cherish_onlyy): Hermes with prompt_caching.cache_ttl: 1h, on
// anthropic_messages, marks its system prompt and the last three messages
// itself (agent/prompt_caching.py's system_and_3, each
// {"type":"ephemeral","ttl":"1h"}). Those marks reach the vendor as sent,
// every one at an hour. On chat completions Hermes sends no marks, and
// the ones magpie adds for a Chat client last five minutes, which is why
// magpie now connects a Claude model to Hermes on Messages.
func TestHermesHourMarksReachTheVendor(t *testing.T) {
	f := &fake{t: t, reply: anthropicAnswer}
	up := setup(t, provider.Anthropic, f)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"claude-sonnet-5"}, Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	srv := New()
	hour := `{"type":"ephemeral","ttl":"1h"}`
	code, body := postTo(t, srv, "/v1/messages", `{"model":"fake/claude-sonnet-5","max_tokens":64,"stream":false,
	  "system":[{"type":"text","text":"You are Hermes Agent.","cache_control":`+hour+`}],
	  "tools":[{"name":"terminal","description":"run a command","input_schema":{"type":"object"}}],
	  "messages":[
	    {"role":"user","content":[{"type":"text","text":"list files"}]},
	    {"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"terminal","input":{"command":"ls"}}]},
	    {"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"a.go b.go","cache_control":`+hour+`}]},
	    {"role":"assistant","content":[{"type":"text","text":"two files","cache_control":`+hour+`}]},
	    {"role":"user","content":[{"type":"text","text":"read a.go","cache_control":`+hour+`}]}]}`)
	if code != 200 {
		t.Fatalf("%d: %s", code, body)
	}
	ttls := markTTLs(t, f.got)
	if len(ttls) < 4 {
		t.Fatalf("%d marks upstream: %s", len(ttls), f.got)
	}
	for _, ttl := range ttls {
		if ttl != "1h" {
			t.Fatalf("marks upstream %q, want every one 1h: %s", ttls, f.got)
		}
	}

	// the same turn on chat completions, as magpie connected Hermes before:
	// no marks from Hermes, and magpie's last five minutes
	code, body = postTo(t, srv, "/v1/chat/completions", `{"model":"fake/claude-sonnet-5","max_tokens":64,"stream":false,
	  "tools":[{"type":"function","function":{"name":"terminal","parameters":{"type":"object"}}}],
	  "messages":[{"role":"system","content":"You are Hermes Agent."},{"role":"user","content":"list files"}]}`)
	if code != 200 {
		t.Fatalf("chat: %d: %s", code, body)
	}
	for _, ttl := range markTTLs(t, f.got) {
		if ttl != "" {
			t.Fatalf("chat marks %q: %s", ttl, f.got)
		}
	}
	if strings.Contains(string(f.got), `"1h"`) {
		t.Fatalf("chat went at an hour: %s", f.got)
	}
}
