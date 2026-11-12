package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// Copilot's /models says the prompt the account takes for a model
// (capabilities.limits.max_prompt_tokens: 936K for gemini-4-carbon), and
// that is the window, not models.dev's 1048576 for the id: past it Copilot
// answers 400 and Codex, told 1M, never compacted (codealc on X). A model
// whose limits say none keeps the window models.dev gives.
func TestCopilotWindowIsThePromptItTakes(t *testing.T) {
	signIn(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[
{"id":"gemini-4-carbon","name":"Gemini 4 Carbon","model_picker_enabled":true,"policy":{"state":"enabled"},"supported_endpoints":["/chat/completions"],"capabilities":{"type":"chat","limits":{"max_context_window_tokens":1048576,"max_prompt_tokens":936000,"max_output_tokens":65536}}},
{"id":"gpt-4.1","name":"GPT-4.1","model_picker_enabled":true,"policy":{"state":"enabled"},"supported_endpoints":["/chat/completions"],"capabilities":{"type":"chat"}}]}`)
	}))
	defer api.Close()
	copilotSessions = map[string]copilotSession{"gho_lim": {Token: "sess", ExpiresAt: time.Now().Add(time.Hour).Unix(), Endpoints: struct {
		API string `json:"api"`
	}{API: api.URL}}}
	ms, err := copilotModels(context.Background(), copilotApp{User: "octocat", Token: "gho_lim"})
	if err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "copilot"}
	got := map[string]int{}
	for _, m := range ms {
		got[m.ID] = p.WindowOf(m)
	}
	if got["gemini-4-carbon"] != 936000 {
		t.Errorf("gemini-4-carbon window %d; want 936000", got["gemini-4-carbon"])
	}
	if got["gpt-4.1"] != catalog.ContextOf("gpt-4.1") {
		t.Errorf("gpt-4.1 window %d; want models.dev's %d", got["gpt-4.1"], catalog.ContextOf("gpt-4.1"))
	}
}
