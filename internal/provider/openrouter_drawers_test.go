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
// Riverflow, Recraft, GPT Image and the rest answer with an image alone
// (iwinux on Discord). /images/models listed those but no model's
// variants, so recraft/recraft-v4.1-flash-v2:free was in no list magpie
// read (AllenH__ on X); /models?output_modalities=image, which needs no
// key, lists both. The fixtures are what openrouter.ai answered: /models
// (rows of it) on 2026-11-06, and /models?output_modalities=image, whole,
// on 2026-11-14, served at its own host.
func TestOpenRouterImageModelsComeFromItsImageAPI(t *testing.T) {
	oneHome(t)
	models, err := os.ReadFile("testdata/openrouter_models_2026-11-06.json")
	if err != nil {
		t.Fatal(err)
	}
	images, err := os.ReadFile("testdata/openrouter_models_image_2026-11-14.json")
	if err != nil {
		t.Fatal(err)
	}
	var asked atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/models" && r.URL.Query().Get("output_modalities") == "image":
			asked.Add(1)
			if r.Header.Get("Authorization") != "" {
				t.Errorf("the image model list was sent the key")
			}
			w.Write(images)
		case r.URL.Path == "/api/v1/models" && r.URL.RawQuery == "":
			w.Write(models)
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
		t.Fatalf("/models?output_modalities=image asked %d times", asked.Load())
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
		// from /models?output_modalities=image, what /images/models lists
		"black-forest-labs/flux.2-pro", "bytedance-seed/seedream-4.5", "sourceful/riverflow-v2-fast",
		"recraft/recraft-v4-vector", "recraft/recraft-v4.1-flash", "openai/gpt-image-2",
		"x-ai/grok-imagine-image-2.0", "krea/krea-2-medium", "microsoft/mai-image-2.5",
		"recraft/recraft-v4.1-flash-v2",
		// and what it leaves out
		"recraft/recraft-v4.1-flash-v2:free",
	} {
		if _, ok := got[id]; !ok {
			t.Errorf("%s isn't among the image models", id)
		}
	}
	// the list's 72 rows but its three routers (openrouter/auto,
	// auto-beta, auto-code)
	if len(got) != 69 {
		t.Errorf("%d image models, want 69: %v", len(got), slices.Sorted(func(yield func(string) bool) {
			for id := range got {
				if !yield(id) {
					return
				}
			}
		}))
	}
	for _, id := range []string{"openrouter/auto", "openrouter/auto-beta", "openrouter/auto-code"} {
		if _, ok := got[id]; ok {
			t.Errorf("%s, a router that may answer with an image, is listed as an image model", id)
		}
	}
	// the free one answers with an image alone, so the gateway asks it at
	// /images as the rest (answersInImages)
	if free := got["recraft/recraft-v4.1-flash-v2:free"]; free.Name != "Recraft V4.1 Flash V2 (free)" ||
		!slices.Equal(free.OutputModalities, []string{"image"}) || free.Images {
		t.Errorf("the free Recraft kept as %+v", free)
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
