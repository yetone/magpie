package provider

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// Copilot bills a cache write at one price however long it is kept: its
// copilot_usage counts a 1-hour write as the cache_write a 5-minute one is
// (#3435). A Claude model on Copilot is priced so, at its list price
// otherwise; the same model on Anthropic keeps the 2× input hour, and a
// 1-hour price the user gave stays theirs.
func TestCopilotOneHourCacheWriteAtItsWritePrice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	// models.dev's entry as magpie's cache had it on 2026-10-24, with a
	// tier added to check the tiers are priced alike
	os.WriteFile(catalog.CachePath(), []byte(`{
	  "anthropic":{"id":"anthropic","models":{
	    "claude-opus-5-5":{"id":"claude-opus-5-5","cost":{"input":4,"output":20,"cache_read":0.2,"cache_write":5,
	      "tiers":[{"tier":{"type":"context","size":272000},"input":8,"output":30,"cache_read":0.4,"cache_write":10}]}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	copilot := Provider{ID: "copilot", Name: "Copilot", Anthropic: "https://api.business.githubcopilot.com", Account: &Account{Agent: "copilot"}}
	anthropic := Provider{ID: "anthropic", Name: "Anthropic", Anthropic: "https://api.anthropic.com", Key: "k"}

	pr, ok := tariffOf(settings.Settings{}, copilot, true, "claude-opus-5.5")
	if !ok || pr.Input != 4 || pr.CacheWrite != 5 || pr.CacheWrite1h != 0 || pr.OneHour() != 5 {
		t.Fatalf("copilot/claude-opus-5.5 = %+v, %v; want its list price with an hour's write at $5, as Copilot bills it", pr, ok)
	}
	if long := pr.At(300000); long.OneHour() != 10 {
		t.Errorf("past its tier an hour's write is %v; want the tier's cache write, $10", long.OneHour())
	}
	// Copilot's own count for this request: 7,844,300,000 nano-AIU, $0.078443
	// (input 2, output 4, a 15,671-token 1-hour write); magpie said $0.125456
	if got := pr.CostSplit(2, 4, 0, 15671, 15671); math.Abs(got-0.078443) > 1e-9 {
		t.Errorf("the 1-hour write request costs $%.6f; Copilot billed $0.078443", got)
	}

	// a call Copilot served, read after Copilot was signed out (no such
	// provider any more): billed as Copilot bills it still
	if pr, ok := tariffOf(settings.Settings{}, Provider{ID: "copilot"}, false, "claude-opus-5.5"); !ok || pr.CacheWrite1h != 0 || pr.OneHour() != 5 || pr.At(300000).OneHour() != 10 {
		t.Errorf("signed-out copilot/claude-opus-5.5 = %+v, %v; want an hour's write at $5, $10 past its tier", pr, ok)
	}
	if pr, ok, _ := BasePriceIn(settings.Settings{}, "copilot", "claude-opus-5.5"); !ok || pr.OneHour() != 5 {
		t.Errorf("BasePriceIn(copilot, claude-opus-5.5) with no Copilot = %+v, %v; want an hour's write at $5", pr, ok)
	}

	// the catalog's own price is left as it was: Anthropic bills an hour at 2×
	if pr, ok := tariffOf(settings.Settings{}, anthropic, true, "claude-opus-5-5"); !ok || pr.CacheWrite1h != 8 || pr.At(300000).OneHour() != 16 {
		t.Errorf("anthropic/claude-opus-5-5 = %+v, %v; want the hour at 2× input, $8 and $16 past its tier", pr, ok)
	}

	// a price the user gave: an hour left out is Copilot's 5-minute price,
	// an hour they gave stays
	four, twenty, half, five, seven := 4.0, 20.0, 0.5, 5.0, 7.0
	s := settings.Settings{ModelPrices: map[string]settings.ModelPrice{
		"copilot/claude-opus-5.5": {Input: &four, Output: &twenty, CacheRead: &half, CacheWrite: &five},
		"copilot/claude-opus-6":   {Input: &four, Output: &twenty, CacheRead: &half, CacheWrite: &five, CacheWrite1h: &seven},
		"anthropic/claude-opus-6": {Input: &four, Output: &twenty, CacheRead: &half, CacheWrite: &five},
	}}
	if pr, ok := tariffOf(s, copilot, true, "claude-opus-5.5"); !ok || pr.CacheRead != 0.5 || pr.OneHour() != 5 {
		t.Errorf("the user's copilot/claude-opus-5.5 = %+v, %v; want their price, its hour at their $5 write", pr, ok)
	}
	if pr, ok := tariffOf(s, copilot, true, "claude-opus-6"); !ok || pr.OneHour() != 7 {
		t.Errorf("the user's copilot/claude-opus-6 = %+v, %v; want the $7 hour they gave", pr, ok)
	}
	if pr, ok := tariffOf(s, anthropic, true, "claude-opus-6"); !ok || pr.OneHour() != 8 {
		t.Errorf("the user's anthropic/claude-opus-6 = %+v, %v; want the hour at 2× input, $8", pr, ok)
	}
	// the ledger reads the user's price alone for a call the vendor
	// answered with another model (UserBasePriceIn, #5340): the same hour
	if pr, ok, _ := UserBasePriceIn(s, "copilot", "claude-opus-5.5"); !ok || pr.OneHour() != 5 {
		t.Errorf("UserBasePriceIn(copilot, claude-opus-5.5) = %+v, %v; want their price, its hour at their $5 write", pr, ok)
	}
}
