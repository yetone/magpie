package provider

import (
	"testing"
	"time"
)

// Shinji_Zl on X asked for Claude's prepaid credits on its usage card, as
// Codex's are. Claude Code's /usage prints none, but the usage_report it
// carries has the month's spend of the extra-usage cap, which the gateway
// adds as a line (ClaudeExtraLine). The card shows it set aside from the
// plan's windows: a cap spent never marks the account used up, as Anthropic
// alone decides what a request spends.
func TestClaudeUsageCredits(t *testing.T) {
	now := time.Date(2026, 11, 11, 13, 0, 0, 0, time.UTC)
	base := "Current session: 10% used · resets 3pm (UTC)\nCurrent week (all models): 20% used · resets Nov 17 at 11am (UTC)\n"
	ws, err := parseClaudeUsage(base+"\n"+ClaudeExtraLine(27140, 50000, "USD"), now)
	if err != nil || len(ws) != 3 {
		t.Fatalf("windows %+v %v", ws, err)
	}
	x := ws[2]
	if x.Name != "Usage credits" || !x.Aside || x.Text || x.Amount != 271.4 || x.Limit != 500 || x.Unit != "USD" || x.Used < 54 || x.Used > 55 {
		t.Fatalf("credits row: %+v", x)
	}
	// spent to its cap, it is not the account spent
	ws, err = parseClaudeUsage(base+ClaudeExtraLine(50000, 50000, "usd"), now)
	if err != nil || len(ws) != 3 || ws[2].Used != 100 || ws[2].Unit != "USD" {
		t.Fatalf("spent cap: %+v %v", ws, err)
	}
	if usedUp(SubscriptionQuota{Windows: ws}, now) || usedPast(SubscriptionQuota{Windows: ws}, 98, now) {
		t.Fatal("a spent extra-usage cap read as the account used up")
	}
	// none said, none shown
	if ws, _ := parseClaudeUsage(base, now); len(ws) != 2 {
		t.Fatalf("windows without credits: %+v", ws)
	}
}
