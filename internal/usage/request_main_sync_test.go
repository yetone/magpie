package usage

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// The Requests cache must follow the same upstream names as Ledger, even
// when the log is unchanged, and retain remote routing metadata when packed.
func TestRequestPageUpstreamNamesAndRemoteRouting(t *testing.T) {
	pageHome(t)
	now := time.Now()
	for i := 0; i < 80; i++ {
		Append(Record{Time: now.Add(time.Duration(i) * time.Second), Agent: "codex", Via: "magpie", Provider: "relay", Model: "alias", Served: "vendor-model", Input: 10, Output: 1, Status: 200})
	}
	check := func(swapped, routed bool) {
		t.Helper()
		for repeat := 0; repeat < 2; repeat++ {
			page := QueryPage(All, Filter{}, 0, 100)
			ledger := LedgerOf(All, Filter{})
			for _, rows := range [][]Row{page.Rows, ledger.Rows} {
				if len(rows) != 80 {
					t.Fatalf("rows = %d", len(rows))
				}
				for _, row := range rows {
					if row.Swapped != swapped || row.Routed != routed || row.Via != "magpie" {
						t.Fatalf("lost model mapping or remote metadata: %+v", row)
					}
				}
			}
		}
	}
	check(true, false)
	cfg := settings.Settings{ModelWires: map[string]string{"relay/alias": "vendor-model"}}
	if err := settings.Save(cfg); err != nil {
		t.Fatal(err)
	}
	check(false, false)
	cfg.ModelWires["relay/alias"] = "group/auto"
	if err := settings.Save(cfg); err != nil {
		t.Fatal(err)
	}
	check(false, true)
	cfg.ModelWires = nil
	if err := settings.Save(cfg); err != nil {
		t.Fatal(err)
	}
	check(true, false)
}
