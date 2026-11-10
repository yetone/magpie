package provider

import (
	"testing"

	"github.com/yetone/magpie/internal/update"
)

// Qoder's plugin before 0.2.65 joined two tool calls Qoder streams at one
// index into one, even with their own ids and names, so the client got one
// tool_use whose input was {"key":"A"}{"key":"B"}, invalid JSON, and neither
// call ran (plugins#930). The movers keep every moved Qoder and Qoder CN user
// on a version that keeps them apart.
func TestQoderMoversKeepToolCallsAtOneIndexApart(t *testing.T) {
	for _, id := range []string{"qoder", QoderCNID} {
		if update.Newer("0.2.65", movers[id].min) {
			t.Fatalf("the %s mover installs opencode-qoder-auth %s, which joins two tool calls at one index into invalid JSON", id, movers[id].min)
		}
	}
}
