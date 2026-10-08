package usage

import (
	"github.com/yetone/magpie/internal/settings"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func resetUsageTest(t *testing.T) {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	_ = provider.Path()
}

func TestGatewaySessionsRequiresExplicitIdentityAndKeepsModels(t *testing.T) {
	resetUsageTest(t)
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.Local)
	Append(Record{Time: at, Agent: "codex", Session: "s-1", Provider: "p", Model: "m1", Input: 10, Output: 2, Status: 200})
	Append(Record{Time: at.Add(time.Minute), Agent: "codex", Session: "s-1", Provider: "p", Model: "m2", Input: 3, Output: 4, Status: 200})
	Append(Record{Time: at, Agent: "codex", Provider: "p", Model: "m1", Input: 999, Output: 999, Status: 200})
	Append(Record{Time: at, Agent: "codex", Session: "request-old-generated", Provider: "p", Model: "m1", Input: 999, Status: 200})
	got := GatewaySessions(time.Time{}, nil)
	if len(got) != 1 || got[0].ID != "s-1" || len(got[0].Models) != 2 || got[0].Input != 13 || got[0].Output != 6 {
		t.Fatalf("sessions=%+v", got)
	}
}

func TestGatewaySessionsCacheRefreshAndOwnership(t *testing.T) {
	resetUsageTest(t)
	at := time.Now()
	Append(Record{Time: at, Agent: "pi", Session: "native", Provider: "p", Model: "m", Input: 10, Status: 200})
	first := GatewaySessions(time.Time{}, nil)
	if len(first) != 1 {
		t.Fatalf("first: %+v", first)
	}
	first[0].Models[0].Model = "changed by caller"
	first[0].Daily[0].Input = 999
	again := GatewaySessions(time.Time{}, nil)
	if again[0].Models[0].Model != "m" || again[0].Daily[0].Input != 10 {
		t.Fatalf("caller changed cache: %+v", again)
	}
	if got := GatewaySessions(time.Time{}, map[string]bool{"pi|native": true}); len(got) != 0 {
		t.Fatalf("native exclusion lost: %+v", got)
	}
	Append(Record{Time: at.Add(time.Second), Agent: "pi", Session: "native", Provider: "p", Model: "m", Input: 5, Status: 200})
	if got, ok := GatewaySessionByID("pi", "native", nil); !ok || got.Input != 15 {
		t.Fatalf("append not reflected: %+v %v", got, ok)
	}
}

func TestGeneratedSessionsExcludedFromUsage(t *testing.T) {
	resetUsageTest(t)
	Append(Record{Time: Clock(), Agent: "pi", Session: "request-old", Provider: "p", Model: "m", Input: 10, Status: 200})
	summary := Summarize(All)
	if len(summary.Sessions) != 0 || summary.Totals.Input != 10 {
		t.Fatalf("generated session polluted usage: %+v", summary)
	}
	if got := Vias(time.Time{}); len(got) != 0 {
		t.Fatalf("generated session polluted routing: %+v", got)
	}
}

func TestGatewaySessionsNativeIdentityWins(t *testing.T) {
	resetUsageTest(t)
	Append(Record{Time: time.Now(), Agent: "pi", Session: "native", Provider: "p", Model: "m", Input: 1, Status: 200})
	if got := GatewaySessions(time.Time{}, map[string]bool{"pi|native": true}); len(got) != 0 {
		t.Fatalf("got duplicate gateway session: %+v", got)
	}
}

func TestGatewaySessionsCachePricesWindowsAndRewrite(t *testing.T) {
	resetUsageTest(t)
	at := time.Now()
	Append(Record{Time: at.Add(-time.Hour), Agent: "pi", Session: "native", Provider: "p", Model: "m", Input: 1000000, Status: 200})
	Append(Record{Time: at, Agent: "pi", Session: "native", Provider: "p", Model: "m", Input: 1000000, Status: 200})
	for _, price := range []float64{2, 4} {
		cfg := settings.Load()
		cfg.ModelPrices = map[string]settings.ModelPrice{"*/m": {Input: &price, Output: new(0.0), CacheRead: new(0.0), CacheWrite: new(0.0)}}
		if err := settings.Save(cfg); err != nil {
			t.Fatal(err)
		}
		for i := range 2 {
			all := GatewaySessions(time.Time{}, nil)
			window, days := GatewaySessionWindow(at.Add(-time.Minute))
			if len(all) != 1 || all[0].Cost != 2*price || len(window) != 1 || window[0].Cost != price || len(days) != 1 || days[0].Input != 1000000 {
				t.Fatalf("price=%v read=%d all=%+v window=%+v days=%+v", price, i, all, window, days)
			}
		}
	}
	vias := Vias(time.Time{})
	vias["pi|native"][0].Calls = 999
	if got := Vias(time.Time{})["pi|native"][0].Calls; got != 2 {
		t.Fatalf("caller mutated routing cache: %d", got)
	}
	if got := Vias(at.Add(-time.Minute))["pi|native"][0].Calls; got != 1 {
		t.Fatalf("routing window: %d", got)
	}
	if err := os.WriteFile(Path(), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got := GatewaySessions(time.Time{}, nil); len(got) != 0 {
		t.Fatalf("rewrite retained sessions: %+v", got)
	}
	if got := Vias(time.Time{}); len(got) != 0 {
		t.Fatalf("rewrite retained routing: %+v", got)
	}
}
