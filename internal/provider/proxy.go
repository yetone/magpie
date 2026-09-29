package provider

import (
	"context"

	"github.com/yetone/magpie/internal/netproxy"
)

// Via is ctx for requests made on the provider's behalf: they go through
// its own proxy (Proxy), or the global one when it has none.
func (p Provider) Via(ctx context.Context) context.Context {
	return netproxy.With(ctx, p.Proxy)
}

// ProxyOf is the own proxy of the provider or signed-in account with id,
// "" when it follows the global one.
func ProxyOf(id string) string {
	for _, p := range load().Providers {
		if p.ID == id {
			return p.Proxy
		}
	}
	return ""
}

// Via is ctx for requests made on behalf of the provider or signed-in
// account with id (see Provider.Via).
func Via(ctx context.Context, id string) context.Context {
	return netproxy.With(ctx, ProxyOf(id))
}
