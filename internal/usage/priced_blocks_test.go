package usage

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

func TestRequestPageAppendDoesNotRepriceHistory(t *testing.T) {
	pageHome(t)
	historyLog(t, 8000)
	QueryPage(All, Filter{}, 0, 100)
	raw := readLogSnapshot().blocks[0]
	requestCache.Lock()
	before := requestCache.gateways[raw].Archive
	requestCache.Unlock()
	allocations := testing.AllocsPerRun(2, func() {
		Append(Record{Agent: "codex", Provider: "relay", Model: "m", Input: 1, Status: 200})
		QueryPage(All, Filter{}, 0, 100)
	})
	if allocations > 3000 {
		t.Fatalf("append re-priced unchanged history: %.0f allocations", allocations)
	}
	requestCache.Lock()
	after := requestCache.gateways[raw].Archive
	requestCache.Unlock()
	if len(before) == 0 || len(after) == 0 || &before[0] != &after[0] {
		t.Fatal("sealed gateway block was re-priced after append")
	}
	for _, period := range []Period{All, Today, Week} {
		got := QueryPage(period, Filter{}, 0, 100)
		want := pageFromLedger(period, Filter{}, 0, 100, LedgerOf(period, Filter{}))
		equalPage(t, got, want)
	}
}

func TestRequestPageRepricesGatewayBlocks(t *testing.T) {
	pageHome(t)
	s := settings.Load()
	s.ModelPrices = map[string]settings.ModelPrice{"*/m": {Input: new(2.0), Output: new(8.0), CacheRead: new(0.0), CacheWrite: new(0.0)}}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	Append(Record{Agent: "codex", Provider: "relay", Model: "m", Input: 1000000, Status: 200})
	if got := QueryPage(All, Filter{}, 0, 100).Sum.Cost; got != 2 {
		t.Fatal(got)
	}
	s.ModelPrices["*/m"] = settings.ModelPrice{Input: new(4.0), Output: new(8.0), CacheRead: new(0.0), CacheWrite: new(0.0)}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if got := QueryPage(All, Filter{}, 0, 100).Sum.Cost; got != 4 {
		t.Fatal(got)
	}
}

func TestRequestPageInvalidatesLivePrice(t *testing.T) {
	pageHome(t)
	p := provider.Provider{ID: "cline-relay", Chat: "https://api.cline.bot/v1", Key: "x", Catalog: "openai"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	Append(Record{Time: time.Now(), Agent: "codex", Provider: p.ID, Model: "m", Input: 1000000, Status: 200})
	if err := catalog.SaveLive(p.ID, p.Chat, []catalog.Model{{ID: "m", Free: false}}); err != nil {
		t.Fatal(err)
	}
	if got := QueryPage(All, Filter{}, 0, 100).Sum.Cost; got == 0 {
		t.Fatal("need paid baseline")
	}
	if err := catalog.SaveLive(p.ID, p.Chat, []catalog.Model{{ID: "m", Free: true}}); err != nil {
		t.Fatal(err)
	}
	if got := QueryPage(All, Filter{}, 0, 100).Sum.Cost; got != 0 {
		t.Fatal(got)
	}
}

func BenchmarkRequestPageAfterAppend(b *testing.B) {
	b.Setenv("HOME", b.TempDir())
	b.Setenv("XDG_CONFIG_HOME", b.TempDir())
	b.Setenv("XDG_CACHE_HOME", b.TempDir())
	historyLog(b, 100000)
	QueryPage(All, Filter{}, 0, 100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Append(Record{Agent: "codex", Provider: "relay", Model: "m", Input: 1, Status: 200})
		QueryPage(All, Filter{}, 0, 100)
	}
}
