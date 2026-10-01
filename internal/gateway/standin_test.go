package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// a model the agent names that magpie doesn't serve goes to the one the
// agent is set to use for it; one magpie serves is sent as asked
func TestStandIn(t *testing.T) {
	setHome(t, t.TempDir())
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"c1","choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`}
	setup(t, provider.Chat, f)
	var asked []string
	StandIn = func(agent, model string) string { asked = append(asked, model); return "fake/m1" }
	t.Cleanup(func() { StandIn = nil })

	code, body := post(t, "/v1/chat/completions", `{"model":"claude-haiku-4-5-20251001","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || modelOf(f.got) != "m1" || !strings.Contains(body, "OK") {
		t.Fatalf("stood in: %d %s, upstream %s", code, body, f.got)
	}
	for _, m := range []string{"m1", "fake/m1", "fake/other"} {
		asked = nil
		if code, body := post(t, "/v1/chat/completions", `{"model":"`+m+`","messages":[{"role":"user","content":"hi"}]}`); code != 200 || len(asked) != 0 {
			t.Fatalf("%s: %d %s, stand-in asked for %v", m, code, body, asked)
		}
	}
	// no stand-in: unknown as before
	StandIn = func(string, string) string { return "" }
	if code, _ := post(t, "/v1/chat/completions", `{"model":"claude-haiku-4-5-20251001","messages":[{"role":"user","content":"hi"}]}`); code != 404 {
		t.Fatalf("without a stand-in: %d", code)
	}
}

// Codex's auto-review asks magpie, its provider, for "codex-auto-review":
// the review goes to the model that stands in (Codex's, TestCodexStandIn).
func TestCodexAutoReviewStandIn(t *testing.T) {
	setHome(t, t.TempDir())
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"c1","choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`}
	setup(t, provider.Chat, f)
	StandIn = func(agent, model string) string {
		if model == "codex-auto-review" {
			return "fake/m1"
		}
		return ""
	}
	t.Cleanup(func() { StandIn = nil })
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex-auto-review","input":"review this","stream":false}`))
	req.Header.Set("User-Agent", "codex_cli_rs/0.160.0 (Mac OS 26.6.0; arm64) Apple_Terminal/455")
	req.Header.Set("x-openai-subagent", "guardian")
	New().Handler().ServeHTTP(rec, req)
	if rec.Code == 404 || modelOf(f.got) != "m1" {
		t.Fatalf("auto-review: %d %s, upstream %s", rec.Code, rec.Body.String(), f.got)
	}
}
