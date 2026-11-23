package provider

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// will930128 on X: in an Opus 5.5 routing group the agy-cli account's
// share showed as not known, last behind every account known, while its
// card showed Claude & GPT's 5 hours and 7 days. agy-cli 0.1.114 set every
// window aside, so routing had none to weigh the account by and
// Allowances left it out. 0.1.115 sends each model's own window as the
// retired Antigravity plugin did: routing reads Opus 5.5 by Claude &
// GPT's share, and the pool's windows (still aside) hold it at a cap. The
// fixture is agy-cli 0.1.115's parseUsageReport of agy's live /usage
// report of 11-15 and its `agy models` rows; its resets are moved ahead
// of now, so none has passed.
func TestAgyCLIRoutedByItsModelsWindows(t *testing.T) {
	raw, err := os.ReadFile("testdata/agy-cli-0.1.115-usage.json")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	week := now.Add(4 * 24 * time.Hour).UTC().Format(time.RFC3339)
	five := now.Add(3 * time.Hour).UTC().Format(time.RFC3339)
	for _, old := range []string{"2026-11-20T09:01:10.000Z", "2026-11-22T11:19:33.000Z"} {
		raw = []byte(strings.ReplaceAll(string(raw), old, week))
	}
	raw = []byte(strings.ReplaceAll(string(raw), "2026-11-15T16:19:33.000Z", five))
	var u plugin.Usage
	if err := json.Unmarshal(raw, &u); err != nil {
		t.Fatal(err)
	}
	read := func(u plugin.Usage) SubscriptionQuota {
		return quotaOfPlugin(SubscriptionQuota{Provider: PluginID("agy-cli"), User: "a@gmail.com"}, u)
	}

	a, ok := routedAllowance("plugin:agy-cli", "a@gmail.com", read(u), now)
	if !ok {
		t.Fatal("routing has no allowance for the account its card shows")
	}
	for model, want := range map[string]float64{
		"claude-opus-5-5":        66.394961, // Claude & GPT's 7 days
		"claude-opus-5-5-high":   66.394961, // an effort level of it
		"gpt-oss-120b-medium":    66.394961,
		"gemini-4-hydrogen-high": 0,
		"gemini-4-argon":         0,
	} {
		used, renews := a.For(model, now)
		if used != want || len(renews) == 0 || renews[0].IsZero() {
			t.Errorf("%s: used %v renewing %v, want %v", model, used, renews, want)
		}
	}
	// the pool's own windows (Claude & GPT's 7 days and 5 hours) hold
	// its models at a cap the user set on the pool
	var held []string
	for _, l := range a.capLimits("claude-opus-5-5-high") {
		if l.Name != "Claude Opus 5.5" {
			held = append(held, l.Name)
		}
	}
	if len(held) != 2 || !strings.Contains(held[0], "7 days") || !strings.Contains(held[1], "5 hours") {
		t.Errorf("Opus 5.5 held at a cap by %q, want Claude & GPT's 7 days and 5 hours", held)
	}

	// 0.1.114's reading, every window aside: what the reporter saw
	for i := range u.Windows {
		u.Windows[i].Aside = true
	}
	if a, ok := routedAllowance("plugin:agy-cli", "a@gmail.com", read(u), now); ok {
		t.Fatalf("an all-aside reading gave routing %+v", a)
	}
}
