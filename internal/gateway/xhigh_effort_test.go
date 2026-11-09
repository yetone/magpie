package gateway

import (
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A client that asks xhigh of a Claude that takes it (Opus 4.7 on, Sonnet
// 5 on, Haiku 5.5, Fable: platform.claude.com/docs/en/build-with-claude/effort)
// gets xhigh, not max, which spends without limit; Opus and Sonnet 4.6,
// which have no xhigh, are asked max.
func TestXhighStaysXhighWhereClaudeTakesIt(t *testing.T) {
	fresh(t)
	up := &adaptiveVendor{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "anth", Name: "Anth", Key: "k", Anthropic: srv.URL,
		Models: []string{"claude-opus-5-5", "claude-opus-4-7", "claude-sonnet-5", "claude-haiku-5-5", "claude-fable-5-2", "claude-fable-5-1", "claude-opus-4-6", "claude-sonnet-4-6"}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ model, path, body, want string }{
		{"claude-opus-5-5", "/v1/chat/completions", `"reasoning_effort":"xhigh"`, "xhigh"},
		{"claude-opus-5-5", "/v1/responses", `"reasoning":{"effort":"xhigh"}`, "xhigh"},
		{"claude-opus-4-7", "/v1/chat/completions", `"reasoning_effort":"xhigh"`, "xhigh"},
		{"claude-sonnet-5", "/v1/chat/completions", `"reasoning_effort":"xhigh"`, "xhigh"},
		{"claude-haiku-5-5", "/v1/chat/completions", `"reasoning_effort":"xhigh"`, "xhigh"},
		{"claude-fable-5-2", "/v1/chat/completions", `"reasoning_effort":"xhigh"`, "xhigh"},
		{"claude-fable-5-1", "/v1/chat/completions", `"reasoning_effort":"xhigh"`, "xhigh"},
		{"claude-opus-4-6", "/v1/chat/completions", `"reasoning_effort":"xhigh"`, "max"},
		{"claude-sonnet-4-6", "/v1/chat/completions", `"reasoning_effort":"xhigh"`, "max"},
	} {
		t.Run(c.model+c.path, func(t *testing.T) {
			body := `{"model":"anth/` + c.model + `",` + c.body + `,"messages":[{"role":"user","content":"hi"}]}`
			if c.path == "/v1/responses" {
				body = `{"model":"anth/` + c.model + `",` + c.body + `,"input":"hi"}`
			}
			code, out := post(t, c.path, body)
			if code != 200 {
				t.Fatalf("status %d: %s", code, out)
			}
			oc, _ := up.last()["output_config"].(map[string]any)
			if e, _ := oc["effort"].(string); e != c.want {
				t.Errorf("output_config = %v, want effort %q", up.last()["output_config"], c.want)
			}
		})
	}
}
