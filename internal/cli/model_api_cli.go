package cli

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// magpie model api: the one API a provider's model is asked on, the
// setting the provider editor's Model settings picks ("API this
// model is asked on"), for a relay whose one key serves its models on
// different APIs, set from a script (yew_fence_931206 on Discord). It is
// saved through provider.SetModelPrefs, the editor's Save, into settings'
// modelAPIs, which a running gateway reads for each request.

const apiUsage = `usage:
  magpie model api <provider/model>              the API the model is asked on, and the ones its provider has
  magpie model api <provider/model>... <api>     ask the model(s) on this API: chat, responses, anthropic,
                                                 gemini, or decide (System One: a decision model a routing
                                                 group asks as a turn begins); auto leaves it to the vendor's
                                                 list and every URL the provider has, as by default
  magpie model api <provider/model>... --reset   the same as auto
  magpie model apis [<provider>]                 the models you set an API for; with a provider, each of its
                                                 models and the API it is asked on

  It is the provider editor's "API this model is asked on", under Model settings, and is
  saved the same way: the app, a running gateway and the agents' model lists go by it at once.
  The provider needs a URL for the API (More endpoints in its editor), and at least two of
  chat, responses, anthropic and gemini, or a System One URL beside one of them, for there
  to be a choice. Several models in one command
  are all checked first: a model the provider doesn't list, or an API it has no URL for, refuses
  the whole command, and nothing is written.

  e.g. magpie model api relay/gpt-5.5 responses
       magpie model api relay/claude-opus-5-5 relay/claude-sonnet-5 anthropic
       magpie model api relay/gpt-5.5 --reset
       magpie model apis relay`

// apiNames are the APIs a model can be set to be asked on, in the
// editor's order.
var apiNames = []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic, provider.Gemini, provider.DecideAPI}

// apiLabels are the editor's names of them.
var apiLabels = map[provider.Protocol]string{
	provider.Chat: "Chat", provider.Responses: "Responses", provider.Anthropic: "Anthropic", provider.Gemini: "Gemini",
	provider.DecideAPI: "System One",
}

// apiChoices are the APIs one of p's models can be asked on alone, as the
// editor offers them (modelAPIs in app.js): those p has a URL for, when it
// has more than one, and System One when it has a URL there beside a
// conversation one. Vertex AI's model id says its one API.
func apiChoices(p *provider.Provider) []provider.Protocol {
	if p.IsVertex() {
		return nil
	}
	var out []provider.Protocol
	for _, a := range apiNames {
		if a != provider.DecideAPI && strings.TrimSpace(p.Base(a)) != "" {
			out = append(out, a)
		}
	}
	if len(out) < 2 {
		out = nil
	}
	// a System One URL beside a conversation one (provider.TakesDecideAPI)
	if p.TakesDecideAPI() {
		out = append(out, provider.DecideAPI)
	}
	return out
}

func apiList(as []provider.Protocol) string {
	s := make([]string, len(as))
	for i, a := range as {
		s[i] = string(a)
	}
	return strings.Join(s, ", ")
}

func modelAPICmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", apiUsage)
	}
	if slices.ContainsFunc(args, isHelp) {
		fmt.Println(apiUsage)
		return nil
	}
	if len(args) == 1 {
		return showModelAPI(args[0])
	}
	refs, val := args[:len(args)-1], strings.ToLower(strings.TrimSpace(args[len(args)-1]))
	if isReset([]string{val}) || val == provider.Auto {
		val = ""
	} else if !slices.Contains(apiNames, provider.Protocol(val)) {
		return fmt.Errorf("a model's API is chat, responses, anthropic, gemini, decide or auto, not %q\n\n%s", args[len(args)-1], apiUsage)
	}
	// every model is checked before any is written
	type one struct {
		p     *provider.Provider
		model string
	}
	var todo []one
	set := settings.Load().ModelAPIs
	for _, ref := range refs {
		if strings.HasPrefix(ref, "-") {
			return fmt.Errorf("%q is not a model: name each as provider/model, then the API\n\n%s", ref, apiUsage)
		}
		p, model, err := modelRef(ref)
		if err != nil {
			return err
		}
		_, had := set[p.ID+"/"+model]
		// a setting kept for a model the vendor no longer lists can still
		// be taken off
		if !provider.ServesModel(*p, model) && !(val == "" && had) {
			return fmt.Errorf("%s has no model %s (magpie provider models %s lists them)", p.ID, model, p.ID)
		}
		if val != "" {
			choices := apiChoices(p)
			if len(choices) == 0 {
				return fmt.Errorf("%s has one API only, so each of its models is asked on it: add another URL under More endpoints in its editor first", p.ID)
			}
			if !slices.Contains(choices, provider.Protocol(val)) {
				return fmt.Errorf("%s has no %s URL to ask %s on (it has %s): add it under More endpoints in its editor first", p.ID, val, model, apiList(choices))
			}
		}
		todo = append(todo, one{p, model})
	}
	// by provider, as the editor's Save sends them
	prefs := map[string]map[string]provider.ModelPref{}
	for _, o := range todo {
		if prefs[o.p.ID] == nil {
			prefs[o.p.ID] = map[string]provider.ModelPref{}
		}
		v := val
		prefs[o.p.ID][o.model] = provider.ModelPref{API: &v}
	}
	for _, pid := range slices.Sorted(maps.Keys(prefs)) {
		if err := provider.SetModelPrefs(pid, prefs[pid]); err != nil {
			return err
		}
	}
	for _, o := range todo {
		ref := o.p.ID + "/" + o.model
		before := set[ref]
		switch {
		case before == val && val == "":
			fmt.Println(muted.Render(ref + " is asked as its vendor's list says already (auto)"))
		case before == val:
			fmt.Println(muted.Render(ref + " is asked on " + apiLabels[provider.Protocol(val)] + " already"))
		case val == "":
			fmt.Println(green.Render("✓"), ref, muted.Render("· auto: asked as its vendor's list says, on the URLs "+o.p.ID+" has"))
		default:
			fmt.Println(green.Render("✓"), ref, muted.Render("· asked on "+apiLabels[provider.Protocol(val)]))
		}
	}
	return nil
}

// showModelAPI says what one model is asked on.
func showModelAPI(ref string) error {
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("name the model first, as provider/model, not %q\n\n%s", ref, apiUsage)
	}
	p, model, err := modelRef(ref)
	if err != nil {
		return err
	}
	saved, had := settings.Shared().ModelAPIs[p.ID+"/"+model]
	if !provider.ServesModel(*p, model) && !had {
		return fmt.Errorf("%s has no model %s (magpie provider models %s lists them)", p.ID, model, p.ID)
	}
	choices := apiChoices(p)
	id := p.ID + "/" + model
	if api, ok := p.ModelAPI(model); ok {
		fmt.Println(bold.Render(id), muted.Render("· asked on "+apiLabels[api]+" · set by you"))
	} else {
		fmt.Println(bold.Render(id), muted.Render("· auto"+listedNote(p, model)))
		if had {
			fmt.Println(muted.Render("  your " + saved + " is not in force: " + p.ID + " has no " + saved + " URL now, or only one API"))
		}
	}
	if len(choices) == 0 {
		fmt.Println(faint.Render("  · " + p.ID + " has one API only, so there is nothing to pick"))
		return nil
	}
	fmt.Println(faint.Render("  · " + p.ID + " has " + apiList(choices) + " · magpie model api " + id + " <api> to pick one"))
	return nil
}

// listedNote is what auto means for p's model: the APIs its vendor's list
// says it is served on, when it says.
func listedNote(p *provider.Provider, model string) string {
	var on []provider.Protocol
	for _, a := range p.ListedAPIs(model) {
		if slices.Contains(apiNames, a) {
			on = append(on, a)
		}
	}
	if len(on) == 0 {
		return ": each API the provider has, as it answers"
	}
	return ": as its vendor's list says, " + apiList(on)
}

// modelAPIs is `magpie model apis`: the models the user set an API for;
// with a provider, each of its models and the API it is asked on, for a
// script to read.
func modelAPIs(args []string) error {
	if len(args) > 0 && isHelp(args[0]) {
		fmt.Println(apiUsage)
		return nil
	}
	if len(args) > 1 || (len(args) == 1 && strings.HasPrefix(args[0], "-")) {
		return fmt.Errorf("%s", apiUsage)
	}
	set := settings.Load().ModelAPIs
	if len(args) == 1 {
		p, err := provider.Find(strings.TrimSuffix(args[0], "/"))
		if err != nil {
			return err
		}
		var models []string
		for _, m := range p.Available() {
			if !slices.Contains(models, m.ID) {
				models = append(models, m.ID)
			}
		}
		for k := range set {
			if model, ok := strings.CutPrefix(k, p.ID+"/"); ok && !slices.Contains(models, model) {
				models = append(models, model)
			}
		}
		if len(models) == 0 {
			fmt.Println(muted.Render(p.ID + " lists no models yet · magpie provider models " + p.ID))
			return nil
		}
		w := 0
		for _, m := range models {
			w = max(w, len(p.ID)+1+len(m))
		}
		for _, m := range models {
			api := provider.Auto
			if a, ok := p.ModelAPI(m); ok {
				api = string(a)
			}
			note := ""
			if s, ok := set[p.ID+"/"+m]; ok && api == provider.Auto {
				note = muted.Render("  (your " + s + " is not in force: no " + s + " URL now, or only one API)")
			}
			fmt.Printf("%-*s  %s%s\n", w, p.ID+"/"+m, api, note)
		}
		if len(apiChoices(p)) == 0 {
			fmt.Println(faint.Render("  · " + p.ID + " has one API only, so each model is asked on it"))
		}
		return nil
	}
	keys := slices.Sorted(maps.Keys(set))
	if len(keys) == 0 {
		fmt.Println(muted.Render("no model has an API of yours yet · magpie model api <provider/model> <api>"))
		return nil
	}
	w := 0
	for _, k := range keys {
		w = max(w, len(k))
	}
	for _, k := range keys {
		fmt.Printf("%-*s  %s\n", w, k, set[k])
	}
	return nil
}
