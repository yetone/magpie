package provider

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

// A Remote magpie's MCP router (treeman_26897 on Discord): the other
// magpie serves the MCP servers in its Library at its hub
// (gateway.HubMCPAll), and the Library here gives that address a server
// of its own (library/bridge_mcp.go), the user not adding the same
// magpie a second time. Its info (GET /v1) says "mcp_hub" when it has a
// server there to give, which a magpie from before this doesn't; the
// list of its models, fetched, asks it once the library would give it a
// server.

// RemoteHubAsk, set by the library, is whether it would give the router of
// the Remote magpie id a server; RemoteHubFound is called once that magpie
// says it serves one. Nil, nothing is asked.
var (
	RemoteHubAsk   func(id string) bool
	RemoteHubFound func(id string)
)

// RemoteHubWait is how long a Remote magpie's info is waited for.
var RemoteHubWait = 5 * time.Second

// RemoteHub is a Remote magpie as its router's server is made: its
// address (the gateway's root) and its key, and the ids it had before it
// was renamed (Provider.Was), which its server follows.
type RemoteHub struct {
	ID, Name, Root, Key string
	Was                 []string
}

// RemoteHubs are the Remote magpies in providers.json, turned off or not,
// but one found to be this magpie, and one without an address or a key.
// An error is providers.json unreadable: which there are is unknown.
func RemoteHubs() ([]RemoteHub, error) {
	f, err := read()
	if err != nil {
		return nil, err
	}
	own, _ := ownOf(f)
	var out []RemoteHub
	for _, p := range own {
		if !p.IsRemoteMagpie() || p.SelfAnswered() || p.Key == "" || p.Anthropic == "" {
			continue
		}
		out = append(out, RemoteHub{ID: p.ID, Name: p.Name, Root: strings.TrimRight(p.Anthropic, "/"), Key: p.Key, Was: slices.Clone(p.Was)})
	}
	return out, nil
}

// noteRemoteHub asks the Remote magpie p, its models just listed, whether
// it serves its router, once the library would give it a server.
func noteRemoteHub(ctx context.Context, p Provider) {
	ask, found := RemoteHubAsk, RemoteHubFound
	if !p.IsRemoteMagpie() || p.SelfAnswered() || p.Key == "" || p.Anthropic == "" || ask == nil || found == nil || !ask(p.ID) {
		return
	}
	b, err := remoteBody(ctx, p, "GET", "/v1", RemoteHubWait)
	if err != nil {
		return // unknown: asked again at the next list
	}
	var info struct {
		Name   string `json:"name"`
		MCPHub bool   `json:"mcp_hub"`
	}
	if json.Unmarshal(b, &info) == nil && info.Name == "magpie" && info.MCPHub {
		found(p.ID)
	}
}

// AskRemoteHub asks the Remote magpie id at once whether it serves its
// router, as its next model list would: its switch just turned on.
func AskRemoteHub(ctx context.Context, id string) {
	if p, err := Find(id); err == nil && !p.Off {
		noteRemoteHub(ctx, *p)
	}
}
