package provider

import (
	"strings"
	"testing"
	"time"
)

// Codex's footer: the accounts of the provider the thread went to (the
// one that answered last of them), a relay key by its balance; with no
// route, or none of that provider's, the account that answered last.
func TestCodexStatuslineCards(t *testing.T) {
	now := time.Now()
	at := func(d time.Duration) *time.Time { x := now.Add(d); return &x }
	qs := []Quota{
		{Provider: "claude", User: "a@x", LastServedAt: at(-time.Minute), Last: true, Windows: []QuotaSpan{{Name: "5 hours", Used: 62}}},
		{Provider: "codex", User: "c@x", LastServedAt: at(-time.Hour), Windows: []QuotaSpan{{Name: "5 hours", Used: 10}}},
		{Provider: "codex", User: "d@x", LastServedAt: at(-2 * time.Minute), Windows: []QuotaSpan{{Name: "5 hours", Used: 20}}},
		{Provider: "relay", Kind: "balance", Balance: "$4.20", Alias: "work"},
		{Provider: "empty", Kind: "balance"},
	}
	name := func(cs []Quota) string {
		var out []string
		for _, q := range cs {
			out = append(out, CodexStatuslineCard(q, now))
		}
		return strings.Join(out, " · ")
	}
	for model, want := range map[string]string{
		"codex/gpt-5.5": "codex d@x 5h 20%",
		"relay/glm-5":   "relay work $4.20",
		"claude/opus":   "claude a@x 5h 62%",
		"empty/m":       "claude a@x 5h 62%",
		"":              "claude a@x 5h 62%",
		"gpt-5.5":       "claude a@x 5h 62%",
	} {
		if got := name(CodexStatuslineCards(model, qs)); got != want {
			t.Errorf("%q: %q, want %q", model, got, want)
		}
	}
}
