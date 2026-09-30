package main

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A model's name and reasoning levels, from the terminal: magpie model.

const modelUsage = `usage:
  magpie model name <provider/model>             the name the model goes by
  magpie model name <provider/model> <name>      name it so everywhere: in magpie, in the gateway's
                                                 model list, and in the lists magpie writes into the agents
  magpie model name <provider/model> --reset     give it back its own name
  magpie model efforts <provider/model>          the reasoning levels it offers, and those it has
  magpie model efforts <provider/model> <l>,<l>  offer only these of them, e.g. low,medium,high
  magpie model efforts <provider/model> --reset  offer every level it has again
  magpie model price <provider/model>            what the model costs you, and what you said it costs
  magpie model price <provider/model> <in>,<out>,<cache read>,<cache write>
                                                 say what it costs, in USD per million tokens, all four parts as
                                                 0.12,1.20,0.01,0.15; 0 is a model served for nothing, which is
                                                 a price, not the absence of one
  magpie model price <provider/model> --reset    take your price off this model
  magpie model prices                            the models you priced
  magpie model names                             the models you named or narrowed

  Each is looked for in this order: this model, then <provider id>/*, then the provider's own
  list, then models.dev. --reset removes only the first, and says so when a <provider id>/* value
  still applies.

  Only what agents are shown changes: they still pick the model, and requests still reach it,
  as <provider/model>. The same model from another provider keeps its own name and levels.
  A price is one provider's tariff for one model, not the model's own: it changes what the
  usage and session totals report, and nothing an agent can see, and it is saved without
  rewriting the model lists in the agents' own files.

  e.g. magpie model name claude/claude-opus-5-5 "Opus 5.5"
       magpie model efforts openai/gpt-6 low,medium,high
       magpie model price relay-a/gpt-5.5 0.12,0.60,0.01,0.15`

func modelCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", modelUsage)
	}
	switch args[0] {
	case "names", "ls", "list":
		return modelNames()
	case "name", "rename":
		return modelName(args[1:])
	case "efforts", "effort", "levels":
		return modelEfforts(args[1:])
	case "price", "cost":
		return modelPrice(args[1:])
	case "prices":
		return modelPrices()
	case "help", "-h", "--help":
		fmt.Println(modelUsage)
		return nil
	}
	return fmt.Errorf("unknown command %q\n\n%s", args[0], modelUsage)
}

// modelRef is the provider and model a typed provider/model names.
func modelRef(s string) (*provider.Provider, string, error) {
	pid, model, err := splitModelRef(s)
	if err != nil {
		return nil, "", err
	}
	p, err := provider.Find(pid)
	if err != nil {
		return nil, "", err
	}
	return p, model, nil
}

// splitModelRef is "provider/model" as the id before the slash and the model
// after it, without asking for the provider. A ref the user must still be
// able to name is one whose provider may be gone: the key a price is
// stored at outlives the provider it was set for.
func splitModelRef(s string) (string, string, error) {
	ref := strings.TrimPrefix(strings.TrimSpace(s), "magpie/")
	pid, model, ok := strings.Cut(ref, "/")
	if !ok || model == "" {
		return "", "", fmt.Errorf("name a model as provider/model, not %q (magpie models lists them)", s)
	}
	return pid, model, nil
}

func isReset(args []string) bool {
	return len(args) == 1 && slices.Contains([]string{"--reset", "-r", "--default", "default", "reset"}, args[0])
}

func modelName(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", modelUsage)
	}
	p, model, err := modelRef(args[0])
	if err != nil {
		return err
	}
	own := model
	for _, m := range p.Available() {
		if m.ID == model && m.Name != "" {
			own = m.Name
			break
		}
	}
	id := p.ID + "/" + model
	rest := args[1:]
	if len(rest) == 0 {
		if n, ok := provider.ModelName(p.ID, model); ok {
			fmt.Println(bold.Render(n), muted.Render("· "+id+" · its own name is "+own))
		} else {
			fmt.Println(bold.Render(own), muted.Render("· "+id+" · its own name"))
		}
		return nil
	}
	name := strings.Join(rest, " ")
	if isReset(rest) {
		name = ""
	}
	if err := provider.SetModelName(id, name); err != nil {
		return err
	}
	if name == "" {
		fmt.Println(green.Render("✓"), id, muted.Render("is called"), bold.Render(own), muted.Render("again"))
	} else {
		fmt.Println(green.Render("✓"), id, muted.Render("is called"), bold.Render(strings.Join(strings.Fields(name), " ")))
	}
	return nil
}

func modelEfforts(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", modelUsage)
	}
	p, model, err := modelRef(args[0])
	if err != nil {
		return err
	}
	id := p.ID + "/" + model
	all := p.Efforts(model)
	rest := args[1:]
	given := len(p.Known(model)) == 0 // levels aren't known: those it has are the user's
	if len(rest) == 0 {
		if len(all) == 0 {
			fmt.Println(muted.Render(id+" has no reasoning levels known"), faint.Render("· give it some of "+strings.Join(provider.Levels, ", ")))
			return nil
		}
		if given {
			fmt.Println(bold.Render(strings.Join(all, " ")), muted.Render("· given it · --reset takes them away"))
			return nil
		}
		kept, narrowed := p.ModelEfforts()[model]
		for _, e := range all {
			if !narrowed || slices.Contains(kept, e) {
				fmt.Print(bold.Render(e), " ")
			} else {
				fmt.Print(faint.Render(e), " ")
			}
		}
		if narrowed {
			fmt.Println(muted.Render("· only the bold ones are offered · --reset offers them all"))
		} else {
			fmt.Println(muted.Render("· all offered"))
		}
		return nil
	}
	var levels []string
	if !isReset(rest) {
		for _, a := range rest {
			for _, l := range strings.FieldsFunc(a, func(r rune) bool { return r == ',' || r == ' ' || r == '/' }) {
				levels = append(levels, l)
			}
		}
	}
	if err := provider.SetModelEfforts(id, levels); err != nil {
		return err
	}
	if given {
		if all = p.Efforts(model); len(all) > 0 {
			fmt.Println(green.Render("✓"), id, muted.Render("offers"), bold.Render(strings.Join(all, ", ")))
		} else {
			fmt.Println(green.Render("✓"), id, muted.Render("has no reasoning levels again"))
		}
		return nil
	}
	if kept, ok := p.ModelEfforts()[model]; ok {
		fmt.Println(green.Render("✓"), id, muted.Render("offers"), bold.Render(strings.Join(kept, ", ")))
	} else {
		fmt.Println(green.Render("✓"), id, muted.Render("offers every level it has"), faint.Render(strings.Join(all, ", ")))
	}
	return nil
}

// modelPrice shows what a provider's model is counted at, or sets what the
// user says it costs in USD per million tokens. All four parts are given: a
// price is a cost report, and one missing a part would understate it.
func modelPrice(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", modelUsage)
	}
	rest := args[1:]
	// --reset comes before the provider is looked for, and works off the
	// key the price is stored at: a price outlives the provider it was set
	// for, and one whose provider is gone is still listed by `magpie model
	// prices` and still counted, so a reset that resolved the provider
	// first could not take it away — and would take another provider's
	// price away instead where the gone one's name has been given to one.
	if isReset(rest) {
		return resetModelPrice(args[0])
	}
	p, model, err := modelRef(args[0])
	if err != nil {
		return err
	}
	id := p.ID + "/" + model
	if len(rest) == 0 {
		pr, ok := provider.EffectivePrice(p.ID, model)
		if !ok {
			fmt.Println(muted.Render(id), faint.Render("· no price is known for it"))
			return nil
		}
		fmt.Println(bold.Render(perMillion(pr)), muted.Render("· "+id))
		fmt.Println(faint.Render("  · cache read " + money(pr.CacheRead) + ", cache write " + money(pr.CacheWrite)))
		switch priceFrom(settings.Load(), p.ID, model) {
		case "model":
			fmt.Println(faint.Render("  · what you said this model costs · --reset takes that away"))
		case "provider":
			fmt.Println(faint.Render("  · what you said every model of this provider costs · magpie model price " +
				p.ID + "/* --reset takes that away"))
		case "ignored":
			fmt.Println(faint.Render("  · a price you gave is not usable and is ignored"))
		default:
			fmt.Println(faint.Render("  · what its provider lists, else its maker's on models.dev · magpie model price " +
				id + " <in>,<out>,<cache read>,<cache write> to change it"))
		}
		return nil
	}
	var nums []float64
	for _, a := range rest {
		for _, f := range strings.FieldsFunc(a, func(r rune) bool { return r == ',' || r == ' ' || r == '/' }) {
			v, err := strconv.ParseFloat(f, 64)
			if err != nil {
				return fmt.Errorf("a price is numbers in USD per million tokens, like 0.12,1.20,0.01,0.15, not %q", f)
			}
			nums = append(nums, v)
		}
	}
	if len(nums) != 4 {
		return fmt.Errorf("give all four parts, input,output,cache read,cache write — %d given", len(nums))
	}
	pr := catalog.Price{Input: nums[0], Output: nums[1], CacheRead: nums[2], CacheWrite: nums[3]}
	if err := provider.SetModelPrice(id, &pr); err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), id, muted.Render("costs"), bold.Render(perMillion(pr)))
	return nil
}

// resetModelPrice takes a price off and says what the model is counted at
// now. The key it works off is the one the price is stored at, which is not
// always the one that was typed: where nothing is stored under the spelling
// it is the id the provider has now, which is where a price set through a
// display name is kept. provider.PriceKey is what decides that, and it
// decides it the same way for both callers, so this cannot resolve one way
// and the removal another.
//
// A key with no price under it is an error rather than a tick: there was
// nothing to take away, and a user who mistyped an id, or who is priced only
// through a provider-wide price, has to be told so rather than left with a
// ✓ over a price that is still counted.
func resetModelPrice(ref string) error {
	pid, model, err := splitModelRef(ref)
	if err != nil {
		return err
	}
	key, there := provider.PriceKey(pid, model)
	dropped, err := provider.DropModelPrice(key)
	if err != nil {
		return err
	}
	s := settings.Load()
	wid, _, _ := strings.Cut(key, "/")
	if !dropped {
		if _, wide := statedPrice(s.ModelPrices, wid+"/*"); wide {
			return fmt.Errorf("%s has no price of its own; what it costs is what you set for every model of this provider, %s/*, which magpie model price %s/* --reset takes away",
				key, wid, wid)
		}
		return fmt.Errorf("%s has no price of its own to reset — magpie model prices lists the ones you set", key)
	}
	if !there {
		fmt.Println(green.Render("✓"), key,
			muted.Render("no longer has a price of its own; its provider is gone, so it is costed at its maker's, if models.dev lists one"))
		return nil
	}
	if _, wide := statedPrice(s.ModelPrices, wid+"/*"); wide {
		fmt.Println(green.Render("✓"), key,
			muted.Render("no longer has a price of its own; it still costs what you set for every model of this provider"))
		return nil
	}
	fmt.Println(green.Render("✓"), key,
		muted.Render("no longer has a price of its own; it is costed at what its provider lists, or its maker's"))
	return nil
}

// perMillion is a price as the two numbers a reader wants first.
func perMillion(p catalog.Price) string {
	return fmt.Sprintf("$%.4g/$%.4g per 1M in/out", p.Input, p.Output)
}

// money is one part of a per-million price as a price tuple writes it.
func money(v float64) string { return fmt.Sprintf("$%.4g", v) }

// statedPrice is the usable price a "<provider id>/<model id>" key holds, if
// it holds one at all. A key that is there with a part missing is not one:
// it would bill the rest of a call at zero.
func statedPrice(prices map[string]settings.ModelPrice, key string) (catalog.Price, bool) {
	m, ok := prices[key]
	if !ok {
		return catalog.Price{}, false
	}
	p, bad := m.Price()
	return p, bad == ""
}

// priceFrom says where a model's effective price came from: the price given
// for it, the one given for every model of its provider, a price that is
// there but unusable, or neither — which leaves the provider's own list price
// and then its maker's. The order is the one EffectivePrice looks in.
func priceFrom(s settings.Settings, providerID, model string) string {
	if _, ok := statedPrice(s.ModelPrices, providerID+"/"+model); ok {
		return "model"
	}
	if _, ok := statedPrice(s.ModelPrices, providerID+"/*"); ok {
		return "provider"
	}
	if _, given := s.ModelPrices[providerID+"/"+model]; given {
		return "ignored"
	}
	if _, given := s.ModelPrices[providerID+"/*"]; given {
		return "ignored"
	}
	return ""
}

// modelPrices lists what the user has said models cost, and which provider
// and model each is for.
func modelPrices() error {
	prices := settings.Load().ModelPrices
	if len(prices) == 0 {
		fmt.Println(muted.Render("no model is priced by you yet"))
		fmt.Println(faint.Render("magpie model price <provider/model> <in>,<out>,<cache read>,<cache write>"))
		return nil
	}
	for _, id := range slices.Sorted(maps.Keys(prices)) {
		p, bad := prices[id].Price()
		if bad != "" {
			fmt.Println(bold.Render(id), muted.Render("· no "+bad+" price given, and ignored"))
			continue
		}
		fmt.Println(bold.Render(id), muted.Render("· "+perMillion(p)+
			" · cache "+money(p.CacheRead)+"/"+money(p.CacheWrite)))
	}
	return nil
}

func modelNames() error {
	s := settings.Load()
	keys := slices.Sorted(maps.Keys(s.ModelNames))
	for k := range s.ModelEfforts {
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	if len(keys) == 0 {
		fmt.Println(muted.Render("no model is named or narrowed yet · magpie model name <provider/model> <name>"))
		return nil
	}
	w := 0
	for _, k := range keys {
		w = max(w, len(k))
	}
	for _, k := range keys {
		line := "  " + pad(k, w)
		if n := s.ModelNames[k]; n != "" {
			line += "  " + n
		}
		if es := s.ModelEfforts[k]; len(es) > 0 {
			line += "  " + muted.Render(strings.Join(es, "/"))
		}
		fmt.Println(line)
	}
	return nil
}
