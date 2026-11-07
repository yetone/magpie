package provider

// A group's member may be sent in its vendor's fast mode (Discord: "gpt-6.1
// sol · high · fast"): OpenAI's priority processing (service_tier
// "priority", Codex's Fast) on a ChatGPT account or an OpenAI key, Cursor's
// -fast model, Claude's fast mode (speed "fast") on an Anthropic key for
// the Opus models that have it. A member whose model has none is sent as
// it is.
//
// Group.Fast lists those members as Members spells them, kept apart from
// the member's id so that a version before it, which reads no "fast",
// still routes to the member, only not fast. Typed, it is a last part
// ":fast" after the effort, if any: "codex/gpt-6.1-sol:high:fast".

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// FastWord is the last part of a member typed to be sent fast.
const FastWord = "fast"

// MemberFast splits a member typed with ":fast" after it into the member
// and true: "a/m:high:fast" is a/m:high, fast, unless a has a model
// "m:high:fast" of its own.
func MemberFast(id string) (member string, fast bool) {
	return memberFastIn(nil, id)
}

func memberFastIn(entries []Entry, id string) (string, bool) {
	i := strings.LastIndex(id, ":")
	if i <= 0 || !strings.EqualFold(id[i+1:], FastWord) {
		return id, false
	}
	if !strings.HasPrefix(id, GroupPrefix) && isModel(entries, id) {
		return id, false
	}
	return id[:i], true
}

// IsFast reports whether the group sends its member id fast.
func (g Group) IsFast(id string) bool {
	return slices.Contains(g.Fast, id)
}

// CanFast reports whether model, as p serves it, has a fast mode magpie
// can ask for: a ChatGPT account's GPT models (Codex's Fast), Cursor's
// models with a -fast one, an OpenAI key's GPT and o-series models on its
// Responses API (priority processing), an Anthropic key's Opus models with
// fast mode, and a plugin's models it says are served at priority.
// A Claude subscription runs Claude Code itself, and a cloud (Bedrock,
// Vertex) refuses the field, so neither is. A provider the user added by
// its address (AddedByAddress) is asked for priority on a GPT model it
// serves on an OpenAI API, when the user switches it fast themselves
// (#1615): it is sent service_tier "priority" there, as Codex's own Fast
// already is (#1234), and one that turns the field away is asked again
// without it and not sent it again (the gateway's optional fields).
func CanFast(p Provider, model string) bool {
	if p.IsPlugin() {
		pp, ok := PluginOf(p.ID)
		if !ok {
			return false
		}
		// a model the plugin says its vendor serves at the priority tier
		// (serviceTiers, #2514), on its Chat or Responses API, which the
		// gateway sends service_tier "priority" there (Lovest on Discord:
		// only Cursor's plugin could be picked fast)
		m, listed := pluginModel(pp, model)
		if listed && pluginTierOn(pp.ID, m, "priority") {
			return true
		}
		// Cursor's plugin runs a model as its fast variant when the chat
		// request says service_tier "priority". From 0.2.2 it lists a model
		// once per context size (claude-opus-5-5@300k) and says fast on
		// the ones Cursor has a fast variant of (#1360); 0.1.x listed the
		// fast one as a model of its own, -fast, as the built-in does
		if p.PluginProvider() != "cursor" {
			return false
		}
		if listed && m.Fast {
			return true
		}
		if strings.HasSuffix(model, "-fast") {
			return false
		}
		_, ok = pluginModel(pp, model+"-fast")
		return ok
	}
	if p.IsRemoteMagpie() {
		// another magpie's model it offers its own Codex Fast on (its
		// list's service_tiers, #1234): a pick sent fast here goes on to
		// it with service_tier "priority", which it sends its vendor as
		// its own Codex's turn would be (littlekk88 on Discord: the
		// picker had no Fast for a remote magpie's model)
		return slices.Contains(RemoteTiers(p, model), "priority")
	}
	if p.Account != nil {
		switch p.Account.Agent {
		case "codex":
			return strings.HasPrefix(model, "gpt-")
		case "cursor":
			if strings.HasSuffix(model, "-fast") {
				return false
			}
			_, ok := CursorVariants(model + "-fast")
			return ok
		}
		return false
	}
	if p.IsBedrock() {
		return false
	}
	// on OpenAI's Responses API: a request translated for its Chat API is
	// told no tier (TestCursorPluginFast)
	if HostOf(p.Responses) == "api.openai.com" && openAIFast.MatchString(model) {
		return true
	}
	if HostOf(p.Anthropic) == "api.anthropic.com" && ClaudeFast(model) {
		return true
	}
	return OwnFast(p, model)
}

// RemoteTiers are the service tiers another magpie's list says it offers
// on model (#1234), nil for a provider that isn't one.
func RemoteTiers(p Provider, model string) []string {
	if !p.IsRemoteMagpie() {
		return nil
	}
	for _, m := range p.liveByID(model) {
		if len(m.Tiers) > 0 {
			return m.Tiers
		}
	}
	return nil
}

// OwnFast reports whether model has a fast mode only as the user's own
// provider may serve it (#1615): a GPT model on a provider they added by
// its address, on an OpenAI API, which magpie can't tell serves priority
// and asks for it all the same when the user switches it fast. Not where
// CanFast knows the vendor's (OpenAI's own API).
func OwnFast(p Provider, model string) bool {
	if !p.AddedByAddress() || p.IsBedrock() || HostOf(p.Responses) == "api.openai.com" {
		return false
	}
	return (p.Responses != "" || p.Chat != "") && openAIFast.MatchString(modelSlug(model))
}

// AddedByAddress reports whether the user added the provider by its
// address: no preset, no account, no plugin. The tier an agent asks for
// goes to it as it was asked (the gateway's OwnTier).
func (p Provider) AddedByAddress() bool {
	return p.ID != "" && p.Preset == "" && p.Account == nil && !p.IsPlugin()
}

// modelSlug is a model's id as its own vendor names it, as a provider the
// user added may serve it: openai/GPT-6-sol is gpt-6-sol.
func modelSlug(model string) string {
	model = strings.ToLower(model)
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	return model
}

var (
	openAIFast = regexp.MustCompile(`^(gpt-|o\d)`)
	// a dated id is its model's: claude-opus-4-8-20260501
	claudeDated = regexp.MustCompile(`-\d{8}$`)
)

// claudeFastModels are the Claude models with fast mode (speed "fast",
// beta fast-mode-2026-02-01).
var claudeFastModels = []string{"claude-opus-4-8", "claude-opus-5", "claude-opus-5-5"}

// ClaudeFast reports whether a Claude model has fast mode.
func ClaudeFast(model string) bool {
	return slices.Contains(claudeFastModels, claudeDated.ReplaceAllString(strings.ToLower(model), ""))
}

// cleanFast takes a member typed with ":fast" as one the group sends fast,
// and keeps g.Fast to the group's own models as Members spells them, each
// once. A group in the group is sent as its own members say, so it takes
// none; a model with no fast mode is refused, as long as it is served.
func cleanFast(entries []Entry, g *Group) error {
	for i, m := range g.Members {
		if id, fast := memberFastIn(entries, strings.TrimSpace(m)); fast {
			g.Members[i] = id
			g.Fast = append(g.Fast, id)
		}
	}
	key := func(id string) string { return cleanMember(entries, strings.TrimSpace(id)) }
	var members []string
	for _, m := range g.Members {
		members = append(members, key(m))
	}
	var out []string
	for _, f := range g.Fast {
		f = key(f)
		if !slices.Contains(members, f) || slices.Contains(out, f) {
			continue // taken out of the group
		}
		model, _ := memberEffortIn(entries, f)
		if IsGroupRef(model) {
			return fmt.Errorf("%s is a group: its models are sent as it says, so it takes no :%s", model, FastWord)
		}
		if p, m, ok := resolveIn(entries, model); ok && !CanFast(p, m) {
			return fmt.Errorf("%s has no fast mode magpie can ask for (ChatGPT and OpenAI GPT models, a GPT model on a provider you added by its address, Cursor's with a fast one, a plugin's it says are served at priority, another magpie's it offers Fast on, and Claude Opus 4.8 and 5 on an Anthropic key have)", model)
		}
		out = append(out, f)
	}
	g.Fast = out
	return nil
}

// SetMemberFast sends the group's member id fast, or not, where it is in
// the group.
func (g *Group) SetMemberFast(id string, fast bool) {
	g.Fast = slices.DeleteFunc(slices.Clone(g.Fast), func(x string) bool { return x == id })
	if fast && slices.Contains(g.Members, id) {
		g.Fast = append(g.Fast, id)
	}
}

// RenameMember has the group's member from go by to (its effort changed):
// its rules, its pick, its fast mode, its being off and its weight
// follow it.
func (g *Group) RenameMember(from, to string) {
	for i, m := range g.Members {
		if m == from {
			g.Members[i] = to
		}
	}
	for i := range g.Rules {
		g.Rules[i].MapUses(func(u string) string {
			if u == from {
				return to
			}
			return u
		})
	}
	if g.Pick == from {
		g.Pick = to
	}
	for i, f := range g.Fast {
		if f == from {
			g.Fast[i] = to
		}
	}
	for i, p := range g.Placed {
		if p == from {
			g.Placed[i] = to
		}
	}
	for i, o := range g.Off {
		if o == from {
			g.Off[i] = to
		}
	}
	g.Weights = renamedWeights(g.Weights, func(m string) string {
		if m == from {
			return to
		}
		return m
	})
	g.EffortMaps = renamedEffortMaps(g.EffortMaps, func(m string) string {
		if m == from {
			return to
		}
		return m
	})
}
