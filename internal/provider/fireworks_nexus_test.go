package provider

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// Fireworks Nexus (CNSA0422 on X) is the platform's /inference API with a
// key from nexus.fireworks.ai, on all three APIs. Pro and Team refuse every
// model but `auto` (docs.fireworks.ai/nexus/open-models: 403), so `auto` is
// its list, and no list is asked for: the platform's would offer models the
// plan refuses.
func TestFireworksNexusPreset(t *testing.T) {
	detectHome(t)
	p, err := FromPreset("fireworks-nexus")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://api.fireworks.ai/inference/v1" || p.Responses != "https://api.fireworks.ai/inference/v1" ||
		p.Anthropic != "https://api.fireworks.ai/inference" {
		t.Fatalf("endpoints %q %q %q", p.Chat, p.Responses, p.Anthropic)
	}
	if p.KeysURL != "https://nexus.fireworks.ai/settings/api-keys" {
		t.Fatalf("keys at %q", p.KeysURL)
	}
	if pr := Preset("fireworks-nexus"); !slices.Equal(pr.HeaderHints, []string{"x-anthropic-api-key", "x-openai-api-key"}) {
		t.Fatalf("header hints %v", pr.HeaderHints)
	}
	asked := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = true
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"object":"list","data":[{"id":"accounts/fireworks/models/glm-5p3","object":"model"}]}`))
	}))
	defer up.Close()
	p.Key = "fw_test"
	p.Chat, p.Responses, p.Anthropic = up.URL+"/inference/v1", up.URL+"/inference/v1", up.URL+"/inference"
	for name, list := range map[string]func() ([]string, error){
		"fetch": func() ([]string, error) { ms, err := p.fetch(t.Context()); return modelIDs(ms), err },
		"all":   func() ([]string, error) { ms, err := p.ListAll(t.Context()); return modelIDs(ms), err },
	} {
		got, err := list()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !slices.Equal(got, []string{"auto"}) {
			t.Fatalf("%s: models %v", name, got)
		}
	}
	if asked {
		t.Fatal("the platform's /models was asked")
	}
}
