package agent

import (
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// Every agent's model picker has two halves: the models the agent reaches
// on its own (its sign-in, its own keys), and magpie's catalog — every model
// of every provider the user added, reached through the local gateway.
// A catalog model is spelled "provider/model"; an agent that sends that to
// the gateway gets the vendor's reply in whichever API the agent speaks.

// magpieID is the provider id agents know the gateway by.
const magpieID = "magpie"

// RoutingGroups is the picker's group of routing groups.
const RoutingGroups = "Routing groups"

// viaMagpie lists the catalog as agent is shown it, for a picker: the
// routing groups first, in one group of their own, then one group per
// provider.
func viaMagpie(agent, prefix string) []Option {
	var out, groups []Option
	shown, _ := provider.CatalogFor(agent)
	for _, e := range shown {
		if e.Group != "" {
			groups = append(groups, Option{Value: prefix + e.ID, Label: e.Name, Note: "routing group · via magpie",
				Icon: e.Provider.Icon, Icons: e.Icons, Group: RoutingGroups, Ref: e.ID})
			continue
		}
		note := e.Provider.Name + " · via magpie"
		if a := e.Provider.Account; a != nil {
			note = a.User + " · via magpie"
		}
		out = append(out, Option{Value: prefix + e.ID, Label: e.Name, Note: note,
			Icon: e.Provider.Icon, Group: e.Provider.Name, Ref: e.ID, Free: e.Free, Context: e.Context})
	}
	return append(groups, out...)
}

// viaMagpieFor is viaMagpie without the agent's own account: Codex CLI going
// through magpie to its own ChatGPT login would only add a hop.
func viaMagpieFor(agentID, prefix string) []Option {
	var out []Option
	for _, o := range viaMagpie(agentID, prefix) {
		if !strings.HasPrefix(o.Value, prefix+agentID+"/") {
			out = append(out, o)
		}
	}
	return out
}

// isMagpie reports whether a model value is a catalog reference.
func isMagpie(v string) bool {
	_, _, ok := provider.Resolve(v)
	return ok && strings.Contains(v, "/")
}

func firstOf(xs []string) string {
	if len(xs) > 0 {
		return xs[0]
	}
	return ""
}

// magpieModels is the catalog as agent is shown it, as catalog.Models, for
// agents that keep their own model files.
func magpieModels(agent string) []catalog.Model {
	var out []catalog.Model
	shown, _ := provider.CatalogFor(agent)
	for _, e := range shown {
		m := catalog.Model{ID: e.ID, Name: e.Label(), Provider: firstOf(e.Provider.Catalogs()), Efforts: e.Efforts, Images: e.Images, Context: e.Context, Output: e.Output}
		// APIs is the one to ask it on for the gateway to relay the request
		// as it is; none for a group, whose members may each want another
		if e.Group == "" {
			if n := e.Provider.Native(e.Model); n != "" {
				m.APIs = []string{string(n)}
			}
		}
		out = append(out, m)
	}
	return out
}

// group tags every option with a group name.
func group(name string, opts []Option) []Option {
	for i := range opts {
		opts[i].Group = name
	}
	return opts
}

func gatewayV1() string { return gateway.URL() + "/v1" }
