package cli

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Which models each agent is shown, from the terminal: magpie visible.

const visibleUsage = `usage:
  magpie visible                          which models each agent is shown
  magpie visible <agent> <name>[,<name>…] show the agent only these: families (a tag set with
                                          magpie provider set <id> family=relay, or magpie group set),
                                          provider ids and group ids
  magpie visible <agent> all              show the agent every model again, the ones taken out of
                                          its list one by one too
  magpie visible <agent> --only-picked    show the agent only the models ticked for it now; a model
                                          that comes later, of any provider, stays off until it is
                                          ticked in its list on the Agents page
  magpie visible <agent> --show-new       show it the models that come later again (the default)
  magpie visible <agent> --provider-picks shown only its picks, it takes in the models picked for a
                                          provider too (magpie provider models <id> …, or ticked in
                                          a provider's editor), as it is added or later; other new
                                          models still wait · --no-provider-picks: they wait too

  An agent not narrowed is shown every model. The gateway's model list and the model lists
  magpie writes into the agents' files are narrowed alike; a model kept from an agent still
  answers when the agent asks for it by name. magpie models <agent> shows what it is shown,
  and why the others aren't. --only-picked and --show-new keep the models shown as they are,
  and keep the providers, families and groups the agent is narrowed to.

  e.g. magpie provider set opencode-go family=ocgo
       magpie group set gpt-plus-auto family=relay
       magpie visible zcode relay,ocgo`

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
				if slices.Contains(s.PicksJoin, id) {
					says = append(says, "only models picked, a provider's picks taken in")
				} else {
					says = append(says, "only models picked")
				}
			}
			fmt.Printf("  %s  %s  %s\n", pad(id, 12), strings.Join(says, " · "),
				muted.Render(fmt.Sprintf("%d shown, %d not", len(shown), len(hidden))))
		}
		if fs := provider.Families(); len(fs) > 0 {
			fmt.Println(faint.Render("  families: " + strings.Join(fs, ", ")))
		}
		return nil
	}
	a, err := agent.Find(args[0])
	if err != nil {
		return err
	}
	// VisibleTo looks the id up lowered, so the key kept here — and taken
	// back out — is lowered alike, a WSL id's case and all
	id := strings.ToLower(a.ID)
	if len(args) == 1 {
		return models([]string{id})
	}
	if len(args) == 2 && (args[1] == "--provider-picks" || args[1] == "--no-provider-picks") {
		if err := provider.SetPicksJoin(id, args[1] == "--provider-picks"); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "saved")
		if _, only := provider.PickedModels(id); !only && args[1] == "--provider-picks" {
			fmt.Println(faint.Render("  " + id + " is shown every model not taken out, a provider's picks among them; it takes them in once it is shown only its picks · magpie visible " + id + " --only-picked"))
		}
		return nil
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
		// all is the recovery the hints point at (magpie models, the
		// gateway's refusal to name a model): the models taken out of the
		// agent's list one by one, and the only-picked switch, keep it
		// shown none or fewer unless these go too (#4587)
		delete(s.HiddenModels, id)
		delete(s.PickedModels, id)
	} else {
		known := visibleNames()
		for i, n := range list {
			n = strings.TrimPrefix(provider.CanonicalRef(n), provider.GroupPrefix)
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
	// after the ones its visibility keeps, the ones its picks leave out,
	// and those turned off for every agent on the Gateway page
	every, own := provider.AgentsOff(), provider.PicksOff(id)
	n := len(hidden) - len(kept)
	if len(every) > 0 {
		vis := map[string]bool{}
		for _, e := range kept {
			vis[e.ID] = true
		}
		n = 0
		gw := 0
		for _, e := range hidden {
			switch {
			case vis[e.ID]:
			case own(e.ID):
				n++
			case every[e.ID]:
				gw++
			}
		}
		if gw > 0 {
			fmt.Println(faint.Render("  " + fmt.Sprint(gw) + " turned off for every agent on the Gateway page"))
		}
	}
	if n > 0 {
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
