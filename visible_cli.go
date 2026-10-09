package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	stats "github.com/yetone/magpie/internal/usage"
)

// Which models each agent is shown, from the terminal: magpie visible.

const visibleUsage = `usage:
  magpie visible                          which models each agent is shown
  magpie visible <agent> <name>[,<name>…] show the agent only these: families (a tag set with
                                          magpie provider set <id> family=relay, or magpie group set),
                                          provider ids and group ids
  magpie visible <agent> all              show the agent every model again
  magpie visible <agent> --only-picked    show the agent only the models ticked for it now; a model
                                          that comes later, of any provider, stays off until it is
                                          ticked in its list on the Agents page
  magpie visible <agent> --show-new       show it the models that come later again (the default)

  An agent not narrowed is shown every model. The gateway's model list and the model lists
  magpie writes into the agents' files are narrowed alike; a model kept from an agent still
  answers when the agent asks for it by name. magpie models <agent> shows what it is shown,
  and why the others aren't. --only-picked and --show-new keep the models shown as they are,
  and keep the providers, families and groups the agent is narrowed to.

  e.g. magpie provider set opencode-go family=ocgo
       magpie group set gpt-plus-auto family=relay
       magpie visible zcode relay,ocgo`

func agentIDs() []string {
	var ids []string
	for _, a := range agent.All() {
		ids = append(ids, a.ID)
	}
	return ids
}

// agentID is the agent a typed name is, by its id or an alias.
func agentOf(name string) string {
	return stats.AgentOf(strings.ToLower(strings.TrimSpace(name)))
}

func knownAgent(id string) bool { return slices.Contains(agentIDs(), id) }

// names is every name a visibility can hold: the families, and the
// providers' and groups' ids.
func visibleNames() []string {
	names := provider.Families()
	for _, p := range provider.All() {
		names = append(names, p.ID)
	}
	for _, g := range provider.Groups() {
		names = append(names, g.ID)
	}
	return names
}

func visibleCmd(args []string) error {
	if len(args) > 0 && slices.Contains([]string{"help", "-h", "--help"}, args[0]) {
		fmt.Println(visibleUsage)
		return nil
	}
	s := settings.Load()
	if len(args) == 0 {
		if len(s.Visible) == 0 && len(s.PickedModels) == 0 {
			fmt.Println(muted.Render("  every agent is shown every model · magpie visible <agent> <family>,… narrows one"))
		}
		ids := slices.Collect(maps.Keys(s.Visible))
		for id := range s.PickedModels {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		slices.Sort(ids)
		for _, id := range ids {
			shown, hidden := provider.CatalogFor(id)
			var says []string
			if names, ok := s.Visible[id]; ok {
				says = append(says, strings.Join(names, ", "))
			}
			if _, ok := s.PickedModels[id]; ok {
				says = append(says, "only models picked")
			}
			fmt.Printf("  %s  %s  %s\n", pad(id, 12), strings.Join(says, " · "),
				muted.Render(fmt.Sprintf("%d shown, %d not", len(shown), len(hidden))))
		}
		if fs := provider.Families(); len(fs) > 0 {
			fmt.Println(faint.Render("  families: " + strings.Join(fs, ", ")))
		}
		return nil
	}
	id := agentOf(args[0])
	if !knownAgent(id) {
		return fmt.Errorf("no agent %q (%s)", args[0], strings.Join(agentIDs(), ", "))
	}
	if len(args) == 1 {
		return models([]string{id})
	}
	if len(args) == 2 && (args[1] == "--only-picked" || args[1] == "--show-new") {
		if err := provider.SetOnlyPicked(id, args[1] == "--only-picked"); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "saved")
		return models([]string{id})
	}
	list := splitList(strings.Join(args[1:], ","))
	if len(list) == 1 && strings.EqualFold(list[0], "all") {
		delete(s.Visible, id)
	} else {
		known := visibleNames()
		for i, n := range list {
			n = strings.TrimPrefix(n, provider.GroupPrefix)
			if !slices.ContainsFunc(known, func(k string) bool { return strings.EqualFold(k, n) }) {
				return fmt.Errorf("%s is no family, provider or group (families: %s; magpie provider set <id> family=%s makes one)",
					n, orNone(provider.Families()), n)
			}
			list[i] = n
		}
		if s.Visible == nil {
			s.Visible = map[string][]string{}
		}
		s.Visible[id] = list
	}
	if err := settings.Save(s); err != nil {
		return err
	}
	if catalog.Changed != nil {
		catalog.Changed() // the agents' files follow
	}
	fmt.Println(green.Render("✓"), "saved")
	return models([]string{id})
}

func orNone(xs []string) string {
	if len(xs) == 0 {
		return "none yet"
	}
	return strings.Join(xs, ", ")
}

// explainHidden says what is kept from an agent, and why: what the kept
// models go by, none of which its visibility names, and how many its list
// on the Agents page leaves out besides — the ones not picked, for an
// agent shown only the models picked for it.
func explainHidden(id string, hidden []provider.Entry) {
	_, kept := provider.ListedFor(id)
	if n := len(hidden) - len(kept); n > 0 {
		if _, only := provider.PickedModels(id); only {
			fmt.Println(faint.Render("  " + id + " is shown only the models picked for it: " + fmt.Sprint(n) + " not picked, and a new one stays off until it is ticked · magpie visible " + id + " --show-new shows new ones"))
		} else {
			fmt.Println(faint.Render("  " + fmt.Sprint(n) + " taken out of " + id + "'s list on the Agents page"))
		}
	} else if _, only := provider.PickedModels(id); only {
		fmt.Println(faint.Render("  " + id + " is shown only the models picked for it; a new one stays off until it is ticked · magpie visible " + id + " --show-new shows new ones"))
	}
	hidden = kept
	names, ok := provider.VisibleTo(id)
	if !ok {
		fmt.Println(faint.Render("  " + id + " is shown every provider's models · magpie visible " + id + " <family>,… narrows it"))
		return
	}
	fmt.Println(faint.Render("  " + id + " is shown " + strings.Join(names, ", ")))
	if len(hidden) == 0 {
		return
	}
	by := map[string][]string{}
	var order []string
	for _, e := range hidden {
		k := strings.Join(e.Names()[:1], "")
		if e.Family == "" {
			k = "no family · " + k
		} else {
			k = "family " + k
		}
		if _, seen := by[k]; !seen {
			order = append(order, k)
		}
		by[k] = append(by[k], e.ID)
	}
	fmt.Println(amber.Render("  ! "+fmt.Sprint(len(hidden))) + muted.Render(" kept from "+id+", in none of those:"))
	for _, k := range order {
		ids := by[k]
		more := ""
		if len(ids) > 4 {
			ids, more = ids[:4], fmt.Sprintf(" +%d", len(by[k])-4)
		}
		fmt.Println(muted.Render("    " + k + ": " + strings.Join(ids, ", ") + more))
	}
}
