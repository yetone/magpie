// Package gateway is the Gateway page: the gateway's state, its keys, how
// to connect a client, the models it serves and its recent calls. The
// gateway itself is core and serves agents with the page off; while a
// gateway key is there, the page that lists and changes them stays on.
//
// It is the first part of the window a plugin draws itself (P1 of
// docs/subsystems/plugin-runtime.md): its scripts register the page with
// the window's kernel, and its one setting's form is drawn from Settings.
package gateway

import (
	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/pluginrt"
)

// Settings are the page's settings, at their defaults.
type Settings struct {
	CallsPage int `json:"callsPage" title:"Recent calls at a time" description:"How many of the gateway's recent calls the page shows, and how many more each Show more adds" enum:"10,20,50,100"`
}

func init() {
	pluginrt.Register(pluginrt.Manifest{
		ID: "gateway", API: "^1", Requires: []string{"window", "gateway"}, Essential: true,
		Name: "Gateway page", Sub: "The gateway's state and keys, how to connect a client, its models and recent calls",
		One: "1 gateway key", Many: "{n} gateway keys",
		InUse: func() (int, error) {
			keys, err := access.List()
			return len(keys), err
		},
		SetAt:    "network #portList",
		Parts:    []pluginrt.Part{{Slot: "nav.page", Key: "gateway"}},
		Settings: Settings{CallsPage: 20},
		Client:   []string{"keys.js", "client.js"},
	})
}
