package usage

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

func TestHermesAggregatesDoNotBecomeLedgerRequests(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	now := time.Now()
	records := []Record{{Time: now, Agent: "hermes", Session: "s", RequestID: "r", Input: 10, Output: 5, Status: 200}}
	logs := []sessions.Call{
		{Time: now, Agent: "hermes", Session: "s", RequestID: "r", Aggregate: true, APICalls: 4, Tokens: sessions.Tokens{Input: 40, Output: 20}},
		{Time: now, Agent: "hermes", Session: "s", RequestID: "r", Tokens: sessions.Tokens{Input: 10, Output: 5}},
	}
	matched := gatewayMatches(records, logs)
	if matched[0] || !matched[1] {
		t.Fatalf("an aggregate consumed a gateway request: %+v", matched)
	}
	rows, sum, _, _ := ledgerWith(time.Time{}, Filter{}, records, logs)
	if len(rows) != 1 || sum.Calls != 1 || sum.Input != 10 || sum.Output != 5 {
		t.Fatalf("aggregate counted as requests: rows=%+v sum=%+v", rows, sum)
	}
}
