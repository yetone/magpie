package gui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/fx"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/settings"
)

// Settings shows the local address agents use; the Gateway page advertises
// the public URL for its copy buttons and snippets.
func TestPublicURLInConsole(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(h, ".local", "share"))
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3425")
	t.Setenv("MAGPIE_PUBLIC_URL", "https://magpie.example.com/magpie///")

	// Seed the rate the Settings page reads, so it needs no network.
	cache := fx.CachePath()
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(fx.Rate{CNYPerUSD: 7.25, At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, b, 0o644); err != nil {
		t.Fatal(err)
	}
	fx.Reset()
	t.Cleanup(fx.Reset)

	if err := settings.Save(settings.Settings{LAN: true, LANKey: "sk-magpie-test"}); err != nil {
		t.Fatal(err)
	}
	want := "https://magpie.example.com/magpie"
	if got := settingsState().Gateway; got != gateway.URL() {
		t.Errorf("Settings: %q, want %q", got, gateway.URL())
	}
	g := providersState().Gateway
	if g.URL != gateway.URL() {
		t.Errorf("Gateway address: %q, want the local %q", g.URL, gateway.URL())
	}
	if len(g.LANURLs) != 1 || g.LANURLs[0] != want {
		t.Errorf("LAN addresses: %v, want [%s]", g.LANURLs, want)
	}
	if g.Open {
		t.Error("a gateway shared with a key is not open to anyone")
	}
	b, err = json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(b, &state); err != nil {
		t.Fatal(err)
	}
	if state["lan"] != true {
		t.Fatalf("LAN state missing from gateway page: %s", b)
	}
}

// The console warns about a gateway left open to the network: listening
// beyond loopback with nothing shared.
func TestGatewayOpenInConsole(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(h, ".local", "share"))
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3425")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	if err := settings.Save(settings.Settings{LAN: false}); err != nil {
		t.Fatal(err)
	}
	if g := providersState().Gateway; !g.Open {
		t.Errorf("open gateway not reported: %+v", g)
	}
	if err := settings.Save(settings.Settings{LAN: true}); err != nil {
		t.Fatal(err)
	}
	if g := providersState().Gateway; g.Open {
		t.Errorf("sharing turns the open warning off: %+v", g)
	}
}
