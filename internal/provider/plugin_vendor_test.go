package provider

import (
	"testing"

	"github.com/yetone/magpie/internal/plugin"
)

// A plugin's provider is the vendor its `api` is on, for the gateway's
// vendor checks (#5464); a provider no plugin gives, a moved subscription,
// and a plugin on no preset's host, this computer or its network keep
// their own host.
func TestPluginVendor(t *testing.T) {
	on := func(api string) Provider {
		p := PluginForTest("plugin-vibe", "vibe")
		p.Account.plugin = &plugin.Provider{ID: "vibe", API: api}
		return p
	}
	for _, c := range []struct {
		name, api, preset, host string
	}{
		{"mistral", "https://api.mistral.ai/v1", "mistral", "api.mistral.ai"},
		{"deepseek", "https://api.deepseek.com/v1", "deepseek", "api.deepseek.com"},
		{"openrouter", "https://openrouter.ai/api/v1", "openrouter", "openrouter.ai"},
		{"unknown host", "https://vibe.example.com/v1", "", ""},
		{"no api", "", "", ""},
		{"localhost", "http://localhost:11434/v1", "", ""},
		{"loopback", "http://127.0.0.1:8080/v1", "", ""},
		{"lan", "http://192.168.1.4:8080/v1", "", ""},
		{"dot local", "http://box.local:8080/v1", "", ""},
	} {
		p := on(c.api)
		if got := p.VendorPreset(); got != c.preset {
			t.Errorf("%s: VendorPreset = %q, want %q", c.name, got, c.preset)
		}
		want := c.host
		if want == "" {
			want = p.Host()
		}
		if got := p.VendorHost(); got != want {
			t.Errorf("%s: VendorHost = %q, want %q", c.name, got, want)
		}
	}
	moved := on("https://api.mistral.ai/v1")
	moved.Account.moved = true
	if moved.VendorPreset() != "" || moved.VendorHost() != moved.Host() {
		t.Errorf("a moved subscription is %q at %q, want its own host %q", moved.VendorPreset(), moved.VendorHost(), moved.Host())
	}
	plain := Provider{ID: "mistral", Preset: "mistral"}
	if plain.VendorPreset() != "mistral" || plain.VendorHost() != plain.Host() {
		t.Errorf("a preset provider is %q at %q", plain.VendorPreset(), plain.VendorHost())
	}
}
