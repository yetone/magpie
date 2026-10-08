package usage

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func resetUsageTest(t *testing.T) {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	_ = provider.Path()
}

func TestGatewaySessionsRequiresExplicitIdentityAndKeepsModels(t *testing.T) {
	resetUsageTest(t)
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.Local)
	Append(Record{Time: at, Agent: "codex", Session: "s-1", Provider: "p", Model: "m1", Input: 10, Output: 2, Status: 200})
	Append(Record{Time: at.Add(time.Minute), Agent: "codex", Session: "s-1", Provider: "p", Model: "m2", Input: 3, Output: 4, Status: 200})
	Append(Record{Time: at, Agent: "codex", Provider: "p", Model: "m1", Input: 999, Output: 999, Status: 200})
	got := GatewaySessions(time.Time{}, nil)
	if len(got) != 1 || got[0].ID != "s-1" || len(got[0].Models) != 2 || got[0].Input != 13 || got[0].Output != 6 {
		t.Fatalf("sessions=%+v", got)
	}
}

func TestGatewaySessionsNativeIdentityWins(t *testing.T) {
	resetUsageTest(t)
	Append(Record{Time: time.Now(), Agent: "pi", Session: "native", Provider: "p", Model: "m", Input: 1, Status: 200})
	if got := GatewaySessions(time.Time{}, map[string]bool{"pi|native": true}); len(got) != 0 {
		t.Fatalf("got duplicate gateway session: %+v", got)
	}
}
