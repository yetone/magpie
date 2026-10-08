package gui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// lml on Discord (Windows, about ten providers): the Providers page took
// seconds to come up. Each model's row read the settings three times over
// (its image answer twice, its reply limit once), parsing settings.json
// again each time even while the request held it: with the owner's 23
// providers that was most of /api/providers (~0.5s on Windows). A
// provider's list now reads them once, and still says what they hold.
func TestProviderListReadsSettingsOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	var models []string
	for i := range 300 {
		models = append(models, fmt.Sprintf("model-%d", i))
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: models}); err != nil {
		t.Fatal(err)
	}
	// a big settings file, as a long-used magpie's is, makes each read
	// show: some 20000 image answers of other providers' models
	images := map[string]bool{"relay/model-7": true}
	for i := range 20000 {
		images[fmt.Sprintf("other-%d/some-model-%d", i%50, i)] = i%2 == 0
	}
	raw, err := json.Marshal(map[string]any{
		"modelImages":  images,
		"modelOutputs": map[string]int{"relay/model-9": 64000},
	})
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(settings.Path()), 0o755)
	if err := os.WriteFile(settings.Path(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	// as a GET /api/* is served (held)
	defer provider.Hold()()
	defer filememo.Hold()()
	providerInfo(*p, nil) // the first read of each file
	start := time.Now()
	info := providerInfo(*p, nil)
	took := time.Since(start)
	if took > 2*time.Second {
		t.Errorf("one provider's 300 models took %v: the settings were read for each", took)
	}
	byID := map[string]modelJSON{}
	for _, m := range info.Models {
		byID[m.ID] = m
	}
	if m := byID["model-7"]; !m.Images || !m.ImageSet {
		t.Errorf("the user's image answer is lost: %+v", m)
	}
	if m := byID["model-8"]; m.ImageSet {
		t.Errorf("an image answer nobody gave: %+v", m)
	}
	if m := byID["model-9"]; m.Output != 64000 {
		t.Errorf("the user's reply limit is lost: %+v", m)
	}
}
