package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// What the app's Usage page hides, a card or a card's rows, the TUI's Usage
// page leaves out too (onkashiko on X: a card hidden in the app's window
// still showed elsewhere), read again at every read of the cards, so a
// hide in the app reaches it with no restart. A card is never left with no
// row.
func TestUsagePageLeavesOutWhatTheAppHides(t *testing.T) {
	home(t)
	readQ := readQuotas
	t.Cleanup(func() { readQuotas = readQ })
	readQuotas = func(bool) ([]provider.SubscriptionQuota, bool) {
		return []provider.SubscriptionQuota{
			{Provider: "stepfun", Name: "StepFun", Plan: "Plus", Windows: []provider.QuotaWindow{{Name: "5h", Used: 10}}},
			{Provider: "stepfun", Name: "StepFun", Balance: "¥9.00"},
			{Provider: "codex", Name: "Codex", User: "a@b.c", Windows: []provider.QuotaWindow{{Name: "5h", Used: 20}}},
			{Provider: "codex", Name: "Codex", User: "x@y.z", Windows: []provider.QuotaWindow{{Name: "5h", Used: 30}}},
			{Provider: "antigravity", Name: "Antigravity", Windows: []provider.QuotaWindow{
				{Name: "Weekly", Used: 40, Pool: "Claude"}, {Name: "Daily", Used: 50, Pool: "Gemini"},
			}},
			{Provider: "kimi", Name: "Kimi", Windows: []provider.QuotaWindow{{Name: "Weekly", Used: 60}}},
		}, false
	}
	lines := func() string {
		t.Helper()
		msg := quotasCmd(false, 0)().(quotaMsg)
		return strings.Join(quotaLines(msg.qs, true, false, 200, time.Now()), "\n")
	}

	all := lines()
	for _, want := range []string{"¥9.00", "x@y.z", "60% used", "40% used", "50% used"} {
		if !strings.Contains(all, want) {
			t.Fatalf("nothing hidden: missing %q in\n%s", want, all)
		}
	}

	if err := settings.Save(settings.Settings{
		UsageCardsHidden:   []string{"stepfun|balance", "codex|x@y.z|plan"},
		UsageWindowsHidden: []string{"antigravity|Claude", "kimi|Weekly"},
	}); err != nil {
		t.Fatal(err)
	}
	got := lines()
	for _, gone := range []string{"¥9.00", "x@y.z", "40% used"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q is hidden in the app, but shows:\n%s", gone, got)
		}
	}
	// the plan card the balance was hidden from stays, the other account
	// stays, a pool not named stays, and Kimi's only row is never taken
	for _, want := range []string{"StepFun · Plus", "a@b.c", "50% used", "60% used"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}
