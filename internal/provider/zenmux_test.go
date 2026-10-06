package provider

import (
	"slices"
	"testing"
)

// A ZenMux key can be added from the picker or an import link, with all
// three API bases supplied. Model slugs keep their vendor prefix.
func TestZenMuxPreset(t *testing.T) {
	detectHome(t)
	p, err := ParseImport("magpie://import?preset=zenmux-api&key=sk-test&models=anthropic/claude-sonnet-4.5")
	if err != nil {
		t.Fatal(err)
	}
	if pr := Preset(p.Preset); pr == nil || pr.Kind != KindRelay || pr.NoKey {
		t.Fatalf("keyed relay preset: %+v", pr)
	}
	if id, err := Add(p); err != nil || id != "zenmux-api" {
		t.Fatalf("add: %q, %v", id, err)
	}
	got, err := Find("zenmux-api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "ZenMux" || got.Icon != "zenmux" || got.Catalog != "zenmux" || got.KeysURL != "https://zenmux.ai/platform/pay-as-you-go" {
		t.Fatalf("saved provider: %+v", got)
	}
	if got.Chat != "https://zenmux.ai/api/v1" || got.Responses != got.Chat || got.Anthropic != "https://zenmux.ai/api/anthropic" {
		t.Fatalf("API bases: %q, %q, %q", got.Chat, got.Responses, got.Anthropic)
	}
	if !slices.Equal(got.Models, []string{"anthropic/claude-sonnet-4.5"}) {
		t.Fatalf("model slugs: %q", got.Models)
	}
	// Another app may have configured either protocol alone. Importing its
	// documented base supplies the rest of the preset without losing its key.
	for _, e := range []endpoints{{chat: p.Chat + "/"}, {responses: p.Responses}, {anthropic: p.Anthropic}} {
		im, skip := imported("default", "sk-import", e, p.Models)
		if skip != "" || im.Preset != p.Preset || im.Key != "sk-import" || im.Chat != p.Chat || im.Responses != p.Responses || im.Anthropic != p.Anthropic {
			t.Fatalf("import at %+v: %+v, %s", e, im, skip)
		}
	}
}

// Installing a new preset must not rename an existing ZenMux OAuth
// provider, including one beside a custom API-key provider (#867).
func TestZenMuxPresetKeepsPluginID(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "plugin", true: "custom-and-plugin"}[custom], func(t *testing.T) {
			detectHome(t)
			want := "zenmux"
			if custom {
				if err := Save(Provider{ID: "zenmux", Name: "My ZenMux", Chat: "https://zenmux.ai/api/v1", Key: "sk-own"}); err != nil {
					t.Fatal(err)
				}
				want = "zenmux-plugin"
			}
			p, err := FromPreset("zenmux-api")
			if err != nil {
				t.Fatal(err)
			}
			p.Key = "sk-new"
			if _, err := Add(p); err != nil {
				t.Fatal(err)
			}
			if got := PluginID("zenmux"); got != want {
				t.Fatalf("existing OAuth provider renamed to %q, want %q", got, want)
			}
		})
	}
}
