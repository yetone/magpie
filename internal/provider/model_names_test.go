package provider

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// John (Discord #feedback): a GLM Coding Plan's models read glm-5-turbo
// where ZCode's read GLM-5-Turbo. The plan's list gives ids alone, and
// Zhipu's catalog (zhipuai) doesn't list glm-5-turbo, so it kept its id as
// its name. A model Zhipu's catalog doesn't list is now named as the other
// providers serving it name it; the id sent upstream stays, a name the
// user gave stays theirs, and an Azure deployment keeps the name its owner
// gave it.
func TestModelNamedAsElsewhere(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(`{
	  "zhipuai": {"models": {"glm-5.3": {"id":"glm-5.3","name":"GLM-5.3"}}},
	  "zai": {"models": {"glm-5-turbo": {"id":"glm-5-turbo","name":"GLM-5-Turbo"}, "gpt-x": {"id":"gpt-x","name":"GPT-X"}}},
	  "zai-coding-plan": {"models": {"glm-5.3-flashx": {"id":"glm-5.3-flashx","name":"GLM-5.3-FlashX"}}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	// as https://open.bigmodel.cn/api/coding/paas/v4/models lists them
	listed := []catalog.Model{{ID: "glm-5.3", Name: "glm-5.3"}, {ID: "glm-5-turbo", Name: "glm-5-turbo"}, {ID: "glm-5.3-flashx", Name: "glm-5.3-flashx"}, {ID: "glm-9", Name: "glm-9"}}
	if err := catalog.SaveLive("zhipu", "", listed); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "zhipu", Name: "Zhipu GLM", Preset: "zhipu", Catalog: "zhipuai", Key: "test-key", Chat: "https://open.bigmodel.cn/api/coding/paas/v4",
		Models: []string{"glm-5.3", "glm-5-turbo", "glm-5.3-flashx", "glm-9", "gpt-x"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("zhipu")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range p.Exposed() {
		got = append(got, m.ID+"|"+m.Name)
	}
	want := []string{
		"glm-5.3|GLM-5.3",
		"glm-5-turbo|GLM-5-Turbo",
		"glm-5.3-flashx|GLM-5.3-FlashX",
		"glm-9|glm-9", // no provider names it
		"gpt-x|GPT-X", // picked, not in the list
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	if err := SetModelName("zhipu/glm-5-turbo", "turbo mine"); err != nil {
		t.Fatal(err)
	}
	byID := map[string]Entry{}
	for _, e := range Catalog() {
		byID[e.ID] = e
	}
	if e := byID["zhipu/glm-5-turbo"]; e.Name != "turbo mine" || e.Default != "GLM-5-Turbo" || e.Model != "glm-5-turbo" {
		t.Errorf("named by the user: %+v", e)
	}

	if err := catalog.SaveLive("my-azure", "", []catalog.Model{{ID: "gpt-x", Name: "gpt-x"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "my-azure", Name: "Azure", Preset: AzurePreset, Key: "test-key", Chat: "https://me.openai.azure.com/openai/v1", Models: []string{"gpt-x"}}); err != nil {
		t.Fatal(err)
	}
	a, err := Find("my-azure")
	if err != nil {
		t.Fatal(err)
	}
	if ms := a.Exposed(); len(ms) != 1 || ms[0].Name != "gpt-x" {
		t.Errorf("an Azure deployment was renamed: %+v", ms)
	}
}

// Unlisted picks are named together, so each fallback sees its neighbors.
func TestPickedNamesAvoidCollisions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(`{"vendor":{"models":{
	  "flash":{"id":"flash","name":"Flash"},
	  "fast":{"id":"fast","name":"Flash"},
	  "other":{"id":"other","name":"Other"}
	}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	for _, c := range []struct {
		name, preset string
		live         []catalog.Model
		want         []string
	}{
		{"unlisted", "", nil, []string{"flash|flash", "fast|fast", "other|Other"}},
		{"listed name", "", []catalog.Model{{ID: "flash", Name: "Flash"}}, []string{"flash|Flash", "fast|fast", "other|Other"}},
		{"Azure", AzurePreset, nil, []string{"flash|flash", "fast|fast", "other|other"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := Provider{ID: "name-picks", Preset: c.preset, Models: []string{"flash", "fast", "other"}}
			if err := catalog.SaveLive(p.ID, "", c.live); err != nil {
				t.Fatal(err)
			}
			for _, reverse := range []bool{false, true} {
				want := slices.Clone(c.want)
				if reverse {
					slices.Reverse(p.Models)
					slices.Reverse(want)
				}
				var got []string
				for _, m := range p.Exposed() {
					got = append(got, m.ID+"|"+m.Name)
				}
				if strings.Join(got, "\n") != strings.Join(want, "\n") {
					t.Errorf("got %v, want %v", got, want)
				}
			}
		})
	}
}

// Picking a subset must not refill names Available deliberately left as ids.
// Unlisted picks still reserve all listed names and ids, even if not picked.
func TestPickedNamesKeepListed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(`{"vendor":{"models":{
	  "flash":{"id":"flash","name":"Flash"},
	  "fast":{"id":"fast","name":"Flash"},
	  "alias":{"id":"alias","name":"listed-id"}
	}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	for _, c := range []struct {
		name, providerID, catalog string
		live                      []catalog.Model
		picks, want               []string
	}{
		{"collision fallback", "picked-collision", "vendor", []catalog.Model{{ID: "flash", Name: "flash"}, {ID: "fast", Name: "fast", Context: 1000000}}, []string{"fast"}, []string{"fast|fast"}},
		{"Antigravity", "antigravity", "", []catalog.Model{{ID: "fast", Context: 1000000}}, []string{"fast"}, []string{"fast|fast"}},
		{"listed name", "picked-name", "", []catalog.Model{{ID: "flash", Name: "Flash"}}, []string{"flash", "fast"}, []string{"flash|Flash", "fast|fast"}},
		{"unpicked name", "unpicked-name", "", []catalog.Model{{ID: "flash", Name: "Flash"}}, []string{"fast"}, []string{"fast|fast"}},
		{"listed id", "picked-id", "", []catalog.Model{{ID: "listed-id", Name: "Listed custom"}}, []string{"listed-id", "alias"}, []string{"listed-id|Listed custom", "alias|alias"}},
		{"unpicked id", "unpicked-id", "", []catalog.Model{{ID: "listed-id", Name: "Listed custom"}}, []string{"alias"}, []string{"alias|alias"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := Provider{ID: c.providerID, Catalog: c.catalog, Models: slices.Clone(c.picks)}
			if err := catalog.SaveLive(p.ID, "", c.live); err != nil {
				t.Fatal(err)
			}
			before := p.Available()
			for _, reverse := range []bool{false, true} {
				want := slices.Clone(c.want)
				if reverse {
					slices.Reverse(p.Models)
					slices.Reverse(want)
				}
				var got []string
				for _, m := range p.Exposed() {
					got = append(got, m.ID+"|"+m.Name)
					for _, listed := range before {
						if listed.ID == m.ID && !reflect.DeepEqual(m, listed) {
							t.Errorf("listed model changed: %+v; Available gave %+v", m, listed)
						}
					}
				}
				if !slices.Equal(got, want) {
					t.Errorf("got %v, want %v", got, want)
				}
			}
			if after := p.Available(); !reflect.DeepEqual(after, before) {
				t.Errorf("available list changed: %+v; was %+v", after, before)
			}
		})
	}
}
