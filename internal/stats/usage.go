package stats

import (
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
)

// Usage is what the day's event says magpie is used with, when the user
// shares it: built-in ids only. A provider of the user's own is "custom",
// and so is a model on one; a plugin no one knows is "plugin".
type Usage struct {
	Agents    []string // the agents connected to magpie: claude, codex
	Providers []string // the providers on: deepseek, codex (a subscription), plugin:kiro
	Models    []string // the models the connected agents are set to: deepseek/deepseek-v4, custom, group
	Groups    int      // routing groups shown
}

// most of each list sent: far more than anyone has
const most = 50

// usage reads Usage; tests put their own.
var usage = readUsage

func readUsage() Usage {
	defer provider.Hold()()
	var u Usage
	for _, a := range agent.All() {
		if !a.Wired() {
			continue
		}
		id, _, _ := strings.Cut(a.ID, "@wsl:")
		u.Agents = with(u.Agents, id)
		for _, ref := range a.Models() {
			u.Models = with(u.Models, modelLabel(ref))
		}
	}
	for _, p := range provider.All() {
		if p.On() && !p.Hidden {
			u.Providers = with(u.Providers, providerLabel(p))
		}
	}
	for _, g := range provider.Groups() {
		if !g.Hidden && !g.Disabled {
			u.Groups++
		}
	}
	return u
}

func with(xs []string, x string) []string {
	if x == "" || slices.Contains(xs, x) || len(xs) >= most {
		return xs
	}
	return append(xs, x)
}

// providerLabel is p as stats name it: nothing the user typed.
func providerLabel(p provider.Provider) string {
	switch {
	case p.IsPlugin():
		if id := p.PluginProvider(); provider.Movable(id) || provider.Preset(id) != nil {
			return "plugin:" + id
		}
		return "plugin"
	case p.Account != nil:
		return p.Account.Agent
	case p.Preset != "":
		if d := provider.Preset(p.Preset); d != nil {
			return d.ID
		}
	}
	return "custom"
}

// modelLabel is an agent's model as stats name it: the provider's label
// and the vendor's model id, "group" for a routing group, and the label
// alone for a provider of the user's own; "" for one magpie can't find.
func modelLabel(ref string) string {
	if strings.HasPrefix(ref, provider.GroupPrefix) {
		return "group"
	}
	p, m, ok := provider.Resolve(ref)
	if !ok {
		return ""
	}
	l := providerLabel(p)
	if l == "custom" || l == "plugin" {
		return l
	}
	return l + "/" + m
}
