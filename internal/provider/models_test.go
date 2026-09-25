package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestExplicitTextOnlyBeatsCrossProviderImageGuess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{
		"a":{"models":{"shared":{"id":"shared","modalities":{"input":["text","image"],"output":["text"]}}}},
		"b":{"models":{"shared":{"id":"shared","modalities":{"input":["text","image"],"output":["text"]}}}}
	}`
	if err := os.WriteFile(catalog.CachePath(), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if !catalog.SeesImages("shared") {
		t.Fatal("test catalog did not classify shared model as vision")
	}
	if err := Save(Provider{ID: "probe", Chat: "https://example.test/v1", Key: "key", Models: []string{"shared"}}); err != nil {
		t.Fatal(err)
	}
	no := false
	if err := catalog.SaveLive("probe", "https://example.test/v1", []catalog.Model{{ID: "shared", ImageInput: &no}}); err != nil {
		t.Fatal(err)
	}
	for _, e := range Catalog() {
		if e.ID == "probe/shared" {
			if e.Images || e.ImageInput == nil || *e.ImageInput {
				t.Fatalf("explicit text-only model advertised images: %+v", e)
			}
			return
		}
	}
	t.Fatal("probe/shared missing from catalog")
}

func TestFetchedKeysShareImageCapability(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		input := `["text","image"]`
		if r.Header.Get("Authorization") == "Bearer text-key" {
			input = `["text"]`
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"shared","modalities":{"input":` + input + `}}]}`))
	}))
	defer server.Close()
	p := Provider{ID: "relay", Chat: server.URL, Key: "vision-key", Keys: []KeyAccount{{Key: "text-key"}}}
	models, err := p.Fetch(context.Background())
	if err != nil || len(models) != 1 || models[0].ImageInput == nil || *models[0].ImageInput || models[0].Images {
		t.Fatalf("shared image capability: %+v, %v", models, err)
	}
}

func TestFetchedKeysKeepUnknownImageCapability(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		input := `,"modalities":{"input":["text","image"]}`
		if r.Header.Get("Authorization") == "Bearer unknown-key" {
			input = ""
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"shared"` + input + `}]}`))
	}))
	defer server.Close()
	p := Provider{ID: "relay", Chat: server.URL, Key: "vision-key", Keys: []KeyAccount{{Key: "unknown-key"}}}
	models, err := p.Fetch(context.Background())
	if err != nil || len(models) != 1 || models[0].ImageInput != nil {
		t.Fatalf("confirmed and unknown image capability: %+v, %v", models, err)
	}
}

func TestFetchedKeysKeepUnknownImageCapabilityFromOldCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer failed-key" {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"shared","modalities":{"input":["text","image"]}}]}`))
	}))
	defer server.Close()
	if err := catalog.SaveLive("relay", server.URL, []catalog.Model{{ID: "shared", Keys: []string{keyID("failed-key")}}}); err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "relay", Chat: server.URL, Key: "vision-key", Keys: []KeyAccount{{Key: "failed-key"}}}
	models, err := p.Fetch(context.Background())
	if err != nil || len(models) != 1 || models[0].ImageInput != nil {
		t.Fatalf("fresh capability and old cache without ImageInput: %+v, %v", models, err)
	}
}

// A key marked off is not asked when the model list is refreshed, and what
// it would have listed stays out of the picker — including a same-named
// model whose weaker capabilities would otherwise win. Turning it back on
// fetches it and brings its models in.
func TestOffKeyIsNotFetched(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME"} {
		t.Setenv(v, "")
	}

	var onHits, offHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer sk-on":
			onHits++
			w.Write([]byte(`{"data":[{"id":"vision","modalities":{"input":["text","image"]}},{"id":"on-only"}]}`))
		case "Bearer sk-off":
			offHits++
			w.Write([]byte(`{"data":[{"id":"vision","modalities":{"input":["text"]}},{"id":"off-only"}]}`))
		default:
			http.Error(w, "no", http.StatusUnauthorized)
		}
	}))
	defer srv.Close()

	if err := Save(Provider{
		ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Key: "sk-on",
		Keys: []KeyAccount{{Name: "spare", Key: "sk-off", Off: true}},
	}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if onHits != 1 || offHits != 0 {
		t.Fatalf("requests: on %d, off %d", onHits, offHits)
	}
	got := p.Exposed()
	if _, ok := modelNamed(got, "off-only"); ok {
		t.Fatalf("off key's model is in the picker: %+v", got)
	}
	vision, ok := modelNamed(got, "vision")
	if !ok || vision.ImageInput == nil || !*vision.ImageInput || !vision.Images {
		t.Fatalf("vision downgraded: %+v", vision)
	}
	if _, ok := modelNamed(got, "on-only"); !ok {
		t.Fatalf("on key's model missing: %+v", got)
	}

	if err := SetKeyOn("relay", KeyID("sk-off"), true); err != nil {
		t.Fatal(err)
	}
	p, err = Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if offHits != 1 {
		t.Fatalf("off key requests after re-enable: %d", offHits)
	}
	if _, ok := modelNamed(p.Exposed(), "off-only"); !ok {
		t.Fatalf("re-enabled key's model missing: %+v", p.Exposed())
	}
}

func modelNamed(ms []catalog.Model, id string) (catalog.Model, bool) {
	for _, m := range ms {
		if m.ID == id {
			return m, true
		}
	}
	return catalog.Model{}, false
}

func TestRejectsTemperatureFromFetchedList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", home)

	no, yes := false, true
	if err := catalog.SaveLive("p", "http://x", []catalog.Model{
		{ID: "strict", Name: "Strict", Temperature: &no},
		{ID: "plain", Name: "Plain", Temperature: &yes},
		{ID: "silent", Name: "Silent"},
	}); err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "p"}
	for model, want := range map[string]bool{"strict": true, "plain": false, "silent": false, "unknown": false} {
		if got := p.RejectsTemperature(model); got != want {
			t.Errorf("RejectsTemperature(%q) = %v, want %v", model, got, want)
		}
	}
}

func TestIsOpenCode(t *testing.T) {
	for base, want := range map[string]bool{
		"https://opencode.ai/zen/go/v1": true,
		"https://api.opencode.ai/v1":    true,
		"https://notopencode.ai/v1":     false,
		"https://api.deepseek.com/v1":   false,
	} {
		if got := (Provider{Chat: base}).IsOpenCode(); got != want {
			t.Errorf("%s: %v", base, got)
		}
	}
}
