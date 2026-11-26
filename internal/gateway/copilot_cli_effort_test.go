package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// The Copilot CLI asks magpie's models on Chat Completions with the effort
// its /model picked, from the levels magpie lists in providers.json
// (capabilities.supports.reasoning_effort, agent.copilotEfforts), and
// temperature 1 beside it (1.0.116's request, as a fake server got it). The
// model gets each at its own level: a vendor's word for one (Trae's light,
// listed to Copilot as low) goes in that word (TonyRL on Discord).
func TestCopilotCLIEffortReachesTheModel(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"thinks","choices":[{"delta":{"role":"assistant","content":"hi"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	up := setup(t, provider.Chat, f)
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"zai":{"models":{"thinks":{"id":"thinks","reasoning_options":[{"type":"effort","values":["light","medium","high"]}]}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if err := provider.Save(provider.Provider{ID: "see", Name: "See", Key: "k", Chat: up.URL + "/v1", Models: []string{"thinks"}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ effort, sent string }{{"low", "light"}, {"medium", "medium"}, {"high", "high"}} {
		code, body := post(t, "/v1/chat/completions", `{"model":"see/thinks","reasoning_effort":"`+c.effort+`","temperature":1,"stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"say hi"}]}`)
		if code != 200 {
			t.Fatalf("status %d: %s", code, body)
		}
		var got map[string]any
		json.Unmarshal(f.got, &got)
		if got["reasoning_effort"] != c.sent || !strings.HasSuffix(f.path, "/chat/completions") {
			t.Errorf("Copilot's %s sent as %s", c.effort, f.got)
		}
	}
}
