package agent

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/usage"
)

// Yayoi_no_yume on X: DimCode (dimcode.dev) calling a Docker magpie by a
// gateway key read as the cube and "DimAgent". Its requests are DimCode's,
// drawn with its mark, and are not the Dim alpha magpie starts.
func TestDimCodeByUserAgent(t *testing.T) {
	for ua, want := range map[string]string{
		"DimAgent/0.5.29":     "dimcode",
		"DimCode/0.5.29":      "dimcode",
		"DimAgent":            "dimcode",
		"dimcode/dev":         "dimcode",
		"dimcode-alpha/0.1.0": "dim",
		"dima/0.1.0":          "dim",
		"dimmer/1.0":          "dimmer",
	} {
		if got := usage.AgentOf(ua); got != want {
			t.Errorf("%q: %q, want %q", ua, got, want)
		}
	}
	if !slices.ContainsFunc(Clients(), func(a *Agent) bool {
		return a.ID == "dimcode" && a.Name == "DimCode" && a.Icon == "dim-color"
	}) {
		t.Error("DimCode isn't a client the Routing and Usage views draw")
	}
}
