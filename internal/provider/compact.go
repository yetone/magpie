package provider

import (
	"maps"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// Where Codex and Claude Code compact a long conversation (#876): the
// window they are told for a model whose own is longer. A model's own
// (settings.ModelCompacts, "provider/model"), else its provider's
// ("provider/*"), else the one for every model (settings.Compact: 272K, a
// number the user gave, or none under Full window).

// compactOf is the user's threshold for a provider's model: its own, else
// its provider's "*"; 0 when neither is set.
func compactOf(s settings.Settings, providerID, model string) int {
	if n := s.ModelCompacts[providerID+"/"+model]; n > 0 {
		return n
	}
	return s.ModelCompacts[providerID+"/*"]
}

// CompactSet is the threshold the user set on a model, by its catalog id
// ("provider/model", magpie/ and a [1m] mark taken off), or on its
// provider; for a group, the smallest its models have, so none of them
// outgrows its own. 0 when none is set: settings.Compact then answers.
func CompactSet(id string) int {
	return compactSet(settings.Load(), id, GroupFinder())
}

// CompactSetIn is CompactSet with the settings and group finder its caller
// has already read (CompactFor's).
func CompactSetIn(s settings.Settings, id string, find func(string) (Group, []Member, bool)) int {
	return compactSet(s, id, find)
}

// CompactFor is where Codex compacts a conversation on e (catalog.Model's
// Compact): the threshold the user set on it or its provider (CompactSet),
// else, when its window is one the user typed (Entry.WindowSet) and no
// size of theirs is typed in Settings, that whole window (#1809: a group
// set to 500K was told Codex as magpie's default 272K, which Codex runs
// to 95% of). magpie's default gives way to a window the user typed as
// it does to one they set in Claude Code (#876); a threshold they typed
// still comes first. 0 leaves it to settings.Compact. It takes the
// settings and the group finder from its caller: an agent's list is built
// for hundreds of models at magpie's start, and reading both again for
// each is what EffectivePriceIn saves the same loop.
func CompactFor(s settings.Settings, e Entry, find func(string) (Group, []Member, bool)) int {
	if n := compactSet(s, e.ID, find); n > 0 {
		return n
	}
	if e.WindowSet && s.CompactAt == 0 {
		return e.Context
	}
	return 0
}

// WindowTyped says the window agents are told for a model, by its
// catalog id (as CompactSet takes it), is one the user typed: the
// group's own size, a group's smallest member's that the user typed
// (smallestWindow, #2224), its largest member's when it names no size
// and the user typed that one (largestWindow), or one set on the provider for the model or
// all of its models. Claude Code then isn't handed magpie's default
// compaction window for it (CompactFor's rule, as Entry.WindowSet).
func WindowTyped(id string) bool {
	id = catalogID(id)
	if strings.HasPrefix(id, GroupPrefix) {
		gid, ok := GroupIDOf(id)
		if !ok {
			return false
		}
		entries := providerEntries()
		all := groupsIn(entries)
		g, ok := groupOf(all, gid)
		switch {
		case !ok || g.Disabled:
			return false
		case g.Context > 0:
			return true
		case g.Context == ContextSmallest:
			_, typed := smallestWindow(entries, membersIn(entries, all, g))
			return typed
		case g.Context == 0:
			return largestWindow(entries, g)
		}
		return false
	}
	pid, model, ok := strings.Cut(id, "/")
	if !ok {
		return false
	}
	p, err := Find(pid)
	return err == nil && p.ContextOf(model) > 0
}

// ClaudeGroup says a model id (as CompactSet takes it) is a routing
// group whose every member is one of Anthropic's Claude models, by the
// name it is listed under or the one the vendor is asked for. Claude Code
// runs such a group as it runs a Claude model, to its own window, so it
// isn't handed magpie's default compaction window (#1741: a group of
// Claude [1m] models was told 272000, its id's last part not starting
// with "claude"). A group with any other member, or none, isn't.
func ClaudeGroup(id string) bool {
	id = catalogID(id)
	if !strings.HasPrefix(id, GroupPrefix) {
		return false
	}
	_, ms, ok := GroupFinder()(id)
	if !ok || len(ms) == 0 {
		return false
	}
	for _, m := range ms {
		if !claudeModel(entryModel(m)) && !claudeModel(m.Model) {
			return false
		}
	}
	return true
}

// catalogID is an agent's model id as the catalog has it: magpie/ and
// Claude Code's [1m] mark taken off, a found group's auto/<model> as
// group/auto-<slug> (CanonicalRef).
func catalogID(id string) string {
	return CanonicalRef(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(id), "magpie/"), "[1m]"))
}

func compactSet(s settings.Settings, id string, find func(string) (Group, []Member, bool)) int {
	id = catalogID(id)
	if strings.HasPrefix(id, GroupPrefix) {
		g, ms, _ := find(id)
		// the group's own, before what its members say
		if g.Compact > 0 {
			return g.Compact
		}
		if len(s.ModelCompacts) == 0 {
			return 0
		}
		least := 0
		for _, m := range ms {
			if n := compactOf(s, m.Provider.ID, entryModel(m)); n > 0 && (least == 0 || n < least) {
				least = n
			}
		}
		return least
	}
	if len(s.ModelCompacts) == 0 {
		return 0
	}
	pid, model, ok := strings.Cut(id, "/")
	if !ok {
		return 0
	}
	return compactOf(s, pid, model)
}

// entryModel is the model id a group's member is listed under: the last
// of its path, the provider's id taken off, which is not always what the
// vendor is asked for (a wire name).
func entryModel(m Member) string {
	if n := len(m.Path); n > 0 {
		if model, ok := strings.CutPrefix(m.Path[n-1], m.Provider.ID+"/"); ok {
			return model
		}
	}
	return m.Model
}

// CompactsOf is the thresholds the user set on a provider's models, by
// model id, "*" for all of them: what its editor's Compact at shows.
func CompactsOf(providerID string) map[string]int {
	out := map[string]int{}
	for k, n := range settings.Shared().ModelCompacts {
		if model, ok := strings.CutPrefix(k, providerID+"/"); ok && model != "" && n > 0 {
			out[model] = n
		}
	}
	return out
}

// SetModelCompacts makes a provider's thresholds cs, by model id, "*" for
// all of them, as its editor's Compact at says: those it leaves out are
// taken away, and every one set or changed has to be a model the provider
// serves, as for a reply limit (SetModelOutputs); one sent back as it is
// for a model the provider no longer lists is kept, not refused (#3305).
// Codex's and Claude Code's lists are written again when anything changed.
func SetModelCompacts(providerID string, cs map[string]int) error {
	p, err := Find(providerID)
	if err != nil {
		return err
	}
	s := settings.Load()
	for model, n := range cs {
		if n < 0 {
			return errorf("a compaction threshold is a number of tokens, not %d", n)
		}
		if err := settings.CheckModelKey("a compaction threshold", p.ID+"/"+model); err != nil {
			return err
		}
		if model != "*" && n > 0 && s.ModelCompacts[p.ID+"/"+model] != n && !p.serves(model) {
			return errorf("%s has no model %s (magpie provider %s lists them)", p.ID, model, p.ID)
		}
	}
	changed := false
	for k := range s.ModelCompacts {
		if model, ok := strings.CutPrefix(k, p.ID+"/"); ok && cs[model] <= 0 {
			delete(s.ModelCompacts, k)
			changed = true
		}
	}
	for model, n := range cs {
		if n <= 0 || s.ModelCompacts[p.ID+"/"+model] == n {
			continue
		}
		if s.ModelCompacts == nil {
			s.ModelCompacts = map[string]int{}
		}
		s.ModelCompacts[p.ID+"/"+model] = n
		changed = true
	}
	if !changed {
		return nil
	}
	if err := settings.Save(s); err != nil {
		return err
	}
	catalog.Touched()
	return nil
}

// SetAgentCompact gives an agent of CompactAgents a Long conversations of
// its own, c, over the one for every agent; nil takes it away, and the
// agent follows that one again.
func SetAgentCompact(agent string, c *settings.AgentCompact) error {
	if !slices.Contains(settings.CompactAgents, agent) {
		return errorf("%q writes no compaction window magpie sets", agent)
	}
	if c != nil && c.At < 0 {
		return errorf("a compaction threshold is a number of tokens, not %d", c.At)
	}
	s := settings.Load()
	cur, had := s.AgentCompacts[agent]
	if c == nil {
		if !had {
			return nil
		}
		s.AgentCompacts = maps.Clone(s.AgentCompacts)
		delete(s.AgentCompacts, agent)
	} else {
		n := *c
		if n.At == settings.WorkingWindow {
			n.At = 0
		}
		if had && cur == n {
			return nil
		}
		s.AgentCompacts = maps.Clone(s.AgentCompacts)
		if s.AgentCompacts == nil {
			s.AgentCompacts = map[string]settings.AgentCompact{}
		}
		s.AgentCompacts[agent] = n
	}
	if err := settings.Save(s); err != nil {
		return err
	}
	catalog.Touched()
	return nil
}

// SetCompactAt has every model without a threshold of its own compacted at
// n tokens, Full window turned off; 0 is the working window again
// (settings.WorkingWindow).
func SetCompactAt(n int) error {
	if n < 0 {
		return errorf("a compaction threshold is a number of tokens, not %d", n)
	}
	s := settings.Load()
	if n == settings.WorkingWindow {
		n = 0
	}
	if s.CompactAt == n && !s.FullContext {
		return nil
	}
	s.CompactAt, s.FullContext = n, false
	if err := settings.Save(s); err != nil {
		return err
	}
	catalog.Touched()
	return nil
}

// SetContextCapAll chooses whether the longest context (SetContextCap) also
// holds a window the user set for a model or its provider.
func SetContextCapAll(all bool) error {
	s := settings.Load()
	if s.ContextCapAll == all {
		return nil
	}
	s.ContextCapAll = all
	if err := settings.Save(s); err != nil {
		return err
	}
	catalog.Touched()
	return nil
}

// SetContextCap holds every model's window to n tokens at most
// (settings.ContextCap), where the user set none for the model or its
// provider; 0 takes the cap away. Agents' lists are written again.
func SetContextCap(n int) error {
	if n < 0 {
		return errorf("a context cap is a number of tokens, not %d", n)
	}
	s := settings.Load()
	if s.ContextCap == n {
		return nil
	}
	s.ContextCap = n
	if err := settings.Save(s); err != nil {
		return err
	}
	catalog.Touched()
	return nil
}
