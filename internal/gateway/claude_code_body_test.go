package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// A relay that only lets Claude Code in (#179: packy's "your request body
// appears to have been tampered with") gets the body Claude Code sent, byte
// for byte but for the model's name: its fields in their order, <, > and &
// as they were, and no thinking added for a Claude model.
func TestClaudeCodeBodyReachesRelayAsSent(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"msg","type":"message","content":[]}`}
	setup(t, provider.Anthropic, f)
	p, _ := provider.Find("fake")
	p.Models = []string{"claude-opus-5-5", "deepseek-v4"}
	provider.Save(*p)

	send := func(body string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/messages?beta=true", strings.NewReader(body))
		req.Header.Set("User-Agent", "claude-cli/2.1.284 (external, cli)")
		req.Header.Set("Authorization", "Bearer "+Token)
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		return string(f.got)
	}
	// as Claude Code orders it; a session-title request asks no thinking
	body := func(model string) string {
		return `{"model":"` + model + `","messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>\nA & B\n</system-reminder>\nhi"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.284.dd4; cc_entrypoint=cli;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude.","cache_control":{"type":"ephemeral"}}],"tools":[],"metadata":{"user_id":"{\"device_id\":\"d\"}"},"max_tokens":32000,"stream":true}`
	}

	if got, want := send(body("fake/claude-opus-5-5")), body("claude-opus-5-5"); got != want {
		t.Errorf("claude body changed:\n got %s\nwant %s", got, want)
	}
	// already the provider's own name: the very bytes
	if got, want := send(body("claude-opus-5-5")), body("claude-opus-5-5"); got != want {
		t.Errorf("unchanged body changed:\n got %s\nwant %s", got, want)
	}
	// a model that thinks by default is still told not to
	got := send(body("fake/deepseek-v4"))
	if m := gjson.Get(got, "model").Str; m != "deepseek-v4" {
		t.Errorf("model %q", m)
	}
	if th := gjson.Get(got, "thinking.type").Str; th != "disabled" {
		t.Errorf("deepseek thinking %q: %s", th, got)
	}
	if !strings.Contains(got, "<system-reminder>") {
		t.Errorf("deepseek body escaped: %s", got)
	}
}

func TestRewriteModel(t *testing.T) {
	for _, c := range []struct{ in, model, want string }{
		{`{"model":"a/b","x":"<&>"}`, "b", `{"model":"b","x":"<&>"}`},
		{`{ "stream" : true , "model" : "a/b" }`, `q"x`, `{ "stream" : true , "model" : "q\"x" }`},
		{`{"messages":[{"model":"inner"}],"model":"a"}`, "b", `{"messages":[{"model":"inner"}],"model":"b"}`},
		{`{"model":"same"}`, "same", `{"model":"same"}`},
		{`{"x":1}`, "b", `{"model":"b","x":1}`},
	} {
		if got := string(rewriteModel([]byte(c.in), c.model)); got != c.want {
			t.Errorf("%s → %s, want %s", c.in, got, c.want)
		}
	}
}
