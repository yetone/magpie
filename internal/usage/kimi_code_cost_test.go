package usage

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// #1370 (maicent): the Usage page counted a Kimi Code membership's k3 at
// ≈¥0.000, its plan's models.dev catalog listing every model at $0. A k3
// call is now counted at Kimi's API price for kimi-k3, kimi-for-coding
// (K2.8 Preview, which the API doesn't sell) is unpriced rather than free,
// and a Moonshot pay-as-you-go call is what it was.
func TestKimiCodeCost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	b, err := os.ReadFile(filepath.Join("..", "provider", "testdata", "kimi_code_catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	if err := os.WriteFile(catalog.CachePath(), b, 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	for _, id := range []string{"kimi-code-cn", "moonshot"} {
		p, err := provider.FromPreset(id)
		if err != nil {
			t.Fatal(err)
		}
		p.Key = "sk-test"
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	// the reporter's k3 row: 240K in, 78.4K out, 10.0M cached
	in, out, cached := 240_000, 78_400, 10_000_000
	k3 := (float64(in)*3 + float64(out)*15 + float64(cached)*0.3) / 1e6
	now := time.Now()
	calls := []struct {
		prov, model string
		priced      bool
		want        float64
	}{
		{"kimi-code-cn", "k3", true, k3},
		{"kimi-code-cn", "kimi-for-coding", false, 0},
		{"moonshot", "kimi-k3", true, k3},
	}
	for i, c := range calls {
		Append(Record{Time: now.Add(-time.Duration(len(calls)-i) * time.Minute), Agent: "claude-code", Provider: c.prov, Model: c.model,
			Input: in, Output: out, CacheRead: cached, Status: 200})
	}
	rows, sum, _ := Ledger(Month, Filter{})
	if len(rows) != len(calls) {
		t.Fatalf("rows %d", len(rows))
	}
	for i, c := range calls {
		r := rows[len(calls)-1-i]
		if r.Model != c.model || r.Priced != c.priced || math.Abs(r.Cost-c.want) > 1e-9 {
			t.Errorf("%s/%s: priced=%v cost=%v, want %v %v", c.prov, c.model, r.Priced, r.Cost, c.priced, c.want)
		}
	}
	s := Summarize(Month)
	if s.Unpriced != 1 || math.Abs(s.Cost-2*k3) > 1e-9 || math.Abs(sum.Cost-2*k3) > 1e-9 {
		t.Fatalf("summary cost %v unpriced %d, ledger %v; want %v and 1", s.Cost, s.Unpriced, sum.Cost, 2*k3)
	}
}
