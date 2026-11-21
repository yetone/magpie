package gateway

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/gwmw"
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
	c := pluginCaller(ctx)
	if c == nil {
		return
	}
	h.Set(provider.CallerAgentHeader, url.PathEscape(c.Agent))
	h.Set(provider.CallerNameHeader, url.PathEscape(c.Name))
	if len(c.Via) > 0 {
		via := make([]string, len(c.Via))
		for i, v := range c.Via {
			via[i] = url.PathEscape(v)
		}
		h.Set(provider.CallerViaHeader, strings.Join(via, ","))
	}
}

// pluginCaller is the agent a request came from as a plugin is told it: the
// one value behind a fetch's x-magpie-caller-* headers (chat.headers'
// input.caller), the JS middleware's ctx.caller and the async request
// hooks' ctx.caller (@febsincos on X: refuse an agent before a provider is
// picked). nil when the gateway knows no agent.
func pluginCaller(ctx context.Context) *gwmw.Caller {
	c, ok := ctx.Value(callerCtx{}).(caller)
	if !ok || c.agent == "" {
		return nil
	}
	via := slices.Clone(c.chain)
	if len(via) == 0 && c.via != "" {
		via = []string{c.via}
	}
	if via == nil {
		via = []string{}
	}
	return &gwmw.Caller{Agent: c.agent, Name: usage.AgentName(c.agent), Via: via}
}
