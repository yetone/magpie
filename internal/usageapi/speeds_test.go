package usageapi

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/usage"
)

// TestLedgerSendsTheModelSpeedRanking: the Requests tab's ranking reads each
// model's median speed and first token off the ledger page, with the least
// tokens a request wrote to count; an empty ledger sends a list, not null.
func TestLedgerSendsTheModelSpeedRanking(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	empty, _ := json.Marshal(LedgerPage(usage.All, usage.Filter{}, 0, 100))
	if !strings.Contains(string(empty), `"speeds":[]`) {
		t.Fatalf("an empty ledger sends %s", empty)
	}
	now := time.Now()
	for i := range 3 {
		usage.Append(usage.Record{Time: now.Add(-time.Duration(i+1) * time.Minute), Agent: "codex", Provider: "openai", Model: "gpt-5.5", Status: 200, Input: 10, Output: 500, TTFT: 2000, Millis: 12000})
	}
	usage.Append(usage.Record{Time: now.Add(-10 * time.Minute), Agent: "codex", Provider: "openai", Model: "gpt-5.5", Status: 500, Error: "boom", Output: 500, TTFT: 100, Millis: 200})
	l := LedgerPage(usage.All, usage.Filter{}, 0, 100)
	if l.SpeedMin != usage.MinRankTokens || len(l.Speeds) != 1 {
		t.Fatalf("speeds %+v, min %d", l.Speeds, l.SpeedMin)
	}
	s := l.Speeds[0]
	// 500 tokens in the 10 s after the first token: 50 tok/s, within a bin
	if s.Requests != 3 || s.Speed < 49 || s.Speed > 51 || s.TTFT < 1950 || s.TTFT > 2050 || s.Name == "" {
		t.Fatalf("%+v", s)
	}
	b, _ := json.Marshal(l)
	for _, k := range []string{`"speeds":[{`, `"tps":`, `"ttft_ms":`, `"requests":3`, `"speed_min":100`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("the page lacks %s", k)
		}
	}
}
