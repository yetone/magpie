package provider

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/settings"
)

// An agent's pick of a model may be sent in its vendor's fast mode (#954),
// as a routing group's member may (Group.Fast): switched by the model in
// the agent's picker, kept by agent (settings' FastPicks), and asked for by
// the gateway on every request the agent sends that model. A group takes
// none: its members are sent as it says.
//
// A fast pick of a ChatGPT model may be sent as Ultrafast instead (#1954,
// settings' UltraPicks): the gateway asks it of each account that offers it
// on the model, and priority of one that doesn't.

// IsFastPick reports whether agent's requests for the entry id
// ("<provider>/<model>") are sent fast.
func IsFastPick(agent, id string) bool {
	return slices.Contains(FastPicks(agent), id)
}

// FastPicks are the entries agent's requests for are sent fast, read once
// for a list of them all.
func FastPicks(agent string) []string {
	return heldSettings().FastPicks[strings.ToLower(agent)]
}

// IsUltraPick reports whether agent's requests for the entry id are sent as
// Ultrafast where the account offers it.
func IsUltraPick(agent, id string) bool {
	return slices.Contains(UltraPicks(agent), id)
}

// UltraPicks are the fast picks agent's requests for are sent as Ultrafast.
func UltraPicks(agent string) []string {
	return heldSettings().UltraPicks[strings.ToLower(agent)]
}

// CanUltra reports whether a fast pick of model on p can be sent as
// Ultrafast: a ChatGPT model that one of Codex's accounts lists the tier
// on (Account.Tiers), or a plugin's model, with fast mode, that the plugin
// says its vendor serves at ultrafast (serviceTiers, #2514), or another
// magpie's model its list offers ultrafast on (#1234), or a model OpenAI's
// API serves at ultrafast on that API (OpenAIUltra), or a GPT model on a
// provider the user added by its address (OwnFast) that OpenAI offers
// Ultrafast on: its API serves it so (OpenAIUltra), one of Codex's accounts
// lists the tier on the model, or Codex's own entry for it does, which
// Codex is offered on that provider as well (codexcat.OwnOffers; KoiLx0 on
// X, a relay that serves it). Like
// its Fast, that is asked for, and a provider that turns the field away is
// asked again without it. A list that doesn't say tiers offers none.
func CanUltra(p Provider, model string) bool {
	return UltraOn(p, model, UltraModels())
}

// UltraOn is CanUltra with UltraModels read once, for a list of them all.
func UltraOn(p Provider, model string, ultra map[string]bool) bool {
	if p.IsPlugin() {
		pp, ok := PluginOf(p.ID)
		if !ok {
			return false
		}
		m, listed := pluginModel(pp, model)
		return listed && CanFast(p, model) && pluginTierOn(pp.ID, m, "ultrafast")
	}
	if p.IsRemoteMagpie() {
		// another magpie's model it offers Ultrafast on, beside Fast
		return CanFast(p, model) && slices.Contains(RemoteTiers(p, model), "ultrafast")
	}
	if OwnFast(p, model) {
		slug := modelSlug(model)
		return ultra[slug] || codexcat.OwnOffers(slug, "ultrafast") || OpenAIUltra(slug)
	}
	if p.Account == nil {
		// OpenAI's own API, on its Responses, for the models its Ultrafast
		// mode names (OpenAIUltra); a magpie in Docker has no Codex CLI
		// whose list could say so (OoopsHh on X)
		return HostOf(p.Responses) == "api.openai.com" && CanFast(p, model) && OpenAIUltra(model)
	}
	return p.Account.Agent == "codex" && CanFast(p, model) && ultra[model]
}

// UltraModels are the models one of Codex's accounts offers Ultrafast on.
func UltraModels() map[string]bool {
	out := map[string]bool{}
	for _, l := range Logins("codex") {
		live, _, found := catalog.Live(accountModels("codex", l.User))
		if !found {
			continue
		}
		for _, m := range live {
			if slices.Contains(m.Tiers, "ultrafast") {
				out[m.ID] = true
			}
		}
	}
	return out
}

// SetFastPick sends agent's requests for the entry id fast, or not. Only a
// model with a fast mode magpie can ask for (CanFast) is switched on.
func SetFastPick(agent, id string, fast bool) error {
	tier := ""
	if fast {
		tier = "priority"
	}
	return SetFastTier(agent, id, tier)
}

// SetFastTier sends agent's requests for the entry id at tier: "" the usual
// speed, "priority" fast, "ultrafast" Ultrafast where the account offers it
// (CanUltra), priority where it doesn't.
func SetFastTier(agent, id, tier string) error {
	agent, id = strings.ToLower(strings.TrimSpace(agent)), strings.TrimSpace(id)
	if agent == "" {
		return errors.New("no agent")
	}
	switch tier {
	case "", "priority", "ultrafast":
	default:
		return fmt.Errorf("magpie knows no tier %q", tier)
	}
	if tier != "" {
		if IsGroupRef(id) {
			return fmt.Errorf("%s is a routing group: its members are sent fast as it says, on the Routing page", id)
		}
		p, model, ok := Resolve(id)
		if !ok {
			return fmt.Errorf("magpie knows no model %q", id)
		}
		if p.ID+"/"+model != id {
			return fmt.Errorf("%s is not a model as <provider>/<model>", id)
		}
		if !CanFast(p, model) {
			return fmt.Errorf("%s has no fast mode magpie can ask for", id)
		}
		if tier == "ultrafast" && !CanUltra(p, model) {
			return fmt.Errorf("no account offers Ultrafast on %s", id)
		}
	}
	s := settings.Load()
	fast := setPick(&s.FastPicks, agent, id, tier != "")
	ultra := setPick(&s.UltraPicks, agent, id, tier == "ultrafast")
	if !fast && !ultra {
		return nil
	}
	if err := settings.Save(s); err != nil {
		return err
	}
	Changed() // a held settings read sees it
	return nil
}

// setPick puts id in agent's list in m, or takes it out, and reports
// whether that changed the list.
func setPick(m *map[string][]string, agent, id string, on bool) bool {
	was := (*m)[agent]
	keep := slices.DeleteFunc(slices.Clone(was), func(x string) bool { return x == id })
	if on {
		keep = append(keep, id)
	}
	if slices.Equal(was, keep) {
		return false
	}
	if len(keep) == 0 {
		delete(*m, agent)
	} else {
		if *m == nil {
			*m = map[string][]string{}
		}
		(*m)[agent] = keep
	}
	return true
}
