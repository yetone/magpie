package gateway

import (
	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// keyRouted is g as a request with who's gateway key routes it: by the
// key's own routing (access.Key.Routing, justforlxz on X) when it sets
// one, else by the group's. A manual group sends to the member picked on
// its card whatever the key says, and a group in g, or a provider in it
// routed its own way, keeps its own routing as it always does (planLevel):
// the key's routing, as the group's, picks between them.
func keyRouted(who access.Identity, g provider.Group) provider.Group {
	switch {
	case who.Routing == "" || g.Routing == provider.Manual:
	case who.Routing == access.RoutingSmart:
		g.Routing = ""
	default:
		g.Routing = who.Routing
	}
	return g
}
