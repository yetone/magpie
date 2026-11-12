package agent

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// dsh can be told a window of its own for a model (ul3140773077392 on X:
// it compacts earlier than Codex at the same window). The list written
// for dsh carries it as contextWindow, the one written for Codex and the
// model's own stay as they were.
func TestDshOwnContextWindow(t *testing.T) {
	syncHome(t)
	window := func(agent string) int {
		t.Helper()
		for _, m := range magpieModels(agent) {
			if m.ID == "relay/glm-4.6" {
				return m.Context
			}
		}
		t.Fatalf("%s: no relay/glm-4.6", agent)
		return 0
	}
	if window("dsh") != 204800 {
		t.Fatalf("to begin with: %d", window("dsh"))
	}
	if err := provider.SetAgentContexts("dsh", map[string]int{"relay/glm-4.6": 400000}); err != nil {
		t.Fatal(err)
	}
	if got := window("dsh"); got != 400000 {
		t.Errorf("dsh is told %d; want 400000", got)
	}
	if got := window("codex"); got != 204800 {
		t.Errorf("codex is told %d; want the model's own, 204800", got)
	}
	if err := provider.SetAgentContexts("dsh", nil); err != nil {
		t.Fatal(err)
	}
	if got := window("dsh"); got != 204800 {
		t.Errorf("taken away: %d", got)
	}
}
