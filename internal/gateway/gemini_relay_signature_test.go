package gateway

import (
	"context"
	"net"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A relay serving Gemini on chat completions under a host of its own
// (Mamba_asr on X: "peta-gemini", gemini-4-argon) gives a call's thought
// signature in extra_content as Google's API does, and turns the next
// turn away without it: 400 "missing a thought_signature". magpie gave
// the signature back to Gemini's own host and to a local one only, so
// on the relay every second tool turn failed, from Claude Code, Codex
// and a Chat client that keeps no extra_content alike. A signature goes
// back to the upstream that gave it, whatever its host.
func TestRelayGeminiSignatureGoesBack(t *testing.T) {
	const sig = "CiQBcGV0YS1nZW1pbmktc2lnbmF0dXJlLWJ5dGVzLS0+Pz8/Pw=="
	tools := map[string]string{
		"/v1/chat/completions": `[{"type":"function","function":{"name":"bash","parameters":{"type":"object"}}}]`,
		"/v1/messages":         `[{"name":"bash","input_schema":{"type":"object"}}]`,
		"/v1/responses":        `[{"type":"function","name":"bash","parameters":{"type":"object"}}]`,
	}
	first := map[string]string{
		"/v1/chat/completions": `{"model":"peta-gemini/gemini-4-argon","stream":%s,"messages":[{"role":"user","content":"list files"}],"tools":%t}`,
		"/v1/messages":         `{"model":"peta-gemini/gemini-4-argon","max_tokens":1000,"stream":false,"messages":[{"role":"user","content":"list files"}],"tools":%t}`,
		"/v1/responses":        `{"model":"peta-gemini/gemini-4-argon","stream":false,"input":[{"role":"user","content":"list files"}],"tools":%t}`,
	}
	// the next turn as each client keeps the call: its id, and no
	// extra_content
	second := map[string]string{
		"/v1/chat/completions": `{"model":"peta-gemini/gemini-4-argon","stream":true,"messages":[
			{"role":"user","content":"list files"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"%id","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}}]},
			{"role":"tool","tool_call_id":"%id","content":"a.txt"}],"tools":%t}`,
		"/v1/messages": `{"model":"peta-gemini/gemini-4-argon","max_tokens":1000,"stream":false,"messages":[
			{"role":"user","content":"list files"},
			{"role":"assistant","content":[{"type":"tool_use","id":"%id","name":"bash","input":{"command":"ls"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"%id","content":"a.txt"}]}],"tools":%t}`,
		"/v1/responses": `{"model":"peta-gemini/gemini-4-argon","stream":false,"input":[
			{"role":"user","content":"list files"},
			{"type":"function_call","call_id":"%id","name":"bash","arguments":"{\"command\":\"ls\"}"},
			{"type":"function_call_output","call_id":"%id","output":"a.txt"}],"tools":%t}`,
	}
	idIn := regexp.MustCompile(`"(?:id|call_id)":"(function-call-7[^"]*)"`)
	type run struct {
		path   string
		stream bool
	}
	for _, c := range []run{{"/v1/chat/completions", true}, {"/v1/chat/completions", false}, {"/v1/messages", true}, {"/v1/responses", true}} {
		t.Run(strings.TrimPrefix(c.path, "/v1/")+map[bool]string{true: "-streamed", false: "-whole"}[c.stream], func(t *testing.T) {
			up, bodies := geminiSigUpstream(t, sig, c.stream)
			fresh(t)
			p := provider.Provider{ID: "peta-gemini", Name: "peta-gemini", Key: "k", Models: []string{"gemini-4-argon"}, Chat: "http://api.peta.example.com/v1"}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			s := New()
			addr := strings.TrimPrefix(up.URL, "http://")
			s.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			}}}
			fill := func(b, id string) string {
				b = strings.ReplaceAll(b, "%t", tools[c.path])
				b = strings.ReplaceAll(b, "%s", map[bool]string{true: "true", false: "false"}[c.stream])
				return strings.ReplaceAll(b, "%id", id)
			}
			out := sigPost(t, s, c.path, fill(first[c.path], ""))
			m := idIn.FindStringSubmatch(out)
			if m == nil {
				t.Fatalf("no call in the reply:\n%s", out)
			}
			sigPost(t, s, c.path, fill(second[c.path], m[1]))
			sent := (*bodies)[len(*bodies)-1]
			var call, result map[string]any
			for _, m := range sent["messages"].([]any) {
				m := m.(map[string]any)
				if cs, ok := m["tool_calls"].([]any); ok {
					call = cs[0].(map[string]any)
				}
				if m["role"] == "tool" {
					result = m
				}
			}
			if call == nil || result == nil {
				t.Fatalf("the relay was sent %v", sent["messages"])
			}
			extra, _ := call["extra_content"].(map[string]any)
			google, _ := extra["google"].(map[string]any)
			if google["thought_signature"] != sig || call["id"] != "function-call-7" || result["tool_call_id"] != "function-call-7" {
				t.Fatalf("the relay was sent the call %v and the result for %v", call, result["tool_call_id"])
			}
		})
	}
}

// A signature goes back only to the upstream that gave it: a relay that
// gave none gets no extra_content, and the calls' own ids.
func TestRelaySignatureNotToAnother(t *testing.T) {
	fresh(t)
	const sig = "c2lnbmVkLWJ5LXBldGE="
	noteSigner(sig, "peta-gemini")
	id := signedID("call_a", sig)
	body := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"` + id + `","type":"function","function":{"name":"a","arguments":"{}"}}]},{"role":"tool","tool_call_id":"` + id + `","content":"x"}]}`)
	if out := string(chatCallSignatures(body, false, "peta-gemini", true)); !strings.Contains(out, sig) || strings.Contains(out, sigMark) {
		t.Errorf("its signer was sent %s", out)
	}
	if out := string(chatCallSignatures(body, false, "other-relay", true)); strings.Contains(out, "extra_content") || strings.Contains(out, sigMark) {
		t.Errorf("another relay was sent %s", out)
	}
	r := &Request{Messages: []Message{
		{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: "call_a", Name: "a", Signature: sig}}},
		{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "call_a", Text: "x"}}},
	}}
	signer := *r
	signer.signer = "peta-gemini"
	if out := string(buildChat(&signer, "gemini-4-argon", "api.peta.example.com", false)); !strings.Contains(out, sig) {
		t.Errorf("its signer was sent %s", out)
	}
	other := *r
	other.signer = "other-relay"
	if out := string(buildChat(&other, "gemini-4-argon", "api.other.example.com", false)); strings.Contains(out, "extra_content") {
		t.Errorf("another relay was sent %s", out)
	}
}
