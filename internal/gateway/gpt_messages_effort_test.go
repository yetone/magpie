package gateway

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A GPT model behind a relay's Messages endpoint is asked how hard to think
// in output_config.effort beside the budget: a relay that reads the level
// there saw every request at its default, medium, with a budget alone
// (zxz8009195489 on Discord, 2026-11-04). Claude and other vendors' models
// go as they did.
func TestGPTMessagesEffortInOutputConfig(t *testing.T) {
	r := &Request{Thinking: true, Effort: "high", Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}}}}
	for _, c := range []struct{ host, model, want string }{
		{"relay.example.com", "gpt-5.2", "high"},
		{"relay.example.com", "openai/gpt-5.4-codex", "high"},
		{"relay.example.com", "GPT-5", "high"},
		{"relay.example.com", "claude-sonnet-4-5", ""},
		{"relay.example.com", "kimi-k2.5", ""},
		{"relay.example.com", "mygpt-model", ""},
		{"api.moonshot.ai", "kimi-k2.5", ""},
	} {
		out, err := build(provider.Anthropic, r, c.model, c.host, false)
		if err != nil {
			t.Fatal(err)
		}
		if got := sentOutputEffort(t, out); got != c.want {
			t.Errorf("%s at %q: output_config.effort %q, want %q: %s", c.model, c.host, got, c.want, out)
		}
		if !strings.Contains(string(out), `"budget_tokens":`) {
			t.Errorf("%s at %q: no budget: %s", c.model, c.host, out)
		}
	}
	// each level reaches the relay as asked
	for _, e := range []string{"low", "medium", "high", "xhigh"} {
		out, _ := build(provider.Anthropic, &Request{Thinking: true, Effort: e, Messages: r.Messages}, "gpt-5.2", "relay.example.com", false)
		if got := sentOutputEffort(t, out); got != e {
			t.Errorf("effort %s sent as %q: %s", e, got, out)
		}
	}
}

// The same through the gateway: a Messages client's budget, on a relay's
// GPT model, has its level said in output_config.effort too.
func TestGPTMessagesEffortThroughGateway(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: anthropicWholeOK}
	setup(t, provider.Anthropic, f)
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"gpt-5.2", "claude-sonnet-4-5"}, Anthropic: "http://relay.example.com/anthropic"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	addr := strings.TrimPrefix(up.URL, "http://")
	s.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}}
	const body = `{"model":"%s","max_tokens":64000,"stream":false,"thinking":{"type":"enabled","budget_tokens":%s},"messages":[{"role":"user","content":"hi"}]}`
	for _, c := range []struct{ model, budget, want string }{
		{"relay/gpt-5.2", "4096", "low"},
		{"relay/gpt-5.2", "24000", "high"},
		{"relay/claude-sonnet-4-5", "24000", ""},
	} {
		f.got = nil
		req := strings.Replace(strings.Replace(body, "%s", c.model, 1), "%s", c.budget, 1)
		if code, reply := postTo(t, s, "/v1/messages", req); code != 200 {
			t.Fatalf("%s: %d %s", c.model, code, reply)
		}
		if got := sentOutputEffort(t, f.got); got != c.want {
			t.Errorf("%s budget %s: output_config.effort %q, want %q: %s", c.model, c.budget, got, c.want, f.got)
		}
	}
}
