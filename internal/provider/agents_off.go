package provider

import (
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// EveryAgent is the key settings' HiddenModels keeps the models turned
// off for agents under, on the Gateway page's model list (辰林鸭~ on
// Discord): a provider's model is taken out of every agent's lists at
// once, as each agent's own picks take one out of its, while routing
// groups made of it and a request that names it are served as before.
// Kept beside the agents' own picks, so every rename of a provider or
// group that carries those carries these too. No agent's id is "*".
const EveryAgent = "*"

// AgentsOff are the models turned off for agents, by catalog id.
func AgentsOff() map[string]bool { return HiddenModels(EveryAgent) }

// SetAgentsOff turns these models off for agents, or back on. A routing
// group is never among them: it is the one agents keep seeing (its own
// switch is on the Routing page), so a group's id is passed by. The
// agents' own files are written again (catalog.Touched).
func SetAgentsOff(ids []string, off bool) error {
	s := settings.Load()
	was := s.HiddenModels[EveryAgent]
	keep := slices.Clone(was)
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || IsGroupRef(id) || strings.HasPrefix(id, AutoPrefix) {
			continue
		}
		at := slices.Index(keep, id)
		switch {
		case off && at < 0:
			keep = append(keep, id)
		case !off && at >= 0:
			keep = slices.Delete(keep, at, at+1)
		}
	}
	slices.Sort(keep)
	if slices.Equal(keep, was) {
		return nil
	}
	if len(keep) == 0 {
		delete(s.HiddenModels, EveryAgent)
	} else {
		if s.HiddenModels == nil {
			s.HiddenModels = map[string][]string{}
		}
		s.HiddenModels[EveryAgent] = keep
	}
	if err := settings.Save(s); err != nil {
		return err
	}
	catalog.Touched()
	return nil
}
