package codexcat

import (
	"encoding/json"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// A model of a provider the user added by its address is offered the tiers
// Codex's own entry gives the same model, Ultrafast among them, and Fast on
// another GPT model; none on any other, nor on one not added by its address
// (hsiangron on X: Fast and Ultrafast weren't there to pick).
func TestOwnProviderTiers(t *testing.T) {
	v1Home(t, `{"etag":"W/\"a\"","models":[{"slug":"gpt-6-sol","base_instructions":"x","service_tiers":[{"id":"priority","name":"Fast","description":"f"},{"id":"ultrafast","name":"Ultrafast","description":"u"}]}]}`)
	ms := []catalog.Model{
		{ID: "relay/gpt-6-sol", Name: "a", OwnTier: true},
		{ID: "relay/gpt-6-sol-2026-09-01", Name: "b", OwnTier: true},
		{ID: "relay/gpt-5.5", Name: "c", OwnTier: true},
		{ID: "relay/glm-5", Name: "d", OwnTier: true},
		{ID: "openrouter/gpt-6-sol", Name: "e"},
	}
	var got struct {
		Models []struct {
			Slug  string `json:"slug"`
			Tiers []struct {
				ID string `json:"id"`
			} `json:"service_tiers"`
		} `json:"models"`
	}
	if err := json.Unmarshal(Catalog(ms), &got); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, m := range got.Models {
		s := ""
		for _, x := range m.Tiers {
			s += x.ID + " "
		}
		ids[m.Slug] = s
	}
	want := map[string]string{"relay/gpt-6-sol": "priority ultrafast ", "relay/gpt-6-sol-2026-09-01": "priority ultrafast ",
		"relay/gpt-5.5": "priority ", "relay/glm-5": "", "openrouter/gpt-6-sol": ""}
	for k, v := range want {
		if ids[k] != v {
			t.Errorf("%s offered %q, want %q", k, ids[k], v)
		}
	}
}
