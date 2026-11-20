package gateway

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// tellCaller tells a plugin's fetch the agent its request came from, as
// the gateway records it (callerFrom): the agent's id and name, and the
// computers a request another magpie passed on came through (@febsincos on
// X: a plugin on the receiving magpie couldn't tell which agent asked).
// The same for a request from an agent here and one passed on: the agent,
// never the magpie that passed it on. Headers the caller sent of these
// names are dropped first. The plugin host takes them off whatever the
// plugin sends on (host.js), so no vendor is told.
func tellCaller(ctx context.Context, h http.Header) {
	h.Del(provider.CallerAgentHeader)
	h.Del(provider.CallerNameHeader)
	h.Del(provider.CallerViaHeader)
	c, ok := ctx.Value(callerCtx{}).(caller)
	if !ok || c.agent == "" {
		return
	}
	h.Set(provider.CallerAgentHeader, url.PathEscape(c.agent))
	h.Set(provider.CallerNameHeader, url.PathEscape(usage.AgentName(c.agent)))
	chain := c.chain
	if len(chain) == 0 && c.via != "" {
		chain = []string{c.via}
	}
	if len(chain) > 0 {
		via := make([]string, len(chain))
		for i, v := range chain {
			via[i] = url.PathEscape(v)
		}
		h.Set(provider.CallerViaHeader, strings.Join(via, ","))
	}
}
