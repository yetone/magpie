// Package rotation is the routings a provider or group can be set to
// besides Smart: In order, Until used up, In turn, Least used first,
// Use before expiry, By weight, Free first, Speed first and Cheapest first (a
// group's). While a provider or group is routed by one, it
// stays on.
//
// It is the first consumer of the gateway's router strategy point (P3 of
// docs/subsystems/plugin-runtime.md): Apply registers internal/routes'
// strategies, and off, a pool set to one of them is tried in its listed
// order and the window offers Smart alone.
package rotation

import (
	"github.com/yetone/magpie/internal/pluginrt"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/routes"
)

func init() {
	pluginrt.Register(pluginrt.Manifest{
		ID: "rotation", API: "^1", Requires: []string{"providers", "gateway"}, Essential: true,
		Name: "Routing strategies", Sub: "The routings besides Smart: In order, Until used up, In turn, Least used first, Use before expiry, By weight, Free first, Speed first and Cheapest first",
		Opens: "providers",
		One:   "1 provider or group routed so", Many: "{n} providers and groups routed so",
		InUse: inUse,
		Parts: []pluginrt.Part{{Slot: "part", Key: "rotation"}},
		Apply: func(c *pluginrt.Ctx) error {
			for name, s := range routes.Builtin {
				c.OnDispose(routes.Register(name, s))
			}
			return nil
		},
	})
}

// inUse counts the providers and groups routed by one of its strategies;
// a providers.json that can't be read is not known, so it stays on.
func inUse() (int, error) {
	routings, err := provider.Routings()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range routings {
		if _, ok := routes.Builtin[r]; ok {
			n++
		}
	}
	return n, nil
}
