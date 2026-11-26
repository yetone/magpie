package cli

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// magpie quota auto-reset days sets the days a reset used by itself may
// have left (shangyuan0818 on X), says it alone, puts back the default,
// and refuses a range it can't read or out of order, writing nothing then.
func TestQuotaResetDaysCmd(t *testing.T) {
	groupsHome(t)
	if out, err := runOut(t, "quota", "auto-reset", "days"); err != nil || !strings.Contains(out, "(the default)") {
		t.Fatalf("unset: %q %v", out, err)
	}
	for _, c := range []struct {
		arg         string
		least, most int
		say         string
	}{
		{"0-7", 0, 7, "at most 7 days"},
		{"3-", 3, 0, "at least 3 days"},
		{"-7", 0, 7, "at most 7 days"},
		{"1-1", 1, 1, "1 to 1 days"},
		{"2-14", 2, 14, "2 to 14 days"},
	} {
		out, err := runOut(t, "quota", "auto-reset", "days", c.arg)
		if s := settings.Load(); err != nil || s.CodexResetDaysLeast != c.least || s.CodexResetDaysMost != c.most || !strings.Contains(out, c.say) {
			t.Fatalf("%s: %q %v, set %d–%d", c.arg, out, err, s.CodexResetDaysLeast, s.CodexResetDaysMost)
		}
	}
	if out, err := runOut(t, "quota", "auto-reset", "days"); err != nil || !strings.Contains(out, "2 to 14 days") {
		t.Fatalf("set: %q %v", out, err)
	}
	for _, bad := range []string{"8-7", "7", "a-b", "0-366", "1.5-3", "-1-3", "--help"} {
		if bad == "--help" {
			if out, err := runOut(t, "quota", "auto-reset", "days", bad); err != nil || !strings.Contains(out, "usage:") {
				t.Fatalf("--help: %q %v", out, err)
			}
		} else if _, err := runOut(t, "quota", "auto-reset", "days", bad); err == nil {
			t.Fatalf("%s: taken", bad)
		}
		if s := settings.Load(); s.CodexResetDaysLeast != 2 || s.CodexResetDaysMost != 14 {
			t.Fatalf("%s: wrote %d–%d", bad, s.CodexResetDaysLeast, s.CodexResetDaysMost)
		}
	}
	if out, err := runOut(t, "quota", "auto-reset", "days", "default"); err != nil || settings.Load().CodexResetDaysMost != 0 || !strings.Contains(out, "default") {
		t.Fatalf("default: %q %v", out, err)
	}
}
