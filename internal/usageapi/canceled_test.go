package usageapi

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/usage"
)

// TestLedgerTellsCanceledRows: the Requests page reads a request its agent
// canceled — a person stopped the reply, or sent the next message while it
// came — as no failure. Its row says canceled, the summary counts it
// apart from the failed ones, and the Failed filter leaves it out.
func TestLedgerTellsCanceledRows(t *testing.T) {
	// a usage log of its own: the package's other tests read the shared one
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	now := time.Now()
	const model = "copilot/claude-opus-5.5"
	usage.Append(usage.Record{Time: now.Add(-3 * time.Minute), Agent: "claude-desktop", Provider: "copilot", Model: model, Status: usage.StatusCanceled, Error: usage.AgentCanceled, Millis: 27000})
	usage.Append(usage.Record{Time: now.Add(-2 * time.Minute), Agent: "claude-desktop", Provider: "copilot", Model: model, Status: 400, Error: "Copilot: unsupported beta header(s): tool-search-tool-2025-10-19"})
	usage.Append(usage.Record{Time: now.Add(-time.Minute), Agent: "claude-desktop", Provider: "copilot", Model: model, Status: 200, Input: 10, Output: 2, Millis: 3000})

	l := LedgerPage(usage.All, usage.Filter{}, 0, 100)
	if l.Calls != 3 || l.Errors != 1 || l.Canceled != 1 {
		t.Fatalf("calls %d errors %d canceled %d, want 3, 1, 1", l.Calls, l.Errors, l.Canceled)
	}
	var canceled []Row
	for _, r := range l.Rows {
		if r.Canceled {
			canceled = append(canceled, r)
		}
	}
	if len(canceled) != 1 || canceled[0].Status != usage.StatusCanceled {
		t.Fatalf("rows said canceled: %+v, want the 499 alone", canceled)
	}
	b, err := json.Marshal(canceled[0])
	if err != nil || !strings.Contains(string(b), `"canceled":true`) || !strings.Contains(string(b), `"status":499`) {
		t.Fatalf("the page reads the row as %s (%v)", b, err)
	}
	failed := LedgerPage(usage.All, usage.Filter{Failed: true}, 0, 100)
	if len(failed.Rows) != 1 || failed.Rows[0].Status != 400 {
		t.Fatalf("the Failed filter kept %+v, want the 400 alone", failed.Rows)
	}
}
