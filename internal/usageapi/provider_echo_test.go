package usageapi

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/usage"
)

// TestLedgerSaysItsOneProvider: a page filtered to one provider says which,
// so with the currency "by provider" its total and chart are shown in what
// that provider sells in (onkashiko on X); a page of every provider says
// none, and its sums go in the "Totals in" currency.
func TestLedgerSaysItsOneProvider(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	now := time.Now()
	usage.Append(usage.Record{Time: now.Add(-2 * time.Minute), Agent: "codex", Provider: "deepseek", Model: "deepseek/deepseek-v4", Status: 200, Input: 10, Output: 2})
	usage.Append(usage.Record{Time: now.Add(-time.Minute), Agent: "codex", Provider: "openai", Model: "openai/gpt-5", Status: 200, Input: 10, Output: 2})

	one := LedgerPage(usage.All, usage.Filter{Provider: "deepseek"}, 0, 100)
	if one.Provider != "deepseek" || one.Calls != 1 {
		t.Fatalf("filtered page: provider %q calls %d, want deepseek, 1", one.Provider, one.Calls)
	}
	if top := topLevel(t, one); top["provider"] != "deepseek" {
		t.Fatalf("the page reads provider %v, want deepseek", top["provider"])
	}
	all := LedgerPage(usage.All, usage.Filter{}, 0, 100)
	if _, ok := topLevel(t, all)["provider"]; ok || all.Provider != "" {
		t.Fatalf("the whole page names a provider %q", all.Provider)
	}
}

func topLevel(t *testing.T, l Ledger) map[string]any {
	t.Helper()
	b, err := json.Marshal(l)
	var m map[string]any
	if err == nil {
		err = json.Unmarshal(b, &m)
	}
	if err != nil {
		t.Fatal(err)
	}
	return m
}
