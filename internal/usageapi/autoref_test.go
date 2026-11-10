package usageapi

import (
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

func TestLedgerFoundGroupNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, id := range []string{"ds-a", "ds-b"} {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"deepseek-flash"}}); err != nil {
			t.Fatal(err)
		}
	}
	for i, ref := range []string{"auto/deepseek-flash", "group/auto-deepseek-flash"} {
		usage.Append(usage.Record{Time: time.Now().Add(-time.Duration(i+1) * time.Minute), Agent: "pi", Provider: "relay", Model: ref, Requested: ref, Input: 10, Status: 200})
	}
	for _, model := range []string{"auto/deepseek-flash", "group/auto-deepseek-flash"} {
		if page := LedgerPage(usage.All, FilterOf(url.Values{"model": {model}}), 0, 20); len(page.Rows) != 2 {
			t.Fatalf("model filter %s dropped the other id: %d rows", model, len(page.Rows))
		}
	}
	page := LedgerPage(usage.All, FilterOf(url.Values{"model": {"auto/deepseek-flash"}}), 0, 20)
	for _, row := range page.Rows {
		if row.Model != "auto/deepseek-flash" || row.Requested != "auto/deepseek-flash" {
			t.Fatalf("ledger leaked internal name: model %q, requested %q", row.Model, row.Requested)
		}
	}
	if got := page.By["model"]; len(got) != 1 || got[0].ID != "auto/deepseek-flash" {
		t.Fatalf("model menu split: %+v", got)
	}
}
