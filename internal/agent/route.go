package agent

import (
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
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
	// own: whether a provider is the account the agent is signed in to,
	// asked once a provider (OwnPaused reads the saved logins); a plugin's
	// for the agent's own vendor (Grok moved onto its plugin) is when it
	// is the account the agent itself is signed in to
	own := map[string]bool{}
	for _, e := range shown {
		a := e.Provider.Account
		if a == nil || a.StandIn() {
			continue
		}
		if _, ok := own[e.Provider.ID]; ok {
			continue
		}
		if a.Agent == agent {
			own[e.Provider.ID] = !e.Provider.OwnPaused()
		} else if e.Provider.PluginProvider() == agent && a.User != "" {
			own[e.Provider.ID] = a.User == provider.AgentUser(agent)
		}
	}
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
			Icon: e.Provider.Icon, Group: e.Provider.Name, Ref: e.ID, Free: e.Free, Rate: e.Rate, RateWas: e.RateWas, Context: e.Context, own: own[e.Provider.ID],
			sub: e.Provider.Account != nil && (e.Provider.Account.Agent != "claude" || agent == "claude")})
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
	if pid, _, ok := strings.Cut(v, "/"); ok && dryProviders[pid] {
		return true
	}
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
	labels := provider.Labels(shown)
	// a model magpie describes images to takes them (provider.Described)
	seen := provider.Described != nil && provider.Described()
	st, find := settings.Load(), provider.GroupFinder()
	for i, e := range shown {
		m := catalog.Model{ID: e.ID, Name: labels[i], Provider: firstOf(e.Provider.Catalogs()), Efforts: e.Efforts, Images: e.Images || seen, ImageInput: e.ImageInput, Context: e.Context, Output: e.Output, AgentsV2: e.AgentsV2, Reasoning: e.Reasoning}
		// what a call costs the user (Pi's cost, #781)
		if pr, ok := entryPrice(st, find, e); ok {
			m.Price = &pr
		}
		if seen && !e.Images {
			yes := true
			m.ImageInput = &yes
		}
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

// entryPrice is what a call to e costs the user, as the usage pages count
// it: a group's when every member costs the same, none in a fast mode,
// since which of them answers isn't known beforehand.
func entryPrice(st settings.Settings, find func(string) (provider.Group, []provider.Member, bool), e provider.Entry) (catalog.Price, bool) {
	if e.Group == "" {
		return provider.EffectivePriceIn(st, e.Provider.ID, e.Model)
	}
	_, ms, ok := find(e.ID)
	if !ok || len(ms) == 0 {
		return catalog.Price{}, false
	}
	var first catalog.Price
	for i, m := range ms {
		pr, ok := provider.EffectivePriceIn(st, m.Provider.ID, m.Model)
		if !ok || m.Fast || i > 0 && pr != first {
			return catalog.Price{}, false
		}
		first = pr
	}
	return first, true
}

// maxTokens is the output limit an agent is handed for m, kept within the
// context window it is handed with it: models.dev lists some models' output
// above their window (deepseek-chat's 384000 against 128000). An unknown
// window leaves the output as it is.
func maxTokens(m catalog.Model) int {
	if m.Context > 0 && m.Output > m.Context {
		return m.Context
	}
	return m.Output
}

// group tags every option with a group name.
func group(name string, opts []Option) []Option {
	for i := range opts {
		opts[i].Group = name
	}
	return opts
}

func gatewayV1() string { return gateway.URL() + "/v1" }
