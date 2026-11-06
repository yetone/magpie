package cli

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// magpie quota auto-reset lead sets how long before a reset about to
// expire it is used, says it alone, puts back the default, and refuses a
// value out of range or not whole minutes, writing nothing then.
func TestQuotaResetLeadCmd(t *testing.T) {
	groupsHome(t)
	if out, err := runOut(t, "quota", "auto-reset", "lead"); err != nil || !strings.Contains(out, "used 30m before it expires (the default)") {
		t.Fatalf("unset: %q %v", out, err)
	}
	for _, c := range []struct {
		arg  string
		want int
	}{{"2h", 120}, {"15m", 15}, {"3m", 3}, {"5m", 5}, {"10m", 10}, {"24h", 1440}, {"90", 90}, {"1h30m", 90}} {
		arg, want := c.arg, c.want
		if out, err := runOut(t, "quota", "auto-reset", "lead", arg); err != nil || settings.Load().CodexResetLead != want {
			t.Fatalf("%s: %q %v, set %d", arg, out, err, settings.Load().CodexResetLead)
		}
	}
	if out, err := runOut(t, "quota", "auto-reset", "lead"); err != nil || !strings.Contains(out, "used 1h30m before it expires") || strings.Contains(out, "default") {
		t.Fatalf("set: %q %v", out, err)
	}
	for _, bad := range []string{"2m", "1m", "25h", "-1h", "30s", "90s", "soon", "--help"} {
		if bad == "--help" {
			if out, err := runOut(t, "quota", "auto-reset", "lead", bad); err != nil || !strings.Contains(out, "usage:") {
				t.Fatalf("--help: %q %v", out, err)
			}
		} else if _, err := runOut(t, "quota", "auto-reset", "lead", bad); err == nil {
			t.Fatalf("%s: taken", bad)
		}
		if got := settings.Load().CodexResetLead; got != 90 {
			t.Fatalf("%s: wrote %d", bad, got)
		}
	}
	if out, err := runOut(t, "quota", "auto-reset", "lead", "default"); err != nil || settings.Load().CodexResetLead != 0 || !strings.Contains(out, "30m") {
		t.Fatalf("default: %q %v", out, err)
	}
}
