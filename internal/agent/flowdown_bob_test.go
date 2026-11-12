package agent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/usage"
)

// shannnnnks on X asked for FlowDown's and Bob's icons: their requests
// carry the system networking's User-Agent, naming the app first, and are
// drawn with the app's own icon.
func TestFlowDownAndBobAreClients(t *testing.T) {
	for ua, want := range map[string]string{
		"FlowDown/1234 CFNetwork/1568.100.1 Darwin/24.0.0": "flowdown",
		"Bob/2.0.1 CFNetwork/1568.100.1 Darwin/24.0.0":     "bob",
		"Bobby/1.0": "Bobby",
	} {
		if got := usage.AgentOf(ua); got != want {
			t.Errorf("%q: %q, want %q", ua, got, want)
		}
	}
	for _, id := range []string{"flowdown", "bob"} {
		if !slices.ContainsFunc(Clients(), func(a *Agent) bool { return a.ID == id && a.Icon == id+"-color" }) {
			t.Errorf("%s isn't a client the Usage views draw", id)
		}
		if _, err := os.Stat(filepath.Join("..", "gui", "assets", "icons", id+"-color.svg")); err != nil {
			t.Errorf("%s has no icon file: %v", id, err)
		}
	}
}
