// Package budget holds each gateway key to the limits its owner set
// (#585): the tokens or estimated cost it may use in a window. While a key
// has a limit, it stays on.
//
// It is the limiter of the gateway's budget point (P3 of
// docs/subsystems/plugin-runtime.md): Apply serves internal/budget's, and
// the point fails closed: a request of a key held to a limit is not sent
// while no limiter serves it. What a key has used is the core's, read
// from the usage records, so the Settings page, `magpie keys` and
// /v1/magpie/limit tell it either way.
package budget

import (
	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
	"github.com/yetone/magpie/internal/gwbudget"
	"github.com/yetone/magpie/internal/pluginrt"
)

func init() {
	pluginrt.Register(pluginrt.Manifest{
		ID: "budget", API: "^1", Requires: []string{"settings", "gateway"}, Essential: true,
		Name: "Key limits", Sub: "Hold each gateway key to the tokens or estimated cost you set for it",
		Opens: "gateway",
		One:   "1 key with a limit", Many: "{n} keys with a limit",
		InUse: InUse,
		Apply: func(c *pluginrt.Ctx) error {
			c.OnDispose(gwbudget.Limit(budget.Limiter{}))
			return nil
		},
	})
}

// InUse counts the gateway keys held to a limit; caller-keys.json that
// can't be read is not known, so it stays on.
func InUse() (int, error) {
	keys, err := access.List()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, k := range keys {
		if len(k.Limits()) > 0 {
			n++
		}
	}
	return n, nil
}
