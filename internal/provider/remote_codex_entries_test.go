package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Another magpie's models carry the entries that magpie hands its own
// Codex on another computer (/v1/codex/models): a ChatGPT account it
// holds has its GPT models there as the backend lists them, instructions
// and model_messages and all, which a Codex here is handed as they are
// (Darkf1ames on X: a remote ChatGPT subscription wasn't seamless in
// Codex). Asked with the remote's key, once a fetch.
func TestRemoteMagpieHandsOnItsCodexEntries(t *testing.T) {
	azureHome(t)
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Write([]byte(`{"data":[{"id":"codex/gpt-6-astra"},{"id":"deepseek/deepseek-v4"}]}`))
		case "/v1/codex/models":
			asked.Add(1)
			if r.URL.Query().Get("client_version") == "" || r.Header.Get("Authorization") != "Bearer sk-magpie-x" {
				t.Errorf("asked %s with %q", r.URL, r.Header.Get("Authorization"))
			}
			w.Write([]byte(`{"models":[{"slug":"codex/gpt-6-astra","display_name":"GPT-6-Astra","description":"Latest frontier agentic coding model.","base_instructions":"You are Codex, an agent based on GPT-6.","model_messages":{"instructions_template":"You are Codex {{ personality }}"},"shell_type":"shell_command","supported_reasoning_levels":[{"effort":"low","description":"Fast"},{"effort":"high","description":"Deep"}],"default_reasoning_level":"low","context_window":272000,"service_tiers":[{"id":"priority","name":"Fast"}]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	if err := Save(Provider{ID: "office", Preset: RemoteMagpiePreset, Key: "sk-magpie-x", Chat: srv.URL}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("office")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if asked.Load() != 1 {
		t.Errorf("its Codex list asked %d times, want once", asked.Load())
	}
	got := map[string]json.RawMessage{}
	for _, m := range ms {
		got[m.ID] = m.Codex
	}
	var e struct {
		Messages map[string]any `json:"model_messages"`
		Tiers    []any          `json:"service_tiers"`
	}
	if json.Unmarshal(got["codex/gpt-6-astra"], &e) != nil || e.Messages["instructions_template"] == nil || len(e.Tiers) != 1 {
		t.Errorf("codex/gpt-6-astra's entry %s", got["codex/gpt-6-astra"])
	}
	if c, ok := got["deepseek/deepseek-v4"]; !ok || len(c) != 0 {
		t.Errorf("deepseek/deepseek-v4: listed %v, entry %s; want listed, none", ok, c)
	}
}
