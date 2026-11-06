package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// OpenRouter's /models lists only the models that answer in text, so its
// image models were Nano Banana and GPT-5 Image alone; FLUX, Seedream,
// Riverflow, Recraft, GPT Image and the rest are listed by its Image API's
// /images/models, which needs no key (iwinux on Discord). The fixtures are
// rows of what openrouter.ai answered both on 2026-11-06, served at its
// own host.
func TestOpenRouterImageModelsComeFromItsImageAPI(t *testing.T) {
	oneHome(t)
	models, err := os.ReadFile("testdata/openrouter_models_2026-11-06.json")
	if err != nil {
		t.Fatal(err)
	}
	images, err := os.ReadFile("testdata/openrouter_images_models_2026-11-06.json")
	if err != nil {
		t.Fatal(err)
	}
	var asked atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			w.Write(models)
		case "/api/v1/images/models":
			asked.Add(1)
			if r.Header.Get("Authorization") != "" {
				t.Errorf("the image model list was sent the key")
			}
			w.Write(images)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	routeOpenRouterHost(t, srv.Listener.Addr().String())

	if err := Save(Provider{ID: "openrouter", Name: "OpenRouter", Preset: "openrouter", Key: "k",
		Chat: "https://openrouter.ai/api/v1", Anthropic: "https://openrouter.ai/api"}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if asked.Load() != 1 {
		t.Fatalf("/images/models asked %d times", asked.Load())
	}
	for _, m := range chat {
		if m.ID == "black-forest-labs/flux.2-pro" || m.ID == "recraft/recraft-v4-vector" {
			t.Errorf("an image model among the chat models: %s", m.ID)
		}
	}
	got := map[string]catalog.Model{}
	for _, m := range catalog.LiveDrawers("openrouter") {
		if _, dup := got[m.ID]; dup {
			t.Errorf("%s listed twice", m.ID)
		}
		got[m.ID] = m
	}
	for _, id := range []string{
		// from /models, as before
		"google/gemini-2.5-flash-image", "openai/gpt-5-image",
		// from /images/models
		"black-forest-labs/flux.2-pro", "bytedance-seed/seedream-4.5", "sourceful/riverflow-v2-fast",
		"recraft/recraft-v4-vector", "recraft/recraft-v4.1-flash", "openai/gpt-image-2",
		"x-ai/grok-imagine-image-2.0", "krea/krea-2-medium", "microsoft/mai-image-2.5",
	} {
		if _, ok := got[id]; !ok {
			t.Errorf("%s isn't among the image models", id)
		}
	}
	if len(got) != 11 {
		t.Errorf("%d image models, want 11: %v", len(got), slices.Sorted(func(yield func(string) bool) {
			for id := range got {
				if !yield(id) {
					return
				}
			}
		}))
	}
	if _, ok := got["openrouter/auto"]; ok {
		t.Error("openrouter/auto, a router that may answer with an image, is listed as an image model")
	}
	flux := got["black-forest-labs/flux.2-pro"]
	if flux.Name != "FLUX.2 Pro" {
		t.Errorf("flux named %q, want it without the vendor's prefix", flux.Name)
	}
	if flux.Released == "" || !flux.Images || !slices.Equal(flux.OutputModalities, []string{"image"}) {
		t.Errorf("flux kept as %+v", flux)
	}
	// Recraft's text-only one takes no picture
	if rf := got["recraft/recraft-v4.1-flash"]; rf.Images {
		t.Errorf("recraft-v4.1-flash said to take images: %+v", rf)
	}
}
