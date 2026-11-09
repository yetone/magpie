// Package middleware is the JS gateway plugins: the plugins with a gateway
// module in plugins.json, run in the gateway's own process
// (docs/subsystems/gateway-middleware.md). While one is on in plugins.json,
// it stays on.
//
// It serves them at three of the gateway's points (P3 and P4 of
// docs/subsystems/plugin-runtime.md): Apply registers internal/middleware's
// chain as the agent middleware of the middleware point, and its Points as
// an observer that registers each module's strategies on the router
// strategy point and gives its onUsage the ledger's rows. Off, agents'
// requests go through no middleware, a pool routed by a module's strategy
// is tried in its listed order, and no onUsage is called.
package middleware

import (
	"github.com/yetone/magpie/internal/gwmw"
	"github.com/yetone/magpie/internal/middleware"
	"github.com/yetone/magpie/internal/pluginrt"
	"github.com/yetone/magpie/internal/usage"
)

func init() {
	pluginrt.Register(pluginrt.Manifest{
		ID: "middleware", API: "^1", Requires: []string{"gateway"}, Essential: true,
		Name: "JS gateway plugins", Sub: "Run the JavaScript gateway plugins: middleware on agents' requests and replies, routings and usage observers",
		Opens: "plugins",
		One:   "1 gateway plugin on", Many: "{n} gateway plugins on",
		InUse: func() (int, error) { return middleware.On(), nil },
		Apply: func(c *pluginrt.Ctx) error {
			c.OnDispose(gwmw.Use(middleware.Chain{}))
			c.OnDispose(usage.Observe(middleware.Points()))
			return nil
		},
	})
}
