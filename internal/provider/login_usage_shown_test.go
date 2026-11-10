package provider

import (
	"context"
	"testing"
	"time"
)

// A Claude account whose provider is switched off showed "No allowance
// reported": LoginUsage answered nothing for it. The account lists show what
// was last known of it, or say it is switched off, and read nothing
// (Kunlin03 on X: a Claude Pro account, Claude Code switch off).
func TestLoginUsageShownOfSwitchedOffClaude(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(24*time.Hour))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	lastQuotas.Lock()
	lastQuotas.m, lastQuotas.loaded = nil, false
	lastQuotas.Unlock()
	writeFile(t, Path(), map[string]any{"providers": []map[string]any{{"id": "claude", "off": true}}})
	if !leftAlone("claude") {
		t.Fatal("claude not left alone")
	}
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	keepLast(SubscriptionQuota{Provider: "claude", User: "pro@example.com", ReadAt: &at,
		Windows: []QuotaWindow{{Name: "5 hours", Span: 5 * time.Hour, Used: 12}}}, "")

	got := keptLoginUsage([]Login{{Agent: "claude", User: "pro@example.com", Plan: "Pro"}, {Agent: "claude", User: "new@example.com", Plan: "Pro"}})
	if q := got["pro@example.com"]; q.Error != "" || len(q.Windows) != 1 || q.Windows[0].Used != 12 || !q.ReadDeferred {
		t.Fatalf("a read account shows its last reading: %+v", q)
	}
	if q := got["new@example.com"]; q.Error != errLeftAlone.Error() || len(q.Windows) != 0 {
		t.Fatalf("an account never read says it is switched off: %+v", q)
	}
	if n := len(LoginUsage(context.Background(), "claude")); n != 0 {
		t.Fatalf("LoginUsage, which callers act on, answered %d readings for a provider left alone", n)
	}
	if len(LoginUsageShown(context.Background(), "claude")) != len(Logins("claude")) {
		t.Fatal("LoginUsageShown skipped an account")
	}
}
