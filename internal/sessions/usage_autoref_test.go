package sessions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

func TestSessionsFoundGroupNames(t *testing.T) {
	inZone(t, 0)
	claude, _ := setup(t)
	PriceOf = priceOf
	for _, id := range []string{"ds-a", "ds-b"} {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"deepseek-flash"}}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := settings.Load()
	cfg.ModelPrices = map[string]settings.ModelPrice{
		"*/auto-deepseek-flash": {Input: new(2.0), Output: new(0.0), CacheRead: new(0.0), CacheWrite: new(0.0)},
		"*/deepseek-flash":      {Input: new(5.0), Output: new(0.0), CacheRead: new(0.0), CacheWrite: new(0.0)},
	}
	if err := settings.Save(cfg); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(claude, "projects", "-work-alias")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "44444444-2222-3333-4444-555555555555"
	body := aliasMsg("m1", "group/auto-deepseek-flash", "2026-09-25T10:00:00Z", 1000000) + aliasMsg("m2", "auto/deepseek-flash", "2026-09-25T10:01:00Z", 1000000)
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	check := func(s Session) {
		t.Helper()
		if len(s.Models) != 1 || s.Models[0].Model != "auto/deepseek-flash" || s.Input != 2000000 || s.Models[0].Input != 2000000 || !near(s.Cost, 7) || !near(s.Models[0].Cost, 7) {
			t.Fatalf("split group or changed price: %+v", s)
		}
	}
	for range 2 { // the kept parse still has its original ids and prices
		var found bool
		for _, s := range List(All) {
			if s.ID == id {
				found = true
				check(s)
			}
		}
		if !found {
			t.Fatal("missing native session")
		}
	}
	if s, ok := Get("claude:" + id); !ok {
		t.Fatal("Get missing session")
	} else {
		check(s)
	}
	if s, ok := Find("claude", id); !ok {
		t.Fatal("Find missing session")
	} else {
		check(s)
	}
	for _, list := range [][]Managed{ListAgent("claude"), Manage(nil, func([]AgentCount, map[string]map[string]bool) string { return "claude" }).Sessions} {
		found := false
		for _, s := range list {
			if s.ID == id {
				found = true
				check(s.Session)
			}
		}
		if !found {
			t.Fatal("management list lost session")
		}
	}
	st := statsAt(0, statsNow)
	var rows []Usage
	for _, d := range st.Days {
		for _, u := range d.Usage {
			if u.Cwd == "/work/alias" {
				rows = append(rows, u)
			}
		}
	}
	if len(rows) != 1 || rows[0].Model != "auto/deepseek-flash" || rows[0].Input != 2000000 || !near(rows[0].Cost, 7) {
		t.Fatalf("model filter would split: %+v", rows)
	}
	for _, s := range st.Sessions {
		if s.ID == id && (len(s.Models) != 1 || s.Models[0] != "auto/deepseek-flash") {
			t.Fatalf("summary split: %+v", s)
		}
	}
	if got := st.Overview("claude", "auto/deepseek-flash", "/work/alias", 8); got.Count != 1 {
		t.Fatalf("overview lost group: %+v", got)
	}
}
