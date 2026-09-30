package provider

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// A model's name is the vendor's, or models.dev's, and agents are shown it
// with the provider after it (Claude Opus 5.5 · Claude Code). The user can
// give one of a provider's models a name of their own instead (Opus): it is
// kept in settings' ModelNames by "<provider id>/<model id>", apart from the
// vendor's list, so a Refresh, a restart or an upgrade leaves it, and the
// same model another provider serves keeps its own. Only the name changes:
// the model's id, and where requests for it go, stay as they were. The
// provider still goes after it (Opus · Claude Code), same as the vendor's
// own name, so a picker full of renamed models can still be told apart by
// vendor; it's left off only when the name given already says it.

// The user can also keep only some of the reasoning levels a model has
// (low, medium and high of its six): settings' ModelEfforts, kept the same
// way. The lists magpie hands out — the gateway's, and those it writes into
// the agents' files — offer only those, in the vendor's order; a request
// for another still reaches the vendor as it did. A model whose levels
// aren't known (one models.dev doesn't list, a custom provider's) can be
// given some of Levels the same way, which it is then taken to have.

// ModelName is the name the user gave a provider's model, if any.
func ModelName(pid, model string) (string, bool) {
	return modelNameIn(settings.Load().ModelNames, pid, model)
}

func modelNameIn(names map[string]string, pid, model string) (string, bool) {
	n, ok := names[pid+"/"+model]
	return n, ok && n != ""
}

// splitRef is "provider/model" as the provider it names, by its id now,
// and the model.
func splitRef(ref string) (*Provider, string, error) {
	r := strings.TrimPrefix(strings.TrimSpace(ref), "magpie/")
	if strings.HasPrefix(r, GroupPrefix) {
		return nil, "", errors.New("that is a routing group, not a provider's model")
	}
	pid, model, ok := strings.Cut(r, "/")
	if !ok || pid == "" || model == "" {
		return nil, "", fmt.Errorf("name a model as provider/model, not %q", ref)
	}
	p, err := Find(pid)
	if err != nil {
		return nil, "", err
	}
	return p, model, nil
}

// SetModelName names a provider's model, spelled "provider/model"; an empty
// name gives it back its own. The agents that keep the models in files of
// their own are told (catalog.Touched), as for any other change of the
// catalog.
func SetModelName(ref, name string) error {
	p, model, err := splitRef(ref)
	if err != nil {
		return err
	}
	name = strings.Join(strings.Fields(name), " ")
	if len([]rune(name)) > 80 {
		return errors.New("a model's name is at most 80 characters")
	}
	if name != "" && !p.serves(model) {
		return fmt.Errorf("%s has no model %s (magpie provider %s lists them)", p.ID, model, p.ID)
	}
	s := settings.Load()
	key := p.ID + "/" + model
	if s.ModelNames[key] == name {
		return nil
	}
	if name == "" {
		delete(s.ModelNames, key)
	} else {
		if s.ModelNames == nil {
			s.ModelNames = map[string]string{}
		}
		s.ModelNames[key] = name
	}
	if err := settings.Save(s); err != nil {
		return err
	}
	catalog.Touched()
	return nil
}

// SetModelPrice is what a provider's model costs the user, in USD per million
// tokens, kept in settings' ModelPrices the way a name is. A nil price takes
// the user's away, leaving the model at what its provider lists and only then
// at its maker's on models.dev; a price of zero is not that, but a model
// served at no cost. A price no vendor could charge is refused, naming the
// part that is wrong.
//
// Unlike the other model preferences this does not tell the agents. What a
// call costs is not what an agent picks a model by, and the model lists
// magpie keeps in the agents' own files are not its to rewrite over a number
// in a cost report.
func SetModelPrice(id string, p *catalog.Price) error {
	if p == nil {
		_, err := DropModelPrice(id)
		return err
	}
	pr, model, err := splitRef(id)
	if err != nil {
		return err
	}
	// a price for a model the provider does not serve is a price that never
	// applies and nothing later says so; the same refusal `magpie model
	// name` makes for the same id.
	if model != "*" && !pr.serves(model) {
		return fmt.Errorf("%s has no model %s (magpie provider %s lists them)", pr.ID, model, pr.ID)
	}
	// the entry is written under the id the provider has now, the way a name
	// is: a key under a display name is a price the provider is never asked
	// for, and nothing later would say so.
	key := pr.ID + "/" + model
	s := settings.Load()
	m := settings.ModelPrice{
		Input: new(p.Input), Output: new(p.Output),
		CacheRead: new(p.CacheRead), CacheWrite: new(p.CacheWrite),
	}
	if err := settings.CheckModelPrice(key, m); err != nil {
		return err
	}
	if s.ModelPrices == nil {
		s.ModelPrices = map[string]settings.ModelPrice{}
	}
	s.ModelPrices[key] = m
	return settings.Save(s)
}

// PriceKey is the key a price for pid's model is stored at, and whether the
// provider naming that key is still there at all.
//
// The spelling that was given comes first, because a price outlives the
// provider it was set for and nothing rewrites the key when that provider
// goes: "b/vendor/m" can still be where a price is held while a provider of
// some other id answers to "b" as its display name. Resolving that name
// first would take the second provider's price away instead — none is stored
// under it, so nothing would change, the caller would be told it had, and
// the price the user meant would stay in the file to be counted at. Only
// where nothing is stored under the spelling is the provider looked for, by
// the id it has or the one it was renamed from: that is where SetModelPrice
// keeps a price set through a display name, and the same resolution
// EffectivePrice reads it back under.
func PriceKey(pid, model string) (string, bool) {
	key := pid + "/" + model
	if _, under := settings.Load().ModelPrices[key]; under {
		_, there := byIDOrWas(pid)
		return key, there
	}
	if p, ok := byIDOrWas(pid); ok {
		return p.ID + "/" + model, true
	}
	// and last a display name, which is what a provider of the user's is
	// often asked by. Nothing writes a price under one — SetModelPrice
	// re-keys the way SetModelName does — but the spelling above is the only
	// thing that could have pointed at another provider's key, and a key
	// nothing is stored under reaches no price at all without this.
	if p, err := Find(pid); err == nil {
		return p.ID + "/" + model, true
	}
	return key, false
}

// DropModelPrice takes a user's price away under the key it is stored at,
// without asking whether the provider is still there, and reports whether
// there was one there to take away. A price outlives the provider it was set
// for: that provider can be deleted, and `magpie model prices` still lists
// the price and usage is still counted at it, so a removal that resolved the
// provider first would leave the user no way to take it away. Which of the
// keys a price is ever kept under this one is, is PriceKey's to say.
//
// A key holding no price is still no error — there is nothing there to take
// away, which is the state it is left in either way — but saying so is what
// tells a mistyped id from a price that was cleared: `magpie model price --
// reset` over a price still in the file would have the user believe it gone.
func DropModelPrice(key string) (bool, error) {
	key = strings.TrimPrefix(strings.TrimSpace(key), "magpie/")
	if strings.HasPrefix(key, GroupPrefix) {
		return false, errors.New("that is a routing group, not a provider's model")
	}
	pid, model, ok := strings.Cut(key, "/")
	if !ok || pid == "" || model == "" {
		return false, fmt.Errorf("name a model as provider/model, not %q", key)
	}
	key, _ = PriceKey(pid, model)
	s := settings.Load()
	if _, ok := s.ModelPrices[key]; !ok {
		return false, nil
	}
	delete(s.ModelPrices, key)
	return true, settings.Save(s)
}

// Levels are the reasoning levels a model whose own aren't known can be
// given.
var Levels = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// SetModelEfforts keeps only these of a provider's model's reasoning
// levels in the lists magpie hands out; none, or all it has, offers them
// all again. They must be levels the model has — or, for a model whose
// levels aren't known, any of Levels, which it is given; none takes them
// away.
func SetModelEfforts(ref string, efforts []string) error {
	p, model, err := splitRef(ref)
	if err != nil {
		return err
	}
	all := p.Known(model)
	var keep []string
	for _, e := range efforts {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if len(all) == 0 && !slices.Contains(Levels, e) {
			return fmt.Errorf("%q is not a reasoning level (they are %s)", e, strings.Join(Levels, ", "))
		}
		if len(all) > 0 && !slices.Contains(all, e) {
			return fmt.Errorf("%s/%s has no reasoning level %q (it has %s)", p.ID, model, e, strings.Join(all, ", "))
		}
		if !slices.Contains(keep, e) {
			keep = append(keep, e)
		}
	}
	keep = effortsKept(all, keep) // in the vendor's order
	if len(keep) == len(all) {
		keep = nil
	}
	s := settings.Load()
	key := p.ID + "/" + model
	if slices.Equal(s.ModelEfforts[key], keep) {
		return nil
	}
	if len(keep) == 0 {
		delete(s.ModelEfforts, key)
	} else {
		if s.ModelEfforts == nil {
			s.ModelEfforts = map[string][]string{}
		}
		s.ModelEfforts[key] = keep
	}
	if err := settings.Save(s); err != nil {
		return err
	}
	catalog.Touched()
	return nil
}

// ModelEfforts are the reasoning levels the user kept of the provider's
// models, by model id.
func (p Provider) ModelEfforts() map[string][]string {
	out := map[string][]string{}
	for k, es := range settings.Load().ModelEfforts {
		if m, ok := strings.CutPrefix(k, p.ID+"/"); ok && len(es) > 0 {
			out[m] = es
		}
	}
	return out
}

// effortsKept is all without the levels the user didn't keep, in its own
// order; all of them when none of those kept is among them any more (the
// vendor's list changed). A model with none known has those the user gave
// it.
func effortsKept(all, kept []string) []string {
	if len(kept) == 0 {
		return all
	}
	if len(all) == 0 {
		return slices.DeleteFunc(slices.Clone(Levels), func(e string) bool { return !slices.Contains(kept, e) })
	}
	out := slices.DeleteFunc(slices.Clone(all), func(e string) bool { return !slices.Contains(kept, e) })
	if len(out) == 0 {
		return all
	}
	return out
}

// SetModelImage says whether a provider's model takes images, in place
// of what its vendor's list says. nil gives that answer back.
func SetModelImage(ref string, images *bool) error {
	p, model, err := splitRef(ref)
	if err != nil {
		return err
	}
	if images != nil && !p.serves(model) {
		return fmt.Errorf("%s has no model %s (magpie provider %s lists them)", p.ID, model, p.ID)
	}
	if images != nil {
		if vendor, known := vendorSees(p, model); known && *images == vendor {
			images = nil
		}
	}
	s := settings.Load()
	key := p.ID + "/" + model
	if images == nil {
		if _, ok := s.ModelImages[key]; !ok {
			return nil
		}
		delete(s.ModelImages, key)
	} else {
		if cur, ok := s.ModelImages[key]; ok && cur == *images {
			return nil
		}
		if s.ModelImages == nil {
			s.ModelImages = map[string]bool{}
		}
		s.ModelImages[key] = *images
	}
	if err := settings.Save(s); err != nil {
		return err
	}
	catalog.Touched()
	return nil
}

// ImageOverride is the user's answer for whether pid's model takes images.
func ImageOverride(pid, model string) (bool, bool) {
	v, ok := settings.Load().ModelImages[pid+"/"+model]
	return v, ok
}

// ApplyImage is what magpie tells of model: the user's answer when they
// gave one, else images as the vendor's list has it (known is that list's
// explicit answer, nil when it didn't say).
func ApplyImage(pid, model string, images bool, known *bool) (bool, *bool) {
	if v, ok := ImageOverride(pid, model); ok {
		return v, &v
	}
	return images, known
}

// vendorSees is whether p's list says model takes images, and whether it
// said so at all.
func vendorSees(p *Provider, model string) (bool, bool) {
	for _, m := range p.Available() {
		if m.ID != model {
			continue
		}
		if m.ImageInput != nil {
			return *m.ImageInput, true
		}
		return m.Images || catalog.SeesImages(m.ID), false
	}
	return false, false
}

// renameModelPrefs moves the names, levels, image answers and everything
// else the user said of a provider's models to the id it has now. The
// settings walk their per-model maps themselves — settings.RenamePerModel,
// by the convention a Model* field of type map[string]X — and move each of
// them whether or not the ones before it moved anything, so a map added to
// them later is moved as well and there is nothing here to write for it.
func renameModelPrefs(s *settings.Settings, from, to string) bool {
	moved := s.RenamePerModel(from, to)
	// the models a user has hidden from a picker are keyed by provider as
	// well, and are not one of the per-model preference maps: they say
	// which models are shown, not what a model is called or costs
	hidden := false
	for _, ids := range s.HiddenModels {
		for i, id := range ids {
			if rest, ok := strings.CutPrefix(id, from+"/"); ok {
				ids[i], hidden = to+"/"+rest, true
			}
		}
	}
	return moved || hidden
}

// Label is how an agent's list names the entry: its name (the user's own,
// when they gave one, else the vendor's) with its provider's after it, or
// "routing group" for a group's — dropped only when a name of the user's
// own already carries the provider's, so it isn't said twice.
func (e Entry) Label() string {
	by := e.Provider.Name
	if e.Group != "" {
		by = "routing group"
	}
	if e.Default != "" && strings.Contains(strings.ToLower(e.Name), strings.ToLower(by)) {
		return e.Name
	}
	return e.Name + " · " + by
}

// ModelNames are the names the user gave the provider's models, by model id.
func (p Provider) ModelNames() map[string]string {
	out := map[string]string{}
	for k, n := range settings.Load().ModelNames {
		if m, ok := strings.CutPrefix(k, p.ID+"/"); ok && n != "" {
			out[m] = n
		}
	}
	return out
}

// EffortsOf is the reasoning levels one of a provider's listed models has,
// before any the user left out.
func EffortsOf(m catalog.Model) []string { return effortsOf(m) }

// serves reports whether model is one of the provider's, listed or exposed:
// a name given to a model it doesn't have would never be shown.
func (p Provider) serves(model string) bool {
	has := func(m catalog.Model) bool { return m.ID == model }
	return slices.ContainsFunc(p.Available(), has) || slices.ContainsFunc(p.Exposed(), has)
}
