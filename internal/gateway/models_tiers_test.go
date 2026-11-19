package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The model list every client reads names the tiers Codex here is offered
// on each model, not only the one another magpie asks for: Cindy shows its
// Fast switch on a model whose service_tiers hold priority, and its link
// asks /v1/models with no magpie header (tsuixl, #1914). A model offered
// no tier says nothing.
func TestModelsListTiersForEveryClient(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"gpt-6.1-sol", "glm-5"}, Responses: "http://relay.invalid/v1"}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	var list struct {
		Data []struct {
			ID    string `json:"id"`
			Tiers []struct {
				ID string `json:"id"`
			} `json:"service_tiers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	got := map[string]string{}
	for _, m := range list.Data {
		s := ""
		for _, x := range m.Tiers {
			s += x.ID + " "
		}
		got[m.ID] = s
	}
	// Ultrafast too: OpenAI serves it on GPT-6.1 Sol, and a provider added
	// by its address offers it as its editor does (OoopsHh on X)
	for id, want := range map[string]string{"relay/gpt-6.1-sol": "priority ultrafast ", "relay/glm-5": ""} {
		if s, ok := got[id]; !ok || s != want {
			t.Errorf("%s tiers %q (listed %v), want %q", id, s, ok, want)
		}
	}
}
