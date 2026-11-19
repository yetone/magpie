package cli

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// routing= on a provider spreads requests over its keys or accounts as
// the editor's Routing does: fast (fastest, speed) is Speed first; manual,
// cheapest, expiring and fit are a group's alone, and a name magpie
// doesn't know is refused.
func TestProviderRoutingPair(t *testing.T) {
	var p provider.Provider
	for in, want := range map[string]string{"fast": provider.Fastest, "fastest": provider.Fastest, "Speed": provider.Fastest, "order": provider.Ordered, "smart": ""} {
		if err := applyPairs(&p, []string{"routing=" + in}); err != nil || p.Routing != want {
			t.Errorf("routing=%s: %q %v, want %q", in, p.Routing, err, want)
		}
	}
	p.Routing = provider.Fastest
	for _, bad := range []string{"manual", "cheapest", "expiring", "fit"} {
		if err := applyPairs(&p, []string{"routing=" + bad}); err == nil || p.Routing != provider.Fastest {
			t.Errorf("routing=%s taken: %q", bad, p.Routing)
		}
	}
}
