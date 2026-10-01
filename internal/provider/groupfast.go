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
// Responses API (priority processing), and an Anthropic key's Opus models with fast mode.
// A Claude subscription runs Claude Code itself, and a relay or a cloud
// (Bedrock, Vertex) may refuse the field, so neither is.
func CanFast(p Provider, model string) bool {
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
	return HostOf(p.Anthropic) == "api.anthropic.com" && ClaudeFast(model)
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
		if strings.HasPrefix(model, GroupPrefix) {
			return fmt.Errorf("%s is a group: its models are sent as it says, so it takes no :%s", model, FastWord)
		}
		if p, m, ok := resolveIn(entries, model); ok && !CanFast(p, m) {
			return fmt.Errorf("%s has no fast mode magpie can ask for (ChatGPT and OpenAI GPT models, Cursor's with a fast one, and Claude Opus 4.8 and 5 on an Anthropic key have)", model)
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
// its rules, its pick and its fast mode follow it.
func (g *Group) RenameMember(from, to string) {
	for i, m := range g.Members {
		if m == from {
			g.Members[i] = to
		}
	}
	for i, r := range g.Rules {
		if r.Use == from {
			g.Rules[i].Use = to
		}
	}
	if g.Pick == from {
		g.Pick = to
	}
	for i, f := range g.Fast {
		if f == from {
			g.Fast[i] = to
		}
	}
}
