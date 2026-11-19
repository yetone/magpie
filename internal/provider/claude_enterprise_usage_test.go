package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// @andy_chen1997 on X signed a Claude Enterprise seat in, and the card
// said Claude Code's /usage was "temporarily unavailable": -p /usage
// printed only the subscription line. Claude Code 2.1.328 draws its text
// from the usage endpoint's five_hour and seven_day, while the rows it
// carries in usage_report (limits[]) can hold the seat's 5 hours alone;
// the gateway adds those as /usage's own lines, the reset as written
// (ISO 8601). No Enterprise account was at hand: the fixtures follow
// Claude Code's schema (GPi) and its handling (Y, S, xNr, vmr, fD).
func TestClaudeEnterpriseUsage(t *testing.T) {
	now := time.Date(2026, 11, 19, 13, 0, 0, 0, time.UTC)
	const sub = "You are currently using your subscription to power your Claude Code usage"

	// the rows' 5 hours
	ws, err := parseClaudeUsage(sub+"\nCurrent session: 37.5% used · resets 2026-11-19T15:00:00.000Z", now)
	if err != nil || len(ws) != 1 {
		t.Fatalf("rows' session: %+v %v", ws, err)
	}
	if w := ws[0]; w.Name != "5 hours" || w.Used != 37.5 || w.ResetsAt == nil || !w.ResetsAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("rows' session window: %+v", w)
	}

	// the server reported no meters: a plan without them, no error
	for _, text := range []string{sub + "\n" + ClaudeNoLimitsLine, sub + "\n" + ClaudeNoLimitsLine + "\n" + ClaudeExtraLine(1200, 50000, "USD")} {
		ws, err := parseClaudeUsage(text, now)
		if err != nil || len(ws) == 0 {
			t.Fatalf("no meters: %+v %v", ws, err)
		}
		if w := ws[0]; !w.Text || !w.Aside || w.Display != "None on this plan" {
			t.Fatalf("no meters window: %+v", w)
		}
		if usedUp(SubscriptionQuota{Windows: ws}, now) || usedPast(SubscriptionQuota{Windows: ws}, 98, now) {
			t.Fatal("a plan with no limits read as used up")
		}
	}
	if ws, _ := parseClaudeUsage(sub+"\n"+ClaudeNoLimitsLine+"\n"+ClaudeExtraLine(1200, 50000, "USD"), now); len(ws) != 2 || ws[1].Name != "Usage credits" {
		t.Fatalf("no meters with usage credits: %+v", ws)
	}

	// the subscription line alone, the report saying nothing: unknown,
	// as before
	if _, err := parseClaudeUsage(sub, now); !errors.Is(err, errClaudeUsageUnavailable) {
		t.Fatalf("subscription line alone: %v", err)
	}
}

// A usage-based seat (Claude Code's vmr and fD: no rate limits) whose
// /usage draws nothing is told as having no limits, from what Claude Code
// saved of its organization; any other account's stays unknown.
func TestClaudeUsageBasedSeat(t *testing.T) {
	t.Cleanup(func() { UsageClaudeVia(nil) })
	UsageClaudeVia(func(context.Context, string) (string, error) {
		return "You are currently using your subscription to power your Claude Code usage", nil
	})
	for _, c := range []struct {
		name, account string
		none          bool
	}{
		{"usage-based enterprise seat", `{"seatTier":"enterprise_usage_based","emailAddress":"a@example.com"}`, true},
		{"billed by usage", `{"billingType":"usage_based"}`, true},
		{"standard enterprise seat", `{"seatTier":"enterprise_standard"}`, false},
		{"no seat", `{"emailAddress":"a@example.com"}`, false},
		{"no file", ``, false},
	} {
		dir := t.TempDir()
		if c.account != "" {
			if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"oauthAccount":`+c.account+`}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		ws, err := readClaudeUsage(context.Background(), dir)
		if c.none {
			if err != nil || len(ws) != 1 || ws[0].Display != "None on this plan" {
				t.Errorf("%s: %+v %v", c.name, ws, err)
			}
		} else if !errors.Is(err, errClaudeUsageUnavailable) {
			t.Errorf("%s: %+v %v, want unavailable", c.name, ws, err)
		}
	}
}
