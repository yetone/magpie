package usage

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
)

// #1370 (maicent): the Usage page counted a Kimi Code membership's k3 at
// ≈¥0.000, its plan's models.dev catalog listing every model at $0. A k3
// call is now counted at Kimi's API price for kimi-k3, a k3.1-pro one at
// kimi-k3.1-pro's, kimi-for-coding (K2.8 Preview, which the API doesn't
// sell) is unpriced rather than free, and a Moonshot pay-as-you-go call is
// what it was.
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
	k31 := (float64(in)*2 + float64(out)*10 + float64(cached)*0.2) / 1e6
	now := time.Now()
	calls := []struct {
		prov, model string
		priced      bool
		want        float64
	}{
		{"kimi-code-cn", "k3", true, k3},
		{"kimi-code-cn", "k3.1-pro", true, k31},
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
	if s.Unpriced != 1 || math.Abs(s.Cost-(2*k3+k31)) > 1e-9 || math.Abs(sum.Cost-(2*k3+k31)) > 1e-9 {
		t.Fatalf("summary cost %v unpriced %d, ledger %v; want %v and 1", s.Cost, s.Unpriced, sum.Cost, 2*k3+k31)
	}
}

// kimiSessionHome is a sandbox HOME with the Kimi Code models.dev entries
// of kimi_code_catalog.json, and a Kimi Code wire file of its own sign-in's
// calls, each line a usage.record in the shape the #1607 reporter quoted
// from their own file (internal/sessions' kimiRecordLine): model is the
// key of the [models."…"] table the call used. Its calls are what the
// session reader reads of it.
func kimiSessionHome(t *testing.T, at time.Time, models ...string) []sessions.Call {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("KIMI_CODE_HOME", filepath.Join(home, ".kimi-code"))
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
	var lines []string
	for i, m := range models {
		lines = append(lines, fmt.Sprintf(`{"type":"usage.record","agentId":"main","model":%q,"usage":{"inputOther":20257,"output":230,"inputCacheRead":1000000,"inputCacheCreation":0},"usageScope":"turn","time":%d}`,
			m, at.Add(time.Duration(i)*time.Second).UnixMilli()))
	}
	wire := filepath.Join(home, ".kimi-code", "sessions", "wd_3331_000000000001", "session_3331", "agents", "main", "wire.jsonl")
	os.MkdirAll(filepath.Dir(wire), 0o755)
	if err := os.WriteFile(wire, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sessions.Reset()
	t.Cleanup(sessions.Reset)
	var calls []sessions.Call
	for _, c := range sessions.Calls(at.Add(-time.Hour)) {
		if c.Agent == "kimi" {
			calls = append(calls, c)
		}
	}
	if len(calls) != len(models) {
		t.Fatalf("the wire file read as %d calls, want %d: %+v", len(calls), len(models), calls)
	}
	old := LogCalls
	LogCalls = func(time.Time) []sessions.Call { return calls }
	t.Cleanup(func() { LogCalls = old })
	return calls
}

// #3331 (westlinkin): Kimi Code left on its own sign-in ("not connected,
// uses Kimi Code's own config") names its model "kimi-code/k3-256k" in
// its session file, and the Usage page showed its calls with no cost,
// though the reporter's Kimi Code (China) entry prices k3-256k and the
// same model through magpie was counted. A local call is priced as a call
// to the user's Kimi Code membership, at the price they set there; with no
// membership added, at the Kimi API model it is (VectorLu's case,
// k3.1-pro); a price the user set for the session's own name, or for
// every unknown provider's model, stands over both; kimi-for-coding (K2.8
// Preview, not on the API) stays unpriced. The Sessions tab prices the
// model the same way.
func TestKimiCodeSessionCost(t *testing.T) {
	in, out, cached := 20257.0, 230.0, 1_000_000.0
	at := func(i, o, c float64) float64 { return (in*i + out*o + cached*c) / 1e6 }
	set := func(i, o, c float64) settings.ModelPrice {
		return settings.StatedPrice(catalog.Price{Input: i, Output: o, CacheRead: c})
	}
	for _, tc := range []struct {
		name   string
		saved  []string
		prices map[string]settings.ModelPrice
		model  string
		priced bool
		want   float64
	}{
		{name: "the reporter's price at their Kimi Code (China)", saved: []string{"kimi-code-cn"},
			prices: map[string]settings.ModelPrice{"kimi-code-cn/k3-256k": set(1, 4, 0.1)},
			model:  "kimi-code/k3-256k", priced: true, want: at(1, 4, 0.1)},
		{name: "a membership's price for every model", saved: []string{"kimi-code-cn"},
			prices: map[string]settings.ModelPrice{"kimi-code-cn/*": set(1, 4, 0.1)},
			model:  "kimi-code/k3-256k", priced: true, want: at(1, 4, 0.1)},
		{name: "the model's price at any provider", saved: []string{"kimi-code-cn"},
			prices: map[string]settings.ModelPrice{"*/k3-256k": set(1, 4, 0.1)},
			model:  "kimi-code/k3-256k", priced: true, want: at(1, 4, 0.1)},
		{name: "the membership named first", saved: []string{"kimi-code-cn", "kimi-code"},
			prices: map[string]settings.ModelPrice{"kimi-code-cn/k3-256k": set(1, 4, 0.1), "kimi-code/k3-256k": set(2, 8, 0.2)},
			model:  "kimi-code/k3-256k", priced: true, want: at(2, 8, 0.2)},
		{name: "a membership with no price of the user's", saved: []string{"kimi-code-cn"},
			model: "kimi-code/k3-256k", priced: true, want: at(3, 15, 0.3)},
		{name: "no membership added", model: "kimi-code/k3.1-pro", priced: true, want: at(2, 10, 0.2)},
		{name: "the session's own name", saved: []string{"kimi-code-cn"},
			prices: map[string]settings.ModelPrice{"kimi-code-cn/k3-256k": set(1, 4, 0.1), UnknownProvider + "/kimi-code/k3-256k": set(5, 5, 5)},
			model:  "kimi-code/k3-256k", priced: true, want: at(5, 5, 5)},
		{name: "every unknown provider's model", saved: []string{"kimi-code-cn"},
			prices: map[string]settings.ModelPrice{"kimi-code-cn/k3-256k": set(1, 4, 0.1), UnknownProvider + "/*": set(5, 5, 5)},
			model:  "kimi-code/k3-256k", priced: true, want: at(5, 5, 5)},
		{name: "K2.8 Preview", saved: []string{"kimi-code-cn"}, model: "kimi-code/kimi-for-coding"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().Add(-time.Hour)
			kimiSessionHome(t, now, tc.model)
			for _, id := range tc.saved {
				p, err := provider.FromPreset(id)
				if err != nil {
					t.Fatal(err)
				}
				p.Key = "sk-test"
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
			}
			if tc.prices != nil {
				if err := settings.Save(settings.Settings{ModelPrices: tc.prices}); err != nil {
					t.Fatal(err)
				}
			}
			rows, sum, _ := Ledger(Month, Filter{})
			if len(rows) != 1 {
				t.Fatalf("rows %d", len(rows))
			}
			r := rows[0]
			if r.Source != "log" || r.Provider != UnknownProvider || r.Model != tc.model || r.Priced != tc.priced || math.Abs(r.Cost-tc.want) > 1e-9 {
				t.Errorf("session call: priced=%v cost=%v, want %v %v; row %+v", r.Priced, r.Cost, tc.priced, tc.want, r.Record)
			}
			if math.Abs(sum.Cost-tc.want) > 1e-9 {
				t.Errorf("summary cost %v, want %v", sum.Cost, tc.want)
			}
			if tc.name == "the session's own name" || tc.name == "every unknown provider's model" {
				return // a ledger key: the Sessions tab has no unknown provider
			}
			pr, ok := sessions.PriceOf(settings.Load(), tc.model)
			if ok != tc.priced || ok && math.Abs((in*pr.Input+out*pr.Output+cached*pr.CacheRead)/1e6-tc.want) > 1e-9 {
				t.Errorf("Sessions tab: %+v %v, want %v %v", pr, ok, tc.priced, tc.want)
			}
		})
	}
}

// A Kimi Code session's call is priced at its membership's rate windows
// (#2463), and keeps them on a cache hit after a call of another model
// under the same unknown provider, which has none (VectorLu, #3331).
func TestKimiCodeSessionWindowsStayWithTheirModel(t *testing.T) {
	kimiSessionHome(t, time.Now().Add(-time.Hour), "kimi-code/k3-256k")
	p, err := provider.FromPreset("kimi-code-cn")
	if err != nil {
		t.Fatal(err)
	}
	p.Key = "sk-test"
	p.RateWindows = []provider.RateWindow{{Start: "00:00", End: "24:00", Rate: 2}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{ModelPrices: map[string]settings.ModelPrice{
		"kimi-code-cn/k3-256k": settings.StatedPrice(catalog.Price{Input: 1}),
		"*/other-model":        settings.StatedPrice(catalog.Price{Input: 1}),
	}}); err != nil {
		t.Fatal(err)
	}
	priceOf := Pricer()
	now := time.Now()
	for i, step := range []struct {
		model string
		want  float64
	}{{"kimi-code/k3-256k", 2e-6}, {"other-model", 1e-6}, {"kimi-code/k3-256k", 2e-6}} {
		r := Record{Provider: UnknownProvider, Agent: "kimi", Model: step.model, Input: 1, Time: now}
		pr := priceOf(r)
		if pr == nil {
			t.Fatalf("step %d %s is unpriced", i, step.model)
		}
		if got := r.CostAt(*pr); math.Abs(got-step.want) > 1e-15 {
			t.Errorf("step %d %s cost %v, want %v", i, step.model, got, step.want)
		}
	}
}
