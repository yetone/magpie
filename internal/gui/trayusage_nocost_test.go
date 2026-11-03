package gui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// Settings › Menu bar cost off (settings.TrayNoCost, BussanQ on Discord):
// magpie's today and last-7-days cards in the menu bar, their tooltip and
// the tooltip with no cards say the tokens and requests alone, no money
// and no count of calls with no known price; on, the default, the cost.
func TestTrayNoCost(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	sums := map[usage.Period]usage.Totals{
		usage.Today: {Calls: 152, Input: 912_000, Output: 318_400, CacheRead: 4_900_000, CacheWrite: 200_000, Cost: 3.4, Unpriced: 2},
		usage.Week:  {Calls: 2310, Input: 41_000_000, Output: 6_200_000, Cost: 128.25},
	}
	old := trayTotalSum
	trayTotalSum = func(p usage.Period) usage.Totals { return sums[p] }
	t.Cleanup(func() { trayTotalSum = old })
	ids := []string{"usage:7d", "usage:today"}

	if err := settings.Save(settings.Settings{TrayUsages: ids}); err != nil {
		t.Fatal(err)
	}
	_, label, _ := trayUsageView(trayUsageCards(context.Background(), "en", ids), time.Now(), false, false)
	if label != "Σ47M · $128 | Σ6.3M · $3.40+" {
		t.Errorf("cost on: label %q", label)
	}

	if err := settings.Save(settings.Settings{TrayUsages: ids, TrayNoCost: true}); err != nil {
		t.Fatal(err)
	}
	cells, label, tip := trayUsageView(trayUsageCards(context.Background(), "en", ids), time.Now(), false, false)
	if label != "Σ47M | Σ6.3M" {
		t.Errorf("cost off: label %q, want the tokens alone", label)
	}
	for _, c := range cells {
		if len(c.Rows) != 1 {
			t.Errorf("cost off: cell %s rows %q, want the tokens alone", c.ID, c.Rows)
		}
	}
	if want := "Last 7 days in magpie\n47M tokens (41M in · 6.2M out)\n2310 requests\n\n" +
		"Today in magpie\n6.3M tokens (912K in · 318K out · 5.1M cached)\n152 requests"; tip != want {
		t.Errorf("cost off: tooltip %q\nwant %q", tip, want)
	}
	// the tooltip with no cards, as the watch loop makes it
	if got := trayIdleTip("en", trayMoney(sums[usage.Today], true), "usd", 0); got != "magpie\nToday: 6.3M tokens (912K in · 318K out · 5.1M cached)\n152 requests" {
		t.Errorf("cost off: idle tooltip %q", got)
	}
	if got := trayIdleTip("en", trayMoney(sums[usage.Today], false), "usd", 0); !strings.Contains(got, "≈$3.40+") {
		t.Errorf("cost on: idle tooltip %q", got)
	}
}
