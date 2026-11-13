package gui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/fx"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Currency "By provider" (onkashiko on X): the window is told which
// providers sell in yuan, beside the rate, and the menu bar's totals — a
// sum of every provider — go in the currency picked for totals.
func TestFXByProvider(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	fx.Reset()
	cache := fx.CachePath()
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(fx.Rate{CNYPerUSD: 7.25, At: time.Now().Add(-time.Hour)})
	if err := os.WriteFile(cache, b, 0o644); err != nil {
		t.Fatal(err)
	}
	fx.Reset()
	for _, p := range []provider.Provider{
		{ID: "deepseek", Name: "DeepSeek", Preset: "deepseek", Key: "k", Chat: "https://api.deepseek.com/v1"},
		{ID: "zai", Name: "Z.ai", Preset: "zai", Key: "k", Chat: "https://api.z.ai/api/paas/v4"},
		{ID: "openai", Name: "OpenAI", Preset: "openai", Key: "k", Chat: "https://api.openai.com/v1"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}

	if err := settings.Save(settings.Settings{Currency: "cny"}); err != nil {
		t.Fatal(err)
	}
	if got := state().FX; got.Yuan != nil {
		t.Fatalf("cny: no provider list needed, got %v", got.Yuan)
	}

	if err := settings.Save(settings.Settings{Currency: settings.CurrencyByProvider}); err != nil {
		t.Fatal(err)
	}
	for _, got := range []fxJSON{state().FX, settingsState().FX} {
		if got.Rate != 7.25 || !slices.Contains(got.Yuan, "deepseek") || slices.Contains(got.Yuan, "zai") || slices.Contains(got.Yuan, "openai") {
			t.Fatalf("by provider: FX = %+v, want the rate and deepseek alone in yuan", got)
		}
	}
	if c, _ := trayCurrency(); c != "usd" {
		t.Fatalf("by provider, totals unset: menu bar in %s, want usd", c)
	}
	if err := settings.Save(settings.Settings{Currency: settings.CurrencyByProvider, TotalCurrency: "cny"}); err != nil {
		t.Fatal(err)
	}
	if c, rate := trayCurrency(); c != "cny" || rate != 7.25 {
		t.Fatalf("by provider, totals in cny: menu bar in %s at %v", c, rate)
	}
	// the menu bar set apart from the totals (onkashiko on X), and the
	// daily Bark message, a total, still in the totals'
	if err := settings.Save(settings.Settings{Currency: settings.CurrencyByProvider, TotalCurrency: "cny", TrayCurrency: "usd"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := trayCurrency(); c != "usd" {
		t.Fatalf("menu bar set to usd, totals in cny: menu bar in %s", c)
	}
	if c, rate := totalsCurrency(); c != "cny" || rate != 7.25 {
		t.Fatalf("menu bar set to usd, totals in cny: totals in %s at %v", c, rate)
	}
	if err := settings.Save(settings.Settings{Currency: "usd", TrayCurrency: "cny"}); err != nil {
		t.Fatal(err)
	}
	if c, rate := trayCurrency(); c != "cny" || rate != 7.25 {
		t.Fatalf("menu bar set to cny, totals in usd: menu bar in %s at %v", c, rate)
	}
}
