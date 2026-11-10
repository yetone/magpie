package usage

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
)

func TestUsageFoundGroupNames(t *testing.T) {
	resetUsageTest(t)
	for _, id := range []string{"ds-a", "ds-b"} {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"deepseek-flash"}}); err != nil {
			t.Fatal(err)
		}
	}
	at := Clock().Add(-time.Minute)
	for i, model := range []string{"group/auto-deepseek-flash", "auto/deepseek-flash"} {
		Append(Record{Time: at, Agent: "pi", Session: "auto-ref", Provider: "relay", Model: model, Input: (i + 1) * 10, Status: 200})
	}
	check := func() {
		t.Helper()
		got := GatewaySessions(time.Time{}, nil)
		if len(got) != 1 || len(got[0].Models) != 1 || got[0].Models[0].Model != "auto/deepseek-flash" || got[0].Input != 30 {
			t.Fatalf("gateway session split: %+v", got)
		}
		_, days := GatewaySessionWindow(time.Time{})
		if len(days) != 1 || days[0].Model != "auto/deepseek-flash" || days[0].Input != 30 {
			t.Fatalf("gateway days split: %+v", days)
		}
		if s := Summarize(All); len(s.Models) != 1 || s.Models[0].Model != "auto/deepseek-flash" || s.Input != 30 {
			t.Fatalf("usage overview split: %+v", s.Models)
		}
	}
	check()
	check() // cached reads
	for _, model := range []string{"auto/deepseek-flash", "group/auto-deepseek-flash"} {
		// the API takes either id to the kept one (usageapi.FilterOf)
		page := QueryPage(All, Filter{Model: provider.CanonicalRef(model)}, 0, 20)
		if page.Total != 2 || page.Sum.Input != 30 {
			t.Fatalf("request filter %s split group: %d rows, %d input", model, page.Total, page.Sum.Input)
		}
		for _, dim := range []string{"model", "modelAt"} {
			want := "auto/deepseek-flash"
			if dim == "modelAt" {
				want = "relay/" + want
			}
			if got := page.By[dim]; len(got) != 1 || got[0].ID != want || got[0].Input != 30 {
				t.Fatalf("%s ranking split group: %+v", dim, got)
			}
		}
	}

	// Clearing the displayed group also clears calls recorded under its old id.
	cfg := settings.Load()
	cfg.UsageCleared = []settings.UsageClear{{Provider: "relay", Model: "auto/deepseek-flash", At: Clock().UnixMilli()}}
	if err := settings.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if s := Summarize(All); s.Calls != 0 {
		t.Fatalf("old group id survived clear: %+v", s.Models)
	}
}

// Codex's model/effort rows are attached after the native session is listed.
// They use the same names and keep the cost of both original ids.
func TestCodexEffortFoundGroupNames(t *testing.T) {
	resetUsageTest(t)
	for _, id := range []string{"ds-a", "ds-b"} {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"deepseek-flash"}}); err != nil {
			t.Fatal(err)
		}
	}
	in := map[string][]sessions.ModelEffort{"thread": {
		{Model: "group/auto-deepseek-flash", Effort: "high", Tokens: sessions.Tokens{Input: 10}, Reasoning: 3, Cost: 2, Priced: true},
		{Model: "auto/deepseek-flash", Effort: "high", Tokens: sessions.Tokens{Input: 20}, Reasoning: 5, Cost: 7, Priced: false},
		{Model: "auto/deepseek-flash", Effort: "low", Tokens: sessions.Tokens{Input: 4}, Cost: 1, Priced: true},
	}}
	got := namedModelEffort(in, provider.UsageAlias())["thread"]
	if len(got) != 2 {
		t.Fatalf("effort rows split: %+v", got)
	}
	for _, row := range got {
		if row.Model != "auto/deepseek-flash" {
			t.Fatalf("effort's group: %+v", row)
		}
		if row.Effort == "high" && (row.Input != 30 || row.Reasoning != 8 || row.Cost != 9 || row.Priced) {
			t.Fatalf("effort totals changed: %+v", row)
		}
	}
	if len(in["thread"]) != 3 || in["thread"][0].Model != "group/auto-deepseek-flash" {
		t.Fatal("kept effort was changed")
	}
}
