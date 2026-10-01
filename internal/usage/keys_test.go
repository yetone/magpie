package usage

import (
	"encoding/csv"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

func TestKeyUsageKeepsIdentityAndPrices(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"vendor":{"id":"vendor","models":{"m":{"id":"m","cost":{"input":2,"output":8,"cache_read":0.5,"cache_write":2.5}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	for _, id := range []string{"relay", "other"} {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "personal-secret", KeyName: "Personal",
			Chat: "https://relay.example/v1", Catalog: "vendor"}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	personal, team := provider.KeyID("personal-secret"), provider.KeyID("team-secret")
	recs := []Record{
		{Time: now.Add(-40 * 24 * time.Hour), Provider: "relay", Model: "m", ProviderKeyID: team, Input: 9999},
		{Time: now.Add(-time.Minute), Provider: "relay", Model: "m", Input: 3, Output: 1, Status: 200},
		{Time: now, Provider: "relay", Model: "m", ProviderKeyID: personal, ProviderKeyName: "Old name", Input: 100, Output: 10, CacheRead: 40, CacheWrite: 20, Status: 200},
		{Time: now, Provider: "relay", Model: "m", ProviderKeyID: personal, ProviderKeyName: "Personal", Input: 50, Output: 5, Status: 200},
		{Time: now, Provider: "relay", Model: "m", ProviderKeyID: team, ProviderKeyName: "Team", Input: 200, Output: 20, Status: 200},
		{Time: now, Provider: "relay", Model: "m", ProviderKeyID: team, ProviderKeyName: "Team", Status: 429},
		{Time: now, Provider: "other", Model: "m", ProviderKeyID: personal, ProviderKeyName: "Personal", Input: 7, Status: 200},
		{Time: now, Provider: "subscription", Model: "m", Host: "user@example.com", Input: 9, Status: 200},
	}
	s := summarize(Month, now, recs)
	if len(s.ProviderKeys) != 4 || s.Calls != 7 || s.Errors != 1 {
		t.Fatalf("summary: %+v", s)
	}
	byID := map[string]Group{}
	for _, g := range s.ProviderKeys {
		byID[g.ID] = g
	}
	p := byID["relay#"+personal]
	wantCost := (150*2 + 15*8 + 40*0.5 + 20*2.5) / 1e6
	if p.ProviderKeyName != "Personal" || p.Calls != 2 || p.Input != 150 || p.CacheRead != 40 || p.CacheWrite != 20 || math.Abs(p.Cost-wantCost) > 1e-9 {
		t.Fatalf("personal: %+v", p)
	}
	if g := byID["relay#"+team]; g.Calls != 2 || g.Errors != 1 || g.Input != 200 {
		t.Fatalf("team: %+v", g)
	}
	if g := byID["relay#"]; g.Calls != 1 || g.Input != 3 || g.ProviderKeyID != "" {
		t.Fatalf("unattributed: %+v", g)
	}
	if g := byID["other#"+personal]; g.Calls != 1 || g.Input != 7 {
		t.Fatalf("same key on another provider: %+v", g)
	}
	// A removed key stays in the history; changing the provider id carries
	// its records along without assigning the old, untracked calls to a key.
	if err := provider.Rename("relay", "renamed"); err != nil {
		t.Fatal(err)
	}
	s = summarize(Month, now, recs)
	for _, g := range s.ProviderKeys {
		if g.Provider == "relay" {
			t.Fatalf("old provider id: %+v", g)
		}
	}
	rows, _, _ := ledger(Month.Since(now), Filter{Query: "TEAM"}, recs)
	if len(rows) != 2 {
		t.Fatalf("key name search: %+v", rows)
	}
	var b strings.Builder
	if err := WriteCSV(&b, rows); err != nil {
		t.Fatal(err)
	}
	cells, err := csv.NewReader(strings.NewReader(b.String())).ReadAll()
	if err != nil || len(cells) != 3 || cells[1][slices.Index(CSVHeader, "provider_key_id")] != team || cells[1][slices.Index(CSVHeader, "provider_key_name")] != "Team" {
		t.Fatalf("CSV: %v, %s", err, b.String())
	}
	encoded, _ := json.Marshal(s)
	if strings.Contains(string(encoded)+b.String(), "team-secret") || strings.Contains(string(encoded)+b.String(), "personal-secret") {
		t.Fatal("raw key in usage output")
	}
}
