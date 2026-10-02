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
)

// Which models each agent is shown, from the terminal: magpie visible.

const visibleUsage = `usage:
  magpie visible                          which models each agent is shown
  magpie visible <agent> <name>[,<name>…] show the agent only these: families (a tag set with
                                          magpie provider set <id> family=relay, or magpie group set),
                                          provider ids and group ids
  magpie visible <agent> all              show the agent every model again

  An agent not narrowed is shown every model. The gateway's model list and the model lists
  magpie writes into the agents' files are narrowed alike; a model kept from an agent still
  answers when the agent asks for it by name. magpie models <agent> shows what it is shown,
  and why the others aren't.

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
	if len(args) == 0 {
		s := settings.Load()
		if len(s.Visible) == 0 {
			fmt.Println(muted.Render("  every agent is shown every model · magpie visible <agent> <family>,… narrows one"))
		}
		for _, id := range slices.Sorted(maps.Keys(s.Visible)) {
			shown, hidden := provider.CatalogFor(id)
			fmt.Printf("  %s  %s  %s\n", pad(id, 12), strings.Join(s.Visible[id], ", "),
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
	id := a.ID
	if len(args) == 1 {
		return models([]string{id})
	}
	list := splitList(strings.Join(args[1:], ","))
	if err := setVisible(id, list); err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), "saved")
	return models([]string{id})
}

// setVisible saves which models an agent is shown under the same lowercase
// key VisibleTo reads, and tells its files to follow.
func setVisible(id string, list []string) error {
	id = strings.ToLower(id)
	s := settings.Load()
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
	// Drop keys saved with the distro's case before writes were normalized.
	for key := range s.Visible {
		if key != id && strings.EqualFold(key, id) {
			delete(s.Visible, key)
		}
	}
	if err := settings.Save(s); err != nil {
		return err
	}
	if catalog.Changed != nil {
		catalog.Changed() // the agents' files follow
	}
	return nil
}

func orNone(xs []string) string {
	if len(xs) == 0 {
		return "none yet"
	}
	return strings.Join(xs, ", ")
}

// explainHidden says what is kept from an agent, and why: what the kept
// models go by, none of which its visibility names.
func explainHidden(id string, hidden []provider.Entry) {
	names, ok := provider.VisibleTo(id)
	if !ok {
		fmt.Println(faint.Render("  " + id + " is shown every model · magpie visible " + id + " <family>,… narrows it"))
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
