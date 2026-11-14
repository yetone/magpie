package provider

import (
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// The window an agent is told for a model, when it is not the model's own
// for every agent (settings.AgentContexts, ul3140773077392 on X): agents
// compact by different rules (dsh at the smaller of 80% of the window and
// window - max tokens - 64K, Codex at its 272K), so one window fits some
// and has others compact too early. An agent's entry comes before the
// provider's and the group's own size, and is a window the user typed
// (Entry.WindowSet): Codex and Claude Code are then told it whole.

// agentKey is the id an agent's windows are kept under.
func agentKey(agent string) string { return strings.ToLower(strings.TrimSpace(agent)) }

// agentWindow is the window agent was given for e, and whether it has one.
// The most specific key wins: the model as the catalog has it, its
// provider's "*", the bare model id, then "*" for all, which only shortens
// a window that is known (as a provider's "*" does).
func agentWindow(own map[string]int, e Entry) (int, bool) {
	if len(own) == 0 {
		return 0, false
	}
	if n := own[e.ID]; n > 0 {
		return n, true
	}
	if e.Group == "" {
		if n := own[e.Provider.ID+"/*"]; n > 0 {
			return n, true
		}
		if n := own[e.Model]; n > 0 {
			return n, true
		}
	}
	if n := own["*"]; n > 0 && e.Context > 0 && n < e.Context {
		return n, true
	}
	return 0, false
}

// withAgentWindows gives the entries the windows agent was set.
func withAgentWindows(s settings.Settings, agent string, es []Entry) []Entry {
	own := s.AgentContexts[agentKey(agent)]
	if len(own) == 0 {
		return es
	}
	// copied: the list may be the catalog's own, which other agents read
	es = slices.Clone(es)
	for i := range es {
		if n, ok := agentWindow(own, es[i]); ok {
			es[i].Context, es[i].WindowSet = n, true
		}
	}
	return es
}

// AgentContexts are the windows agent (its list id, Agent.ListsFor) was set.
func AgentContexts(agent string) map[string]int {
	return maps.Clone(settings.Shared().AgentContexts[agentKey(agent)])
}

// SetAgentContexts gives agent the windows in cs, replacing those it had;
// none takes them away, and it is told each model's own again. A key is a
// model as the catalog has it ("provider/model", "group/<id>"), a provider's
// "provider/*", a bare model id or "*". The agents are told when it changes.
func SetAgentContexts(agent string, cs map[string]int) error {
	agent = agentKey(agent)
	if agent == "" {
		return errors.New("no agent")
	}
	clean := map[string]int{}
	for k, n := range cs {
		k = strings.TrimPrefix(strings.TrimSpace(k), "magpie/")
		switch {
		case n < 0:
			return errorf("a context window is a number of tokens, not %d", n)
		case n == 0:
		case k == "":
			return errors.New("a context window needs a model, provider/* or * to hold for")
		default:
			clean[k] = n
		}
	}
	s := settings.Load()
	if maps.Equal(s.AgentContexts[agent], clean) || len(clean) == 0 && len(s.AgentContexts[agent]) == 0 {
		return nil
	}
	s.AgentContexts = maps.Clone(s.AgentContexts)
	if len(clean) == 0 {
		delete(s.AgentContexts, agent)
	} else {
		if s.AgentContexts == nil {
			s.AgentContexts = map[string]map[string]int{}
		}
		s.AgentContexts[agent] = clean
	}
	if err := settings.Save(s); err != nil {
		return err
	}
	catalog.Touched()
	return nil
}

// AgentContextAgents are the agents that have a window of their own, sorted.
func AgentContextAgents() []string {
	return slices.Sorted(maps.Keys(settings.Load().AgentContexts))
}
