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
                                                 a price, not the absence of one; a fifth part is a 1-hour cache
                                                 write's (not given: a Claude model's is 2× input, any other's the 4th)
  magpie model price <provider/model> <in>,<out>,<cache read>,<cache write> --tier 272k <in>,<out>,<cr>,<cw>
                                                 and what a request whose input, cached tokens included, is over
                                                 272K costs, the whole request; --tier may be given again
  magpie model price <provider/model> --reset    take your price off this model
  magpie model price <model> <in>,<out>,<cache read>,<cache write>
                                                 what the model costs from any provider you have not priced it
                                                 for, kept as '*/<model>': for usage whose provider is gone, or
                                                 a model models.dev doesn't price, e.g. gemini-3-pro-preview
  magpie model prices                            the models you priced
  magpie model context <provider/model>          how long a request it takes, and what you said it takes
  magpie model context <provider/model> <n>      say how long, as 200000 or 1m; '<provider>/*' is every model
  magpie model context <provider/model> --reset  take your limit off this model
  magpie model output <provider/model>           the most a reply of it may hold, and what you said
  magpie model output <provider/model> <n>       say the most, as 128000 or 128k; '<provider>/*' is every model
  magpie model output <provider/model> --reset   take your limit off this model
  magpie model wire <provider/model>             the name the vendor is asked for, and the one you gave
  magpie model wire <provider/model> <name>      ask for the model by this name, for a relay that serves it
  magpie model wire '<provider>/*' <name>        ask for every model of that provider by this name; a * in
                                                 it is the model, so vendor-c/* asks for model-3 as
                                                 vendor-c/model-3. Quote it: a shell reads a bare * as a glob
  magpie model wire <provider/model> --reset     ask for it by the name magpie knows it by again, under an
                                                 id of its own for a provider that has since been deleted,
                                                 and says when there was no name of its own to take away
  magpie model wires                             the names your vendors are asked for models by
  magpie model names                             the models you named or narrowed
  magpie model suffix [on|own|off]               whether the agents' lists name each model with its provider
                                                 (or "routing group") after it: on, as by default, "Sol · OpenAI";
                                                 own, a name you gave a model just as you wrote it, "Opus 5.5",
                                                 the others as on; off, "Sol" alone — but two a list would name
                                                 the same keep it
  magpie model compact [on|off|<size>]           whether Codex and Claude Code compact a long conversation at
                                                 272K: on, as by default, for a model of a longer window (in
                                                 Claude Code, a Claude model runs to its own); off, at the
                                                 model's whole window, 1M for a [1m] one; a size such as 500k
                                                 compacts there instead. The app's Settings → Long
                                                 conversations is the same switch; a provider's own Compact
                                                 at (its editor) comes before it

  Each is looked for in this order: this model, then <provider id>/*, then the provider's own
  list, then models.dev. --reset removes only the first, and says so when a <provider id>/* value
  still applies. A price comes from '*/<model>' after <provider id>/* and before any list price.

  Only what agents are shown changes with a name or levels: they still pick the model, and
  requests still reach it, as <provider/model>. The same model from another provider keeps
  its own name, levels and limits.
  A price is one provider's tariff for one model, not the model's own: it changes what the
  usage and session totals report, and nothing an agent can see, and it is saved without
  rewriting the model lists in the agents' own files.

  e.g. magpie model name claude/claude-opus-5-5 "Opus 5.5"
       magpie model efforts openai/gpt-6 low,medium,high
       magpie model price relay-a/gpt-5.5 0.12,0.60,0.01,0.15
       magpie model context relay-a/claude-opus-5-5 1m`

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
	case "wires":
		return modelWires()
	case "suffix", "suffixes":
		return modelSuffix(args[1:])
	case "compact", "full-context":
		return modelCompact(args[1:])
	case "context", "ctx":
		return modelContext(args[1:])
	case "output", "max-output":
		return modelOutput(args[1:])
	case "wire", "upstream":
		return modelWire(args[1:])
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
	ref := args[0]
	// a model named with no provider, or as */model, is that model from any
	// provider: what prices a session's model whose provider is gone, or one
	// that never went through magpie
	every := false
	if r := strings.TrimPrefix(strings.TrimSpace(ref), "magpie/"); r != "" && !strings.Contains(r, "/") {
		ref, every = settings.AnyProvider+r, true
	} else {
		every = strings.HasPrefix(r, settings.AnyProvider)
	}
	if isReset(rest) {
		return resetModelPrice(ref)
	}
	if every {
		return anyModelPrice(ref, rest)
	}
	p, model, err := modelRef(ref)
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
		for _, l := range priceDetail(pr) {
			fmt.Println(faint.Render(l))
		}
		switch priceFrom(settings.Load(), p.ID, model) {
		case "model":
			fmt.Println(faint.Render("  · what you said this model costs · --reset takes that away"))
		case "provider":
			fmt.Println(faint.Render("  · what you said every model of this provider costs · magpie model price " +
				typedRef(p.ID+"/*") + " --reset takes that away"))
		case "any":
			fmt.Println(faint.Render("  · what you said this model costs from any provider · magpie model price " +
				typedRef(provider.AnyPriceKey(model)) + " --reset takes that away"))
		case "ignored":
			fmt.Println(faint.Render("  · a price you gave is not usable and is ignored"))
		default:
			from := "what its provider lists, else its maker's on models.dev"
			if p.RemotePriced(model) {
				from = "what the other magpie counts it at"
			}
			fmt.Println(faint.Render("  · " + from + " · magpie model price " +
				typedRef(id) + " <in>,<out>,<cache read>,<cache write> to change it"))
		}
		return nil
	}
	pr, err := parsePrice(rest)
	if err != nil {
		return err
	}
	if err := provider.SetModelPrice(id, &pr); err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), id, muted.Render("costs"), bold.Render(perMillion(pr)))
	for _, t := range pr.Tiers {
		fmt.Println(faint.Render(fmt.Sprintf("  · and $%.4g/$%.4g in/out the whole request over %s input tokens", t.Input, t.Output, tokenSize(t.Above))))
	}
	return nil
}

// parsePrice is a price as typed, in USD per million tokens: its four
// parts, a 1-hour cache write's after them if given, and then each
// "--tier <size>" with the parts of what a request whose input is over that
// size costs.
func parsePrice(rest []string) (catalog.Price, error) {
	var groups [][]string
	var sizes []string
	cur := []string{}
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		size, isTier := strings.CutPrefix(a, "--tier=")
		if !isTier && a == "--tier" {
			if i+1 >= len(rest) {
				return catalog.Price{}, fmt.Errorf("--tier takes the input size it starts over, like --tier 272k 20,75,2,25")
			}
			isTier, size = true, rest[i+1]
			i++
		}
		if isTier {
			groups, sizes, cur = append(groups, cur), append(sizes, size), []string{}
			continue
		}
		cur = append(cur, a)
	}
	groups = append(groups, cur)
	var out catalog.Price
	for g, args := range groups {
		var nums []float64
		for _, a := range args {
			for _, f := range strings.FieldsFunc(a, func(r rune) bool { return r == ',' || r == ' ' || r == '/' }) {
				v, err := strconv.ParseFloat(f, 64)
				if err != nil {
					return catalog.Price{}, fmt.Errorf("a price is numbers in USD per million tokens, like 0.12,1.20,0.01,0.15, not %q", f)
				}
				nums = append(nums, v)
			}
		}
		if len(nums) != 4 && len(nums) != 5 {
			what := "the price"
			if g > 0 {
				what = "the tier over " + sizes[g-1]
			}
			return catalog.Price{}, fmt.Errorf("give all four parts of %s, input,output,cache read,cache write (and a 1-hour cache write after them if you like) — %d given", what, len(nums))
		}
		var oneHour float64
		if len(nums) == 5 {
			oneHour = nums[4]
		}
		if g == 0 {
			out = catalog.Price{Input: nums[0], Output: nums[1], CacheRead: nums[2], CacheWrite: nums[3], CacheWrite1h: oneHour}
			continue
		}
		above, err := provider.ParseTokens(sizes[g-1])
		if err != nil || above <= 0 {
			return catalog.Price{}, fmt.Errorf("--tier takes the input size it starts over, like 272k or 200000, not %q", sizes[g-1])
		}
		out.Tiers = append(out.Tiers, catalog.Tier{Above: above, Input: nums[0], Output: nums[1], CacheRead: nums[2], CacheWrite: nums[3], CacheWrite1h: oneHour})
	}
	slices.SortFunc(out.Tiers, func(a, b catalog.Tier) int { return a.Above - b.Above })
	return out, nil
}

// priceDetail is the parts of a price after the two perMillion gives: its
// cache's, and what a request over each of its sizes costs.
// A 1-hour cache write is shown only where the price has one — a Claude
// model's, or one given — not one no vendor bills (PAMI on Discord).
func priceDetail(p catalog.Price) []string {
	cache := "  · cache read " + money(p.CacheRead) + ", cache write " + money(p.CacheWrite)
	if p.CacheWrite1h > 0 {
		cache += " (5 min), " + money(p.CacheWrite1h) + " (1 hour)"
	}
	lines := []string{cache}
	for _, t := range p.Tiers {
		lines = append(lines, "  · over "+tokenSize(t.Above)+" input tokens, the whole request: "+
			fmt.Sprintf("$%.4g/$%.4g in/out", t.Input, t.Output)+", cache "+money(t.CacheRead)+"/"+money(t.CacheWrite))
	}
	return lines
}

// tokenSize is a number of tokens as --tier takes it: 272K, 1M.
func tokenSize(n int) string {
	switch {
	case n >= 1e6 && n%1e6 == 0:
		return strconv.Itoa(n/1e6) + "M"
	case n >= 1e3 && n%1e3 == 0:
		return strconv.Itoa(n/1e3) + "K"
	}
	return strconv.Itoa(n)
}

// anyModelPrice shows or sets what a model costs from any provider (*/model):
// the price a session's model is counted at when no price of a provider's
// own applies — its provider gone, the model no longer listed by it, or a
// model models.dev doesn't price.
func anyModelPrice(ref string, rest []string) error {
	key := provider.AnyPriceKey(strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(ref), "magpie/"), settings.AnyProvider))
	if key == settings.AnyProvider || key == settings.AnyProvider+"*" {
		return fmt.Errorf("name one model, such as %s", typedRef("*/claude-opus-4.6"))
	}
	if len(rest) == 0 {
		if pr, ok := statedPrice(settings.Load().ModelPrices, key); ok {
			catalog.OneHourFor(strings.TrimPrefix(key, settings.AnyProvider), &pr)
			fmt.Println(bold.Render(perMillion(pr)), muted.Render("· "+key))
			for _, l := range priceDetail(pr) {
				fmt.Println(faint.Render(l))
			}
			fmt.Println(faint.Render("  · what you said this model costs from any provider · --reset takes that away"))
			return nil
		}
		fmt.Println(muted.Render(key), faint.Render("· no price of yours · magpie model price "+typedRef(key)+" <in>,<out>,<cache read>,<cache write>"))
		return nil
	}
	pr, err := parsePrice(rest)
	if err != nil {
		return err
	}
	if err := provider.SetModelPrice(key, &pr); err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), key, muted.Render("costs"), bold.Render(perMillion(pr)),
		faint.Render("from any provider you have not priced it for"))
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
	if strings.HasPrefix(key, settings.AnyProvider) {
		if !dropped {
			return fmt.Errorf("%s has no price of yours to reset — magpie model prices lists the ones you set", key)
		}
		fmt.Println(green.Render("✓"), key,
			muted.Render("no longer has a price of yours; it is costed at what its provider lists, or its maker's on models.dev"))
		return nil
	}
	if !dropped {
		if _, wide := statedPrice(s.ModelPrices, wid+"/*"); wide {
			return fmt.Errorf("%s has no price of its own; what it costs is what you set for every model of this provider, %s/*, which magpie model price %s --reset takes away",
				key, wid, typedRef(wid+"/*"))
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
	if _, ok := statedPrice(s.ModelPrices, provider.AnyPriceKey(model)); ok {
		return "any"
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
		for _, t := range p.Tiers {
			fmt.Println(faint.Render(fmt.Sprintf("  · over %s input tokens $%.4g/$%.4g in/out · cache %s/%s", tokenSize(t.Above), t.Input, t.Output, money(t.CacheRead), money(t.CacheWrite))))
		}
	}
	return nil
}

// modelSuffix says whether the agents' lists name models with their
// providers' after them, all but the names the user gave (#92), or none
// (#335), or sets it.
func modelSuffix(args []string) error {
	if len(args) == 0 {
		switch provider.SuffixMode() {
		case provider.SuffixOff:
			fmt.Println("off", muted.Render("· agents' lists name a model alone, \"Sol\" · magpie model suffix on|own"))
		case provider.SuffixOwn:
			fmt.Println("own", muted.Render("· a name you gave a model just as you wrote it, \"Opus 5.5\"; the others \"Sol · OpenAI\" · magpie model suffix on|off"))
		default:
			fmt.Println("on", muted.Render("· agents' lists name a model with its provider, \"Sol · OpenAI\" · magpie model suffix own|off"))
		}
		return nil
	}
	mode := provider.SuffixOn
	switch strings.ToLower(args[0]) {
	case "on", "yes", "true":
	case "own", "mine":
		mode = provider.SuffixOwn
	case "off", "no", "false":
		mode = provider.SuffixOff
	default:
		return fmt.Errorf("magpie model suffix on|own|off, not %q", args[0])
	}
	if err := provider.SetSuffixMode(mode); err != nil {
		return err
	}
	switch mode {
	case provider.SuffixOff:
		fmt.Println(green.Render("✓"), "agents' lists name each model alone", muted.Render("· two that would read the same keep their provider's"))
	case provider.SuffixOwn:
		fmt.Println(green.Render("✓"), "agents' lists name a model you named just as you wrote it", muted.Render("· the others with their provider"))
	default:
		fmt.Println(green.Render("✓"), "agents' lists name each model with its provider again")
	}
	return nil
}

// modelCompact says whether long conversations are compacted at the
// working window (settings.WorkingWindow) or the size the user gave, or
// sets it: off is the app's Full window, every model run to its whole
// window; a size is compacting there (#876).
func modelCompact(args []string) error {
	if len(args) == 0 {
		if s := settings.Load(); s.FullContext {
			fmt.Println("off", muted.Render("· Codex and Claude Code run a conversation to the model's whole window · magpie model compact on"))
		} else {
			fmt.Println("on", muted.Render(fmt.Sprintf("· Codex and Claude Code compact at %dK on a longer window; in Claude Code a Claude model runs to its own · magpie model compact off", s.Compact()/1000)))
		}
		return nil
	}
	var full bool
	switch strings.ToLower(args[0]) {
	case "on", "yes", "true":
	case "off", "no", "false", "full":
		full = true
	default:
		n, err := provider.ParseTokens(args[0])
		if err != nil || n <= 0 {
			return fmt.Errorf("magpie model compact on|off|<size such as 500k>, not %q", args[0])
		}
		if err := provider.SetCompactAt(n); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), fmt.Sprintf("Codex and Claude Code compact at %dK on a longer window", settings.Load().Compact()/1000))
		return nil
	}
	if err := provider.SetFullContext(full); err != nil {
		return err
	}
	if full {
		fmt.Println(green.Render("✓"), "Codex and Claude Code run a conversation to the model's whole window", muted.Render("· every turn sends all of it"))
	} else {
		fmt.Println(green.Render("✓"), fmt.Sprintf("Codex and Claude Code compact at %dK again", settings.Load().Compact()/1000))
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

// modelWires lists the names the user gave the vendors their models are asked
// for, and which provider and model each is for. It is `magpie model prices`
// for the wire names: those are what a provider is asked for, and no other
// command says what is kept for a provider as a whole, so a name given for
// every model of one was written with no way to read it back.
//
// A name kept for a provider that has since been deleted is listed like any
// other: the name outlives the provider it was given for, and is in force for
// whichever provider takes that id next, so it is read off the file rather
// than off a provider that may not be there.
//
// A name that is only whitespace is no name and is not listed, as it is
// nowhere else a name is read: UpstreamNames leaves it out and a request
// under it goes out by the name magpie knows the model by. Listing it would
// print a line with nothing on it for a name no vendor is asked for, which
// is the one thing a listing read back off the file must not say.
func modelWires() error {
	wires := settings.Load().ModelWires
	rows := make([][2]string, 0, len(wires))
	w := 0
	for _, k := range slices.Sorted(maps.Keys(wires)) {
		if n := strings.TrimSpace(wires[k]); n != "" {
			rows = append(rows, [2]string{k, n})
			w = max(w, len(k))
		}
	}
	if len(rows) == 0 {
		fmt.Println(muted.Render("no vendor is asked for a model by another name yet · magpie model wire <provider/model> <name>"))
		return nil
	}
	for _, r := range rows {
		fmt.Println("  " + pad(r[0], w) + "  " + r[1])
	}
	return nil
}

func modelContext(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", modelUsage)
	}
	p, model, err := modelRef(args[0])
	if err != nil {
		return err
	}
	id := p.ID + "/" + model
	rest := args[1:]
	if len(rest) == 0 {
		if model == "*" {
			// the wildcard is not a model of its own: it is the one value
			// given to every model of the provider
			n := p.Contexts["*"]
			fmt.Println(bold.Render(tokenCount(n)), muted.Render("· "+id+" · every model of "+p.ID))
			if n > 0 {
				fmt.Println(faint.Render("  · what you said every model of this provider takes, unless that model has a window of its own"))
			} else {
				fmt.Println(faint.Render("  · what the vendor's list and models.dev say · magpie model context " + typedRef(id) + " <tokens> to say one for all of them"))
			}
			return nil
		}
		e, ok := provider.ServedEntryOf(id)
		if !ok {
			fmt.Println(muted.Render(id), faint.Render("· is not a model this provider serves"))
			return nil
		}
		fmt.Println(bold.Render(tokenCount(e.Context)), muted.Render("· "+id))
		switch {
		case p.Contexts[model] > 0 && p.Contexts["*"] > 0:
			// the model's own window is what it takes, and --reset takes
			// it to the one its provider gives every model, not to the
			// vendor's list
			fmt.Println(faint.Render("  · what you said it takes · --reset goes back to the one you set for every model of this provider"))
		case p.Contexts[model] > 0:
			fmt.Println(faint.Render("  · what you said it takes · --reset goes back to the vendor's list"))
		case p.Contexts["*"] > 0:
			fmt.Println(faint.Render("  · what you set for every model of this provider"))
		default:
			fmt.Println(faint.Render("  · what the vendor's list and models.dev say · magpie model context " + typedRef(id) + " <tokens> to change it"))
		}
		return nil
	}
	if isReset(rest) {
		dropped, err := provider.DropContext(*p, model)
		if err != nil {
			return err
		}
		// a window that was never set is one a --reset did not take off,
		// and a ✓ over it says the model had one; where what the model
		// takes now is the provider's own every-model window, that is the
		// command the user is after
		if !dropped {
			if p.Contexts["*"] > 0 {
				return fmt.Errorf("%s has no window of its own; what every model of this provider takes is what you set for %s, which %s takes away",
					id, p.ID+"/*", resetCommand("context", p.ID+"/*"))
			}
			if _, served := provider.ServedEntryOf(id); served {
				return fmt.Errorf("%s has no window of its own to reset — magpie model context %s says what it takes now", id, typedRef(id))
			}
			return fmt.Errorf("%s has no window of its own to reset", id)
		}
		fmt.Println(green.Render("✓"), id, muted.Render("has no window of your own · magpie model context "+typedRef(id)+" says what it takes now"))
		return nil
	}
	n, err := parseTokens(rest[0])
	if err != nil {
		return fmt.Errorf("a window is a number of tokens, like 200000 or 1m, not %q", rest[0])
	}
	if n == 0 {
		return fmt.Errorf("0 tokens is no window at all — %s takes the one you set off %s",
			resetCommand("context", id), resetTakesOff(p, model))
	}
	if err := provider.SetContext(*p, model, n); err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), id, muted.Render("takes"), bold.Render(tokenCount(n)))
	return nil
}

func modelOutput(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", modelUsage)
	}
	rest := args[1:]
	// A --reset is the one thing here that does not go looking for the
	// provider first. A reply limit outlives the provider it was set for,
	// and a provider can be deleted: the entry is then left in the settings
	// answering for a model nothing serves, and refusing the ref would
	// leave the user no way to take it off but the file itself.
	if isReset(rest) {
		key, dropped, err := provider.DropModelOutput(args[0])
		if err != nil {
			return err
		}
		// a limit that was never set is one a --reset did not take off,
		// and a ✓ over it says the model had one; where what the model
		// answers with now is the provider's own every-model limit, that is
		// the command the user is after
		if !dropped {
			wid, _, _ := strings.Cut(key, "/")
			if settings.Load().ModelOutputs[wid+"/*"] > 0 {
				return fmt.Errorf("%s has no reply limit of its own; what every model of this provider answers with is what you set for %s, which %s takes away",
					key, wid+"/*", resetCommand("output", wid+"/*"))
			}
			if _, served := provider.ServedEntryOf(key); served {
				return fmt.Errorf("%s has no reply limit of its own to reset — magpie model output %s says what it answers with now", key, typedRef(key))
			}
			return fmt.Errorf("%s has no reply limit of its own to reset", key)
		}
		// the pointer to the query is only worth giving where the query
		// reads the entry that was just cleared: with nothing serving that
		// id there is no limit left in force to report, and where another
		// provider answers to the spelling the query would read that
		// provider's model instead of the one taken off here
		line := "has no reply limit of your own"
		if clearedEntryIs(key) {
			line += " · magpie model output " + typedRef(key) + " says what it answers with now"
		}
		fmt.Println(green.Render("✓"), key, muted.Render(line))
		return nil
	}
	p, model, err := modelRef(args[0])
	if err != nil {
		return err
	}
	id := p.ID + "/" + model
	if len(rest) == 0 {
		s := settings.Load()
		if model == "*" {
			// the wildcard is not a model of its own: it is the one limit
			// given to every model of the provider
			n := s.ModelOutputs[id]
			fmt.Println(bold.Render(tokenCount(n)), muted.Render("· "+id+" · every model of "+p.ID))
			if n > 0 {
				fmt.Println(faint.Render("  · what you said every model of this provider answers with, unless that model has a limit of its own"))
			} else {
				fmt.Println(faint.Render("  · what the vendor's list and models.dev say · magpie model output " + typedRef(id) + " <tokens> to say one for all of them"))
			}
			return nil
		}
		e, ok := provider.ServedEntryOf(id)
		if !ok {
			fmt.Println(muted.Render(id), faint.Render("· is not a model this provider serves"))
			return nil
		}
		fmt.Println(bold.Render(tokenCount(e.Output)), muted.Render("· "+id))
		switch {
		case s.ModelOutputs[id] > 0 && s.ModelOutputs[p.ID+"/*"] > 0:
			// the model's own limit is what it answers with, and --reset
			// takes it to the one its provider gives every model, not to
			// the vendor's list
			fmt.Println(faint.Render("  · what you said it answers with · --reset goes back to the one you set for every model of this provider"))
		case s.ModelOutputs[id] > 0:
			fmt.Println(faint.Render("  · what you said it answers with · --reset goes back to the vendor's list"))
		case s.ModelOutputs[p.ID+"/*"] > 0:
			fmt.Println(faint.Render("  · what you set for every model of this provider"))
		default:
			fmt.Println(faint.Render("  · what the vendor's list and models.dev say · magpie model output " + typedRef(id) + " <tokens> to change it"))
		}
		return nil
	}
	n, err := parseTokens(rest[0])
	if err != nil {
		return fmt.Errorf("a reply limit is a number of tokens, like 128000 or 128k, not %q", rest[0])
	}
	if n == 0 {
		return fmt.Errorf("0 tokens is no reply limit at all — %s takes the one you set off %s",
			resetCommand("output", id), resetTakesOff(p, model))
	}
	if err := provider.SetModelOutput(id, n); err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), id, muted.Render("answers with at most"), bold.Render(tokenCount(n)))
	return nil
}

// typedRef is a provider/model spelled the way a command carrying it has to be
// typed: a wildcard in it is quoted, as a shell reads a bare * as a glob of
// the files in the directory the command runs in and never hands magpie the
// name. Every command magpie prints is one the user is meant to paste, so
// every one of them goes through here.
func typedRef(ref string) string {
	if strings.Contains(ref, "*") {
		return "'" + ref + "'"
	}
	return ref
}

// resetCommand is the command that takes a limit off, over the ref as typed.
func resetCommand(verb, ref string) string {
	return fmt.Sprintf("magpie model %s %s --reset", verb, typedRef(ref))
}

// resetTakesOff is what the command taking a limit off would take off. The
// wildcard is not a model of its own but the one value given to every model
// of the provider, and a message calling that "this model" sends the user
// after a limit that was never set.
func resetTakesOff(p *provider.Provider, model string) string {
	if model == "*" {
		return "every model of " + p.ID
	}
	return "this model"
}

// clearedEntryIs reports whether a query about key reads the entry a removal
// just cleared, as opposed to another provider's model of the same name:
// ServedEntryOf is asked by the id as given and answers with the entry a
// query finds, so they are one entry only where the id still names the
// provider the entry is stored under. A reply limit outlives the provider it
// was set for and nothing rewrites its key when that provider is deleted, so
// "b/vendor/x" is still where a limit is held while a provider of another id
// has since been given the display name "b" — and a query spelled that way
// reads the second provider's model, which this command never touched.
func clearedEntryIs(key string) bool {
	e, served := provider.ServedEntryOf(key)
	return served && e.ID == key
}

func tokenCount(n int) string {
	switch {
	case n <= 0:
		return "unknown"
	case n%1000000 == 0:
		return fmt.Sprintf("%dM", n/1000000)
	case n%1000 == 0:
		return fmt.Sprintf("%dk", n/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func modelWire(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", modelUsage)
	}
	// --reset comes before the provider is looked for, and works off the key
	// the name is stored at: a name outlives the provider it was given for,
	// and one whose provider has since been deleted is in force for
	// whichever provider takes that id next, its requests going out renamed
	// to a vendor that never heard of it. So it is the one name --reset
	// most has to reach, and the only one a reset that resolved the
	// provider first could not.
	//
	// What decides this is the key, not the lookup: a name under an id no
	// provider has now is in force for whoever takes that id, and so is one
	// under an id another provider is shown by — the provider such a ref
	// resolves to holding a name of its own, which is the one a removal
	// taken through it would take away instead. A ref that names its
	// provider by that provider's own id keeps to the path below, which
	// reports the removal as one.
	if len(args) > 1 && isReset(args[1:]) {
		pid, model, err := splitModelRef(args[0])
		if err != nil {
			return err
		}
		p, ferr := provider.Find(pid)
		if ferr != nil || (p.ID != pid && provider.HasUpstreamName(pid+"/"+model)) {
			dropped, err := provider.DropUpstreamName(pid + "/" + model)
			if err != nil {
				return err
			}
			if dropped {
				fmt.Println(green.Render("✓"), pid+"/"+model,
					muted.Render("no longer has a name of its own; a provider taking that id later is asked for it by the name magpie knows it by"))
				return nil
			}
			// no name is kept under that key, so the id is a mistyped one
			// and not a deleted provider's; the lookup below says which
			// provider is not there, as it does for every other command
		}
	}
	p, model, err := modelRef(args[0])
	if err != nil {
		return err
	}
	id := p.ID + "/" + model
	every, shown := "'"+p.ID+"/*'", p.ID+"/"+model // quoted: a shell reads a bare * as a glob
	if model == "*" {
		shown = every
	}
	names := p.UpstreamNames()
	_, own := names[model]
	_, one := names["*"]
	rest := args[1:]
	asked := provider.UpstreamName(*p, model)
	if len(rest) == 0 {
		fmt.Println(bold.Render(asked), muted.Render("· what "+p.Name+" is asked for, for "+shown))
		switch {
		case asked == model:
			fmt.Println(faint.Render("  · the same name magpie knows it by · magpie model wire " + shown + " <name> to change it"))
		case model == "*":
			fmt.Println(faint.Render("  · " + exampleOf(p, asked)))
			fmt.Println(faint.Render("  · --reset takes it away · magpie model wire " + p.ID + "/<model> <name> gives a single model one of its own"))
		case own:
			fmt.Println(faint.Render("  · magpie and the agents still know it as " + model + " · --reset asks for that again"))
		case one:
			fmt.Println(faint.Render("  · from the name given for every model of " + p.Name + " (" + every + " " + names["*"] + ")" +
				" · --reset on it leaves that one in force"))
		}
		return nil
	}
	name := strings.Join(rest, " ")
	if isReset(rest) {
		name = ""
	}
	// A blank name is a removal, --reset or a name of only whitespace
	// alike, and it is DropUpstreamName that makes it: it says whether
	// there was a name there to take away, which with the provider's own
	// list is what tells a model already asked for by the name magpie
	// knows it by from one the provider does not serve at all, for which
	// no name could have been kept. A ✓ over a key holding no name would
	// read as a name being gone that never was there, and under a
	// provider-wide name as the model's own name being the one in force,
	// which it is not. So the removal is asked for rather than made blind,
	// the way `magpie model price --reset` asks DropModelPrice.
	if strings.TrimSpace(name) == "" {
		return resetWireName(*p, model, id, shown)
	}
	if err := provider.SetUpstreamName(id, name); err != nil {
		return err
	}
	now := provider.UpstreamName(*p, model)
	if model == "*" {
		// the same precedence the reset above is honest about: a model's
		// own key wins over the provider's, so the models that keep one
		// are not asked for under this, and a ✓ claiming every model of
		// the provider is would be false wherever there is one. The
		// example stands on a model this reaches, so the ones it does
		// not are named here rather than left to be found one at a time.
		scope, kept := "every model of "+p.Name, ownKeptSaid(p)
		if kept != "" {
			scope += " with no name of its own"
		}
		fmt.Println(green.Render("✓"), shown, muted.Render("· "+scope+" is asked for as"), bold.Render(now))
		fmt.Println(faint.Render("  · " + exampleOf(p, now)))
		if kept != "" {
			fmt.Println(faint.Render("  · " + kept))
		}
		return nil
	}
	fmt.Println(green.Render("✓"), shown, muted.Render("is asked for as"), bold.Render(now),
		faint.Render("· magpie and the agents still know it as "+model))
	return nil
}

// resetWireName takes a name off under the key it is stored at and says what
// the model is asked for now, which is the name a wildcard entry leaves in
// force where one is in force at all.
//
// A key holding no name is an error rather than a tick, as it is for a price:
// a removal over a name that is not there has taken nothing away, and the
// tick would say the model is asked for by the name magpie knows it by — a
// claim nothing stands behind, and one that is false wherever a name given
// for the whole provider is in force. So there are three answers, as there
// are for a price: the model is not one the provider serves, so none could
// have been kept for it and none is in force; nothing was kept under that key
// of a model it does serve; or what is in force is the provider's own name
// and that is the one --reset reaches.
func resetWireName(p provider.Provider, model, id, shown string) error {
	dropped, err := provider.DropUpstreamName(id)
	if err != nil {
		return err
	}
	now := provider.UpstreamName(p, model)
	if !dropped {
		if model != "*" && !provider.ServesModel(p, model) {
			// none was kept and none could have been: a model the
			// provider has nothing like is never asked for by any name,
			// so naming the one in force would claim a request magpie
			// does not make
			return fmt.Errorf("%s is not a model of %s magpie knows of, so there is nothing to take away and none in force: a name is only kept for a model the provider serves, and this one is asked for by no name", id, p.Name)
		}
		every := "'" + p.ID + "/*'"
		if provider.HasUpstreamName(p.ID + "/*") {
			return fmt.Errorf("%s has no name of its own; what it is asked for is %s, the name given for every model of %s, which magpie model wire %s --reset takes away",
				id, now, p.Name, every)
		}
		if model == "*" {
			// the same precedence the success below is honest about: a
			// model's own key wins over the provider's, so one that keeps
			// a name is asked for under it whatever this pattern said, and
			// "every model …" is false the moment there is one — so the
			// models that keep a name are named here as they are there
			if kept := ownKeptSaid(&p); kept != "" {
				return fmt.Errorf("%s has no name of its own to reset, so there was nothing to take away: %s", id, kept)
			}
			return fmt.Errorf("%s has no name of its own to reset, so there was nothing to take away: every model of %s is already asked for by the name magpie knows it by", id, p.Name)
		}
		return fmt.Errorf("%s has no name of its own to reset, so there was nothing to take away: it is already asked for as %s", id, now)
	}
	if now != model {
		fmt.Println(green.Render("✓"), shown, muted.Render("has no name of its own again · it is still asked for as"),
			bold.Render(now), faint.Render("· that is the name given for '"+p.ID+"/*'"))
		return nil
	}
	if model == "*" {
		// the provider's own name is gone, but a model's own beats it
		// while it is there, so the models that keep one are still asked
		// for by it — and saying every model of the provider is asked
		// for by the name magpie knows it by is false the moment one
		// does, so they are named instead
		if kept := ownKeptSaid(&p); kept != "" {
			fmt.Println(green.Render("✓"), shown, muted.Render("has no name of its own again ·"), kept)
			return nil
		}
		fmt.Println(green.Render("✓"), shown, muted.Render("· every model of "+p.Name+" is asked for by the name magpie knows it by again"))
		return nil
	}
	fmt.Println(green.Render("✓"), shown, muted.Render("is asked for as"), bold.Render(model), muted.Render("again"))
	return nil
}

// ownKept are the models of a provider that keep a name of their own, in the
// order they are shown. A name given for every model of a provider does not
// reach them: a model's own key wins over the provider's, so each is asked
// for by its own whatever the provider's entry says, and a line about every
// model of that provider is true only where there is none to leave out of it.
// Naming them is what says which of the two is in force, which the example
// such a name is shown with cannot: that one stands on a model with no name
// of its own, or on the pattern alone, and names no model the provider's entry
// does not reach.
//
// A name kept for a model the provider does not serve is not one of these:
// magpie asks for no such a model, so nothing goes out under that name, and
// saying it did would claim a request that is not made.
func ownKept(p *provider.Provider) []string {
	names := p.UpstreamNames()
	kept := make([]string, 0, len(names))
	for _, m := range slices.Sorted(maps.Keys(names)) {
		if m != "*" && provider.ServesModel(*p, m) {
			kept = append(kept, m)
		}
	}
	return kept
}

// ownKeptSaid is ownKept said as a sentence, and is "" where there is none.
//
// A model is named with the name it goes out under, not with the one as it
// is stored: a star in a model's own name is that model, as it is in one
// given for every model of a provider (UpstreamNameIn), so it is the model
// magpie knows the model by that stands there. The whole claim of the
// sentence is that this is the name in force, so a star left standing in it
// would name an id no vendor is ever asked for.
func ownKeptSaid(p *provider.Provider) string {
	names, kept := p.UpstreamNames(), ownKept(p)
	askedFor := func(m string) string { return strings.ReplaceAll(names[m], "*", m) }
	switch len(kept) {
	case 0:
		return ""
	case 1:
		return kept[0] + " is asked for as " + askedFor(kept[0]) + ", a name of its own"
	}
	// a relay with a name of its own for every model it serves would
	// otherwise answer in a line as long as its list, so a few are named
	// and the rest counted
	const room = 3
	shown, more := kept, ""
	if len(shown) > room {
		shown, more = shown[:room], ", … and "+strconv.Itoa(len(kept)-room)+" more"
	}
	asked := make([]string, 0, len(shown))
	for _, m := range shown {
		asked = append(asked, askedFor(m))
	}
	return strings.Join(shown, ", ") + " are asked for as " + strings.Join(asked, ", ") + ", names of their own" + more
}

// exampleOf is what a name given for a provider's every model asks for: a
// model of that provider for it to stand on, and what that model goes out as.
// The star in such a name is that model, so it says what it stands for.
//
// The model is one of Exposed, the list the user is shown, and not of
// Available, which is every model the vendor is known to serve: a relay
// fronting one vendor under ids of its own shares that vendor's catalogue,
// so its first model is one nobody is offered, and an example standing on it
// names a model the user cannot pick and the name was not given for. Exposed
// is the user's own list where the file has one, so that is what it is read
// from there.
//
// It is the first of them with no name of its own, and that is what the
// provider's own name is really in force for: a model's own key wins over the
// provider's, so a model given a name of its own is asked for by that one
// whatever is given for every model, and an example standing on it would say
// a name no request goes out under, which reads as the one thing the
// provider's own entry does not reach.
//
// A provider with nothing to stand one on — no catalogue, nothing fetched,
// nothing in the file, or a name of its own for every model it shows — is
// left with the pattern alone, which is all a relay naming none of its own
// can be told.
func exampleOf(p *provider.Provider, name string) string {
	if !strings.Contains(name, "*") {
		return p.Name + " is asked for every one of its models as " + name
	}
	const star = " · * is the model magpie knows each of them by"
	names := p.UpstreamNames()
	model := ""
	for _, m := range p.Exposed() {
		if _, own := names[m.ID]; !own {
			model = m.ID
			break
		}
	}
	if model == "" {
		return p.Name + " is asked for every one of its models as " + name + star
	}
	return p.ID + "/" + model + " is asked for as " + strings.ReplaceAll(name, "*", model) + star
}
