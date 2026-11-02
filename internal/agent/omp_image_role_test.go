package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// treeman_26897 on Discord: omp's Roles view had none of magpie's image
// models under IMAGE. omp's image role takes a model of kind image alone
// (model-roles.ts), and every model magpie wrote to models.yml was a chat
// model. From omp 18.7.0 models.yml takes kind: image on openai-images,
// which generate_image posts to the provider's baseUrl + /images/generations,
// the gateway's /v1/images. Its generate_image skips a keyless provider's
// model, so magpie's entry then carries the gateway's key. omp 18.15.0 was
// run with this models.yml against a stand-in gateway: --kind image lists
// art/gpt-image-2, and generate_image posted it to /v1/images/generations.
func TestOmpListsImageModelsForItsImageRole(t *testing.T) {
	home := piExtensionHome(t)
	was := ompVersion
	ompVersion = func() string { return "18.15.0" }
	defer func() { ompVersion = was }()

	a := omp(home)
	if err := a.Field("model").Set(magpieID + "/mixed/deepseek-v4.1-flash"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".omp", "agent", "models.yml")
	var f struct {
		Providers map[string]ompProviderEntry `yaml:"providers"`
	}
	if err := yaml.Unmarshal([]byte(readFile(path)), &f); err != nil {
		t.Fatal(err)
	}
	e := f.Providers[magpieID]
	// omp's generate_image skips a model of a keyless provider
	if e.Auth != "" || e.APIKey != "magpie" {
		t.Errorf("auth %q, apiKey %q: want the gateway's key, not auth: none", e.Auth, e.APIKey)
	}
	if e.BaseURL != here(home).v1() {
		t.Errorf("baseUrl = %q, want %q: omp posts to it + /images/generations", e.BaseURL, here(home).v1())
	}
	var img *ompModel
	for i, m := range e.Models {
		switch {
		case m.ID == "art/gpt-image-2":
			img = &e.Models[i]
		case m.Kind != "":
			t.Errorf("%s is of kind %q, want chat (no kind)", m.ID, m.Kind)
		}
	}
	if img == nil {
		t.Fatalf("no art/gpt-image-2 in models.yml:\n%s", readFile(path))
	}
	if img.Kind != "image" || img.API != "openai-images" || strings.Join(img.Input, ",") != "text,image" || img.Name != "GPT Image 2 · Art" {
		t.Errorf("image model = %+v, want kind image on openai-images, input text,image", *img)
	}
	if img.Reasoning || img.Thinking != nil || img.Cost != nil {
		t.Errorf("image model has chat fields: %+v", *img)
	}

	// omp before 18.7.0 turns the whole file away over openai-images, and
	// one whose version isn't known is taken for an older one
	// (and its chat stays keyless on loopback)
	for _, v := range []string{"18.6.3", ""} {
		e := ompProviderAt("http://127.0.0.1:3425", v)
		for _, m := range e.Models {
			if m.Kind != "" || m.API == "openai-images" {
				t.Errorf("omp %q got %s (%s, %s)", v, m.ID, m.Kind, m.API)
			}
		}
		if e.Auth != "none" || e.APIKey != "" {
			t.Errorf("omp %q: auth %q, apiKey %q, want auth: none", v, e.Auth, e.APIKey)
		}
	}
	got := 0
	for _, m := range ompProviderAt("http://127.0.0.1:3425", "18.7.0").Models {
		if m.Kind == "image" {
			got++
		}
	}
	if got != 1 {
		t.Errorf("omp 18.7.0: %d image models, want 1", got)
	}
}
