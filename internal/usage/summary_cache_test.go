package usage

import (
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"os"
	"testing"
	"time"
)

func TestSummaryInvalidatesLiveFreePrice(t *testing.T) {
	pageHome(t)
	if err := provider.Save(provider.Provider{ID: "cline-relay", Chat: "https://api.cline.bot/v1", Key: "x", Catalog: "openai"}); err != nil {
		t.Fatal(err)
	}
	Append(Record{Time: time.Now(), Agent: "codex", Provider: "cline-relay", Model: "m", Input: 1000000, Status: 200})
	if err := catalog.SaveLive("cline-relay", "https://api.cline.bot/v1", []catalog.Model{{ID: "m", Free: false}}); err != nil {
		t.Fatal(err)
	}
	first := Summarize(Today)
	if first.Cost == 0 {
		t.Fatalf("need paid baseline: %#v", first.Totals)
	}
	if err := catalog.SaveLive("cline-relay", "https://api.cline.bot/v1", []catalog.Model{{ID: "m", Free: true}}); err != nil {
		t.Fatal(err)
	}
	direct := summarize(Today, time.Now(), Load(time.Time{}))
	if direct.Cost != 0 {
		t.Fatalf("need free direct summary: %#v", direct.Totals)
	}
	if next := Summarize(Today); next.Cost != direct.Cost {
		t.Fatalf("cached tariff stale: cached=%v direct=%v", next.Cost, direct.Cost)
	}
}

func TestSummaryInvalidatesSameStampPrice(t *testing.T) {
	pageHome(t)
	s := settings.Load()
	s.ModelPrices = map[string]settings.ModelPrice{"*/m": {Input: new(2.0), Output: new(8.0), CacheRead: new(0.0), CacheWrite: new(0.0)}}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	Append(Record{Time: time.Now(), Agent: "codex", Provider: "relay", Model: "m", Input: 1000000, Status: 200})
	first := Summarize(Today)
	if first.Cost != 2 {
		t.Fatalf("need paid baseline: %#v", first.Totals)
	}
	old, err := os.Stat(settings.Path())
	if err != nil {
		t.Fatal(err)
	}
	s = settings.Load()
	s.ModelPrices = map[string]settings.ModelPrice{"*/m": {Input: new(4.0), Output: new(8.0), CacheRead: new(0.0), CacheWrite: new(0.0)}}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(settings.Path(), old.ModTime(), old.ModTime()); err != nil {
		t.Fatal(err)
	}
	direct := summarize(Today, time.Now(), Load(time.Time{}))
	if direct.Cost != 4 {
		t.Fatalf("direct failed coarse-stamp guard: %#v", direct.Totals)
	}
	if next := Summarize(Today); next.Cost != direct.Cost {
		t.Fatalf("cached override stale: cached=%v direct=%v", next.Cost, direct.Cost)
	}
}
