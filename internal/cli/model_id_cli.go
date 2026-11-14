package cli

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// modelID is `magpie model id`: the id a provider's model is listed,
// routed and counted by in magpie, while the provider is asked for it by
// its own (provider.SetModelID).
func modelID(args []string) error {
	if len(args) == 0 || len(args) > 2 {
		return fmt.Errorf("%s", modelUsage)
	}
	if slices.Contains([]string{"-h", "--help", "help"}, args[0]) {
		fmt.Println(modelUsage)
		return nil
	}
	p, model, err := modelRef(args[0])
	if err != nil {
		return err
	}
	if len(args) == 1 {
		own := p.OwnModelID(model)
		if own == model {
			if local, ok := localOf(p, model); ok {
				fmt.Println(bold.Render(p.ID+"/"+local), muted.Render("· listed as "+local+" · "+p.ID+" is asked for "+model))
				return nil
			}
			fmt.Println(bold.Render(p.ID+"/"+model), muted.Render("· listed by its own id"))
			return nil
		}
		fmt.Println(bold.Render(p.ID+"/"+model), muted.Render("· "+p.ID+" is asked for "+own))
		return nil
	}
	id := strings.TrimSpace(args[1])
	if isReset(args[1:]) {
		id = ""
	}
	// the provider's own id of it, before it is listed anew
	own := p.OwnModelID(model)
	replaced, changed, err := provider.SetModelID(args[0], id)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Println(muted.Render(p.ID + "/" + model + " is listed so already"))
		return nil
	}
	if id == "" || id == own {
		fmt.Println(bold.Render(p.ID+"/"+own), muted.Render("· listed by its own id again"))
		return nil
	}
	fmt.Println(bold.Render(p.ID+"/"+id), muted.Render("· listed, routed and counted so; "+p.ID+" is still asked for "+own))
	if replaced != "" {
		fmt.Println(muted.Render("  " + replaced + " had that id: it is listed by its own again"))
	}
	if n := strings.TrimSpace(settings.Shared().ModelWires[p.ID+"/"+id]); n != "" {
		fmt.Println(muted.Render("  a wire name of yours is set for " + p.ID + "/" + id + ": it is asked for as " + n + " (magpie model wire --reset takes it off)"))
	}
	return nil
}

// localOf is the id the user lists p's model of its own id own by.
func localOf(p *provider.Provider, own string) (string, bool) {
	for local, o := range p.ModelIDs() {
		if o == own {
			return local, true
		}
	}
	return "", false
}

// modelIDs is `magpie model ids`: every model listed by an id the user
// gave it, with the provider's own.
func modelIDs() error {
	ids := settings.Load().ModelIDs
	keys := slices.Sorted(maps.Keys(ids))
	w := 0
	for _, k := range keys {
		w = max(w, len(k))
	}
	n := 0
	for _, k := range keys {
		own := strings.TrimSpace(ids[k])
		if own == "" {
			continue
		}
		n++
		fmt.Printf("%-*s  %s\n", w, k, muted.Render("is asked for as ")+own)
	}
	if n == 0 {
		fmt.Println(muted.Render("no model is listed by an id of yours yet · magpie model id <provider/model> <id>"))
	}
	return nil
}
