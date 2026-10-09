package provider

import (
	"errors"
	"fmt"
	"maps"
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
	return touchedIf(setModelName(ref, name))
}

// touchedIf tells the agents of a change to the catalog that was made.
func touchedIf(changed bool, err error) error {
	if changed {
		catalog.Touched()
	}
	return err
}

func setModelName(ref, name string) (bool, error) {
	p, model, err := splitRef(ref)
	if err != nil {
		return false, err
	}
	name = strings.Join(strings.Fields(name), " ")
	if len([]rune(name)) > 80 {
		return false, errors.New("a model's name is at most 80 characters")
	}
	if name != "" && !p.serves(model) {
		return false, fmt.Errorf("%s has no model %s (magpie provider %s lists them)", p.ID, model, p.ID)
	}
	s := settings.Load()
	key := p.ID + "/" + model
	if s.ModelNames[key] == name {
		return false, nil
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
		return false, err
	}
	return true, nil
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
	var key string
	if model, every := strings.CutPrefix(strings.TrimSpace(id), settings.AnyProvider); every {
		// a model from any provider, or from none still there: no provider
		// is asked whether it serves it, since what it prices is mostly
		// usage whose provider is gone, or that never went through magpie
		key = AnyPriceKey(model)
	} else {
		pr, model, err := splitRef(id)
		if err != nil {
			return err
		}
		// a price for a model the provider does not serve is a price that
		// never applies and nothing later says so; the same refusal `magpie
		// model name` makes for the same id.
		if model != "*" && !pr.serves(model) {
			return fmt.Errorf("%s has no model %s (magpie provider %s lists them, and magpie model price '*/%s' prices it from any provider)", pr.ID, model, pr.ID, model)
		}
		// the entry is written under the id the provider has now, the way a
		// name is: a key under a display name is a price the provider is
		// never asked for, and nothing later would say so.
		key = pr.ID + "/" + model
	}
	s := settings.Load()
	m := settings.StatedPrice(*p)
	if err := settings.CheckModelPrice(key, m); err != nil {
		return err
	}
	if s.ModelPrices == nil {
		s.ModelPrices = map[string]settings.ModelPrice{}
	}
	s.ModelPrices[key] = m
	return settings.Save(s)
}

// AnyPriceKey is the key a price for a model from any provider is stored at:
// its id lower-cased, the way a session's bare model is looked up.
func AnyPriceKey(model string) string {
	return settings.AnyProvider + strings.ToLower(strings.TrimSpace(model))
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
	if pid+"/" == settings.AnyProvider {
		return AnyPriceKey(model), false
	}
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
	return touchedIf(setModelEfforts(ref, efforts))
}

func setModelEfforts(ref string, efforts []string) (bool, error) {
	p, model, err := splitRef(ref)
	if err != nil {
		return false, err
	}
	all := p.Known(model)
	var keep []string
	for _, e := range efforts {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if len(all) == 0 && !slices.Contains(Levels, e) {
			return false, fmt.Errorf("%q is not a reasoning level (they are %s)", e, strings.Join(Levels, ", "))
		}
		if len(all) > 0 && !slices.Contains(all, e) {
			return false, fmt.Errorf("%s/%s has no reasoning level %q (it has %s)", p.ID, model, e, strings.Join(all, ", "))
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
		return false, nil
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
		return false, err
	}
	return true, nil
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
	return touchedIf(setModelImage(ref, images))
}

func setModelImage(ref string, images *bool) (bool, error) {
	p, model, err := splitRef(ref)
	if err != nil {
		return false, err
	}
	if images != nil && !p.serves(model) {
		return false, fmt.Errorf("%s has no model %s (magpie provider %s lists them)", p.ID, model, p.ID)
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
			return false, nil
		}
		delete(s.ModelImages, key)
	} else {
		if cur, ok := s.ModelImages[key]; ok && cur == *images {
			return false, nil
		}
		if s.ModelImages == nil {
			s.ModelImages = map[string]bool{}
		}
		s.ModelImages[key] = *images
	}
	if err := settings.Save(s); err != nil {
		return false, err
	}
	return true, nil
}

// SetModelAPI says which one of a provider's APIs a model is asked on —
// chat, responses or anthropic — for a relay whose one key serves some of
// its models on one and others on another (01huadalang on Discord: 有的供应商
// 一个 api 里有很多模型但是不同协议); "" leaves it to the vendor's list and
// the URLs the provider has, as before. It must be an API the provider has
// a URL for: a preset's or a subscription's as well as a custom
// provider's, when it has more than one (01huadalang: OpenCode Go's
// deepseek-v4.1-flash answers on Responses too, and magpie asked it on
// chat alone).
func SetModelAPI(ref, api string) error {
	return touchedIf(setModelAPI(ref, api))
}

func setModelAPI(ref, api string) (bool, error) {
	p, model, err := splitRef(ref)
	if err != nil {
		return false, err
	}
	proto := Protocol(strings.TrimSpace(api))
	if proto != "" {
		if !slices.Contains(Protocols, proto) {
			return false, fmt.Errorf("a model's API is chat, responses or anthropic, not %q", api)
		}
		if p.Base(proto) == "" {
			return false, fmt.Errorf("%s has no %s URL to ask %s on: add it under More endpoints first", p.ID, proto, model)
		}
		if !p.serves(model) {
			return false, fmt.Errorf("%s has no model %s (magpie provider %s lists them)", p.ID, model, p.ID)
		}
	}
	s := settings.Load()
	key := p.ID + "/" + model
	if s.ModelAPIs[key] == string(proto) {
		return false, nil
	}
	if proto == "" {
		delete(s.ModelAPIs, key)
	} else {
		if s.ModelAPIs == nil {
			s.ModelAPIs = map[string]string{}
		}
		s.ModelAPIs[key] = string(proto)
	}
	if err := settings.Save(s); err != nil {
		return false, err
	}
	return true, nil
}

// ModelAPI is the API the user said p's model is asked on, when p has a
// URL for it still.
func (p Provider) ModelAPI(model string) (Protocol, bool) {
	if len(p.Speaks()) < 2 {
		return "", false
	}
	proto := Protocol(heldSettings().ModelAPIs[p.ID+"/"+model])
	if proto == "" || !slices.Contains(Protocols, proto) || p.Base(proto) == "" {
		return "", false
	}
	return proto, true
}

// ModelPref is what the provider editor's Names & levels changed of one
// model, sent with its Save: each part left nil is as it was. Name "" gives
// the model its own name back, Efforts [] all its levels, OwnImages the
// vendor's answer for whether it sees images, API "" every API the
// provider has for it, Same "" its own id to merge it with other
// vendors' by (see setModelSame), and OwnPrice its list price again in
// place of Price, what the user said it costs (SetModelPrice, #819).
type ModelPref struct {
	Name      *string        `json:"name,omitempty"`
	Efforts   *[]string      `json:"efforts,omitempty"`
	Images    *bool          `json:"images,omitempty"`
	OwnImages bool           `json:"ownImages,omitempty"`
	API       *string        `json:"api,omitempty"`
	Same      *string        `json:"same,omitempty"`
	Price     *catalog.Price `json:"price,omitempty"`
	OwnPrice  bool           `json:"ownPrice,omitempty"`
}

// SetModelPrefs makes the changes to a provider's models, by model id, as
// SetModelName, SetModelEfforts, SetModelImage, SetModelAPI and
// setModelSame do, and tells the agents
// once, after them all, rather than once a change. It stops at the first
// that fails, telling the agents of those made before it.
func SetModelPrefs(pid string, prefs map[string]ModelPref) error {
	changed := false
	set := func(c bool, err error) error {
		changed = changed || c
		return err
	}
	err := func() error {
		for _, model := range slices.Sorted(maps.Keys(prefs)) {
			m, ref := prefs[model], pid+"/"+model
			if m.Name != nil {
				if err := set(setModelName(ref, *m.Name)); err != nil {
					return err
				}
			}
			if m.Efforts != nil {
				if err := set(setModelEfforts(ref, *m.Efforts)); err != nil {
					return err
				}
			}
			if m.Images != nil || m.OwnImages {
				images := m.Images
				if m.OwnImages {
					images = nil
				}
				if err := set(setModelImage(ref, images)); err != nil {
					return err
				}
			}
			if m.API != nil {
				if err := set(setModelAPI(ref, *m.API)); err != nil {
					return err
				}
			}
			if m.Same != nil {
				if err := set(setModelSame(ref, *m.Same)); err != nil {
					return err
				}
			}
			// a price isn't what an agent picks a model by: the agents
			// aren't told of it
			if m.Price != nil || m.OwnPrice {
				price := m.Price
				if m.OwnPrice {
					price = nil
				}
				if err := SetModelPrice(ref, price); err != nil {
					return err
				}
			}
		}
		return nil
	}()
	return touchedIf(changed, err)
}

// setModelSame says which model a provider's model, spelt "provider/model",
// is the same as, for one a vendor names its own way (Volcengine Ark's
// dated ids, kyzhouxu on #583): the routing groups magpie finds merge it
// with that model from every other provider (autoGroups). The name is a
// model's id as any vendor spells it ("deepseek-v4.1-flash", or with a
// vendor's prefix); "" — or a name that is the model's own however spelt —
// merges it by its own id again.
func setModelSame(ref, same string) (bool, error) {
	p, model, err := splitRef(ref)
	if err != nil {
		return false, err
	}
	same = strings.TrimSpace(same)
	if len([]rune(same)) > 80 {
		return false, errors.New("the model a model is the same as is at most 80 characters")
	}
	if same != "" && Slug(sameModel(same)) == "" {
		return false, fmt.Errorf("%q names no model: give a model's id, as deepseek-v4.1-flash", same)
	}
	if sameModel(same) == sameModel(model) {
		same = "" // its own id: merged by it already
	}
	if same != "" && !p.serves(model) {
		return false, fmt.Errorf("%s has no model %s (magpie provider %s lists them)", p.ID, model, p.ID)
	}
	s := settings.Load()
	key := p.ID + "/" + model
	if s.ModelSameAs[key] == same {
		return false, nil
	}
	if same == "" {
		delete(s.ModelSameAs, key)
	} else {
		if s.ModelSameAs == nil {
			s.ModelSameAs = map[string]string{}
		}
		s.ModelSameAs[key] = same
	}
	if err := settings.Save(s); err != nil {
		return false, err
	}
	return true, nil
}

// ImageOverride is the user's answer for whether pid's model takes images.
func ImageOverride(pid, model string) (bool, bool) {
	return ImageOverrideIn(settings.Load(), pid, model)
}

// ImageOverrideIn is ImageOverride from settings s already read: a list of
// many models reads them once, not for each (lml on Discord, Windows).
func ImageOverrideIn(s settings.Settings, pid, model string) (bool, bool) {
	v, ok := s.ModelImages[pid+"/"+model]
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
	// (and so is the order they are listed in, the ones picked for an agent
	// shown only its picks, and the ones sent fast)
	hidden := false
	for _, m := range []map[string][]string{s.HiddenModels, s.PickedModels, s.OrderedModels, s.FastPicks} {
		for _, ids := range m {
			for i, id := range ids {
				if rest, ok := strings.CutPrefix(id, from+"/"); ok {
					ids[i], hidden = to+"/"+rest, true
				}
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

// Labels are how an agent's list names each of es, in order: as Label, or,
// when the user wants names plain (settings' PlainNames, #335), by the
// name alone — but for two or more the list would call the same, as a
// routing group found for a model is called with that model left in the
// list, which keep their provider's after it to tell them apart. With
// their own names plain (PlainOwnNames, #92), a name the user gave — a
// model's, or a routing group's they made (#868: "· routing group" was
// what an agent's narrow menu cut off) — is that name just as they wrote
// it, but for two the list would call the same, and the rest keep Label's.
func Labels(es []Entry) []string {
	out := make([]string, len(es))
	s := heldSettings()
	plain := s.PlainNames
	own := !plain && s.PlainOwnNames
	same := map[string]int{}
	if plain || own {
		for _, e := range es {
			same[strings.ToLower(e.Name)]++
		}
	}
	for i, e := range es {
		out[i] = e.Label()
		mine := own && (e.Default != "" || e.Named)
		if (plain || mine) && e.Name != "" && same[strings.ToLower(e.Name)] == 1 {
			out[i] = e.Name
		}
	}
	return out
}

// The ways the agents' lists name models (SuffixMode): every name with its
// provider's after it, as by default; all but the names the user gave
// models (#92); or none (#335).
const (
	SuffixOn  = "on"
	SuffixOwn = "own"
	SuffixOff = "off"
)

// SuffixMode is how the agents' lists name models: SuffixOn, SuffixOwn or
// SuffixOff.
func SuffixMode() string {
	s := settings.Load()
	switch {
	case s.PlainNames:
		return SuffixOff
	case s.PlainOwnNames:
		return SuffixOwn
	}
	return SuffixOn
}

// SetSuffixMode has the agents' model lists name models as mode says (see
// Labels), and the agents told when it changes.
func SetSuffixMode(mode string) error {
	plain, own := false, false
	switch mode {
	case SuffixOn:
	case SuffixOwn:
		own = true
	case SuffixOff:
		plain = true
	default:
		return fmt.Errorf("provider in model names: on, own or off, not %q", mode)
	}
	s := settings.Load()
	if s.PlainNames == plain && s.PlainOwnNames == own {
		return nil
	}
	s.PlainNames, s.PlainOwnNames = plain, own
	if err := settings.Save(s); err != nil {
		return err
	}
	catalog.Touched()
	return nil
}

// SetCodexAgentsV1 turns settings.CodexAgentsV1 on or off, and has Codex's
// lists written and asked for again.
func SetCodexAgentsV1(on bool) error {
	s := settings.Load()
	if s.CodexAgentsV1 == on {
		return nil
	}
	s.CodexAgentsV1 = on
	if err := settings.Save(s); err != nil {
		return err
	}
	catalog.Touched()
	return nil
}

// SetCodexAutoReview sets settings.CodexAutoReview, the model Codex's
// auto-review runs on ("" for Codex's own pick), and has Codex's lists
// written and asked for again.
func SetCodexAutoReview(id string) error {
	s := settings.Load()
	if s.CodexAutoReview == id {
		return nil
	}
	s.CodexAutoReview = id
	if err := settings.Save(s); err != nil {
		return err
	}
	catalog.Touched()
	return nil
}

// SetFullContext turns settings.FullContext on or off: Codex and Claude
// Code are told a model's whole window, or the working one, again.
func SetFullContext(on bool) error {
	s := settings.Load()
	if s.FullContext == on {
		return nil
	}
	s.FullContext = on
	if err := settings.Save(s); err != nil {
		return err
	}
	catalog.Touched()
	return nil
}

// SetPlainNames has the agents' model lists name models by their names
// alone (see Labels), or with their providers' again, and the agents told.
func SetPlainNames(on bool) error {
	if on {
		return SetSuffixMode(SuffixOff)
	}
	return SetSuffixMode(SuffixOn)
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

// ServesModel is whether the provider really serves model, listed or
// exposed: the question SetUpstreamName asks before it keeps a name for
// one, and the same one, since a name given for a model the provider does
// not serve is a request magpie would never send it. It is the answer a
// caller told to take a name off needs as well: a model the provider has
// nothing like is not asked for by any name, so reporting which one is in
// force for it would say magpie asks for a model it does not know.
func ServesModel(p Provider, model string) bool { return p.serves(model) }

// UpstreamName is the name to send a provider for one of its models: the one
// the user gave, for the model or for every model of that provider, else the
// name magpie knows the model by. Every "*" in the name the user gave is that
// model, wherever it stands: "vendor-c/*" for a provider's every model sends
// vendor-c/model-3 for model-3, so one name covers a relay that namespaces
// its models and still asks for each of them by its own, and "vendor/*/pro"
// sends vendor/model-3/pro. A name with no "*" in it is that one name for
// every model of the key, which is the right answer only for a relay that
// does serve them all alike.
//
// The two are deliberately different things. Everything a user or an agent
// sees — the catalog, the routing groups' membership, what a call is recorded
// and priced as — is in magpie's name for the model, so a rename upstream
// never renames a model here. Only the request that goes out carries the
// vendor's name, and what a reply says answered is compared with it, so a
// model asked for by the name the vendor serves it under is not read as
// another one.
func UpstreamName(p Provider, model string) string {
	return UpstreamNameIn(settings.Load().ModelWires, p.ID, model)
}

// UpstreamNameIn is UpstreamName over a settings' ModelWires already read,
// for a caller that walks a whole ledger of models in a row. A name that is
// only whitespace — one an older magpie stored, or one hand-edited into the
// file — is no name, as it is where one is given.
func UpstreamNameIn(wires map[string]string, pid, model string) string {
	n := strings.TrimSpace(wires[pid+"/"+model])
	if n == "" {
		n = strings.TrimSpace(wires[pid+"/*"])
	}
	if n == "" {
		return model
	}
	return strings.ReplaceAll(n, "*", model)
}

// UpstreamNameAs is UpstreamName for a request that goes out asking for sent,
// which is not always the model magpie knows: an Antigravity account is asked
// for the variant of it the effort picks, and that is the id the body carries.
// The two levels of the lookup are kept apart, since they are about different
// things. A name given for the model itself is the model's, whichever variant
// answers for it, so a "*" in it is that model and not the variant: the vendor
// is asked for the model by the name its owner gave, as it is everywhere else
// in magpie, and not for a "*" it never heard of. A "*" in the provider's own
// name is whatever the request is going out under, so one name for a provider
// asks for gemini-3.7-flash-high and gemini-3.7-flash-low each by its own,
// rather than for the one id whatever level the effort picked.
func UpstreamNameAs(p Provider, model, sent string) string {
	return UpstreamNameAsIn(settings.Load().ModelWires, p.ID, model, sent)
}

// UpstreamNameAsIn is UpstreamNameAs over a settings' ModelWires already
// read. A name that is only whitespace is no name, as it is in
// UpstreamNameIn: the lookup falls through to the level below it, and a
// request with no name at all goes out under the id it was built with.
func UpstreamNameAsIn(wires map[string]string, pid, model, sent string) string {
	if sent == model {
		return UpstreamNameIn(wires, pid, model)
	}
	if n := strings.TrimSpace(wires[pid+"/"+model]); n != "" {
		return strings.ReplaceAll(n, "*", model)
	}
	if n := strings.TrimSpace(wires[pid+"/*"]); n != "" {
		return strings.ReplaceAll(n, "*", sent)
	}
	return sent
}

// SentNameIn is the name a call on the provider's model goes out under at
// that effort: the name the vendor would be asked for now, which is the one
// magpie knows the model by unless the user gave one. It is UpstreamNameAs for
// the id that call asks for, which is not the model magpie knows everywhere:
// on an Antigravity account it is the variant of it that effort picks, so a
// name given for the provider asks for each level by its own id, and a ledger
// reading a record back reads the vendor's reply against the name that
// really went out — the same one the gateway sent — rather than against the
// family it is one level of.
//
// The effort is the one the gateway sent, already fitted to the model's own
// levels, which is what a record keeps of it, so a level of the model's own
// is one of its variants and goes out as that one. An effort that is no
// level of the family names the same id the gateway asked for all the same:
// the level of the family's own nearest (AntigravitySentID), which is where
// the envelope went out.
//
// It is SentNameOnIn for the caller that has nothing but the id — a record in
// the ledger, which keeps the id the call went out on and nothing else. A
// provider that is an account is built with that account's own id as its own
// (googleProvider and the rest of the account providers), so the id says
// which account it was, and a provider of the user's own is no account's
// whatever its id is. A record's provider need not even be signed in now,
// which is why nothing here goes looking for the account behind the id: the
// id a call went out on is all a record has and all it can be read by.
func SentNameIn(wires map[string]string, pid, model, effort string) string {
	return SentNameOnIn(wires, pid, pid, model, effort)
}

// SentNameOnIn is SentNameIn for a caller holding the provider itself, and so
// the account behind it: the call goes out on that account, and it is the
// account the id the effort picks is a question about. A provider's id is
// that account's own id today (googleProvider and the rest of the account
// providers build each one with it), so passing the id for the account reads
// the same — but the two are one id by construction rather than by rule, and
// a provider of the user's own has no account at all whatever its id is. So
// the route, which is holding the provider, asks about the account and lets
// the gateway's dispatch (codeAssistID, on the same agent) be the one rule.
func SentNameOnIn(wires map[string]string, pid, account, model, effort string) string {
	return UpstreamNameAsIn(wires, pid, model, antigravitySentID(account, model, effort))
}

// antigravitySentID is the id a call on the model goes out under at that
// effort on an Antigravity account. Every other account is asked for the
// model magpie knows, and so is one of a model this account does not serve
// as a family of levels.
func antigravitySentID(account, model, effort string) string {
	if account != "antigravity" {
		return model
	}
	return AntigravitySentID(model, effort)
}

// AntigravitySentID is the id a request for model goes out under at that
// effort on an Antigravity account: the variant of the family the effort
// picks, at the level of that family nearest the effort asked for; with no
// effort asked for, the family's own default. One of the family's own
// variant ids — an id a pick or an agent's model list kept from before the
// families were one model — goes as it is when no effort is asked for, and
// at the level the effort picks otherwise, since the effort the client
// asks for wins, as it does for the family. Any other id goes as it is.
//
// The gateway sends the envelope under this and the ledger judges what the
// vendor answered against it, so there is one of it rather than one each:
// a reply naming the id a call went out under is that model answering, and
// read against the family instead every level of it reads as another
// model having swapped in, in the routing view and in the ledger alike.
func AntigravitySentID(model, effort string) string {
	if base, _, ok := AntigravityBase(model); ok {
		if effort == "" {
			return model
		}
		model = base
	}
	vs, ok := AntigravityVariants(model)
	if !ok {
		return model
	}
	if effort == "" {
		return vs[""]
	}
	var levels []string
	for _, l := range Levels {
		if vs[l] != "" {
			levels = append(levels, l)
		}
	}
	if id := vs[nearestEffort(effort, levels)]; id != "" {
		return id
	}
	return vs[""]
}

// nearestEffort is the level of levels nearest want in Levels' order, a tie
// going up, or want itself where the levels are none or hold it: an effort a
// family does not take is asked at the level of its own nearest, the rule
// the gateway fits an effort to a model's levels by on the way out
// (fitEffort), kept here where a ledger reading a record back can reach it
// too.
func nearestEffort(want string, levels []string) string {
	at := slices.Index(Levels, want)
	if len(levels) == 0 || at < 0 || slices.Contains(levels, want) {
		return want
	}
	best, dist := want, len(Levels)
	for _, l := range levels {
		i := slices.Index(Levels, l)
		if i < 0 || l == "none" {
			continue
		}
		d := i - at
		if d < 0 {
			d = -d
		}
		if d < dist || d == dist && i > at {
			best, dist = l, d
		}
	}
	return best
}

// SetUpstreamName is the name to send a provider for one of its models; an
// empty name takes the user's away and sends the one magpie knows. A name is
// only given for a model the provider really serves: a relay lists the model
// under the id magpie knows it by and asks for it under the vendor's own, and
// one it does not list is a request magpie would never send, so naming it is
// refused here, as a price and a limit are for the same id. A "*" stands
// for every model of the provider rather than for one, and a removal is
// exempt, so an entry left for a model that has since gone can still be
// taken away.
//
// A provider magpie asks through an agent's own backend has no model field
// to put a name in — the request goes out carrying the id the agent knows —
// so a name given for one of its models would never be sent (asksOwnBackend).
//
// The ref is taken as the provider it names, by its id now, and the name is
// kept under that id: a ref is as likely to be spelled with an id the
// provider had before a rename (Was) or with the name it is shown by, and
// every lookup of a wire name goes by the id the provider has (UpstreamName).
// A name under any other spelling of the provider is one no lookup will ever
// match — a name the provider is never asked for, and a removal that takes
// away nothing.
//
// The key is checked here, where it is given, and not as the whole settings
// are written: a hand-edited key no lookup will ever match is a no-op of the
// user's own making, and must not keep every other setting from being saved.
//
// Kept in settings, and saved without telling the agents, like a price: which
// model an agent picks, and what it is called, have not changed — only the
// name the request goes out under.
//
// A removal goes to DropUpstreamName, which works off the key rather than
// off this provider, so that taking a name away is the same operation
// whether or not the provider it was given for is still there.
//
// A blank name is a removal, and whether there was a name there to take away
// is DropUpstreamName's to say, not this one's: a caller with something of
// its own to say about a name that was not there — `magpie model wire --
// reset`, which tells a mistyped id from a model already asked for by the
// name magpie knows it by — asks DropUpstreamName for the removal itself,
// the way `magpie model price --reset` asks DropModelPrice. A caller that
// only wants the model left as it is has nothing to tell the user either
// way, which is why a blank name is an error and not a second answer.
func SetUpstreamName(ref, name string) error {
	p, model, err := splitRef(ref)
	if err != nil {
		return err
	}
	key := p.ID + "/" + model
	if err := settings.CheckModelKey("wire name", key); err != nil {
		return err
	}
	if name = strings.TrimSpace(name); name == "" {
		_, err := DropUpstreamName(key)
		return err
	}
	if model != "*" && !p.serves(model) {
		return fmt.Errorf("%s has no model %s (magpie provider %s lists them)", p.ID, model, p.ID)
	}
	if p.asksOwnBackend() {
		return fmt.Errorf("%s is %s's own account: magpie asks it for the model by the id it knows, so there is no upstream name to give it", p.Name, p.Account.Agent)
	}
	s := settings.Load()
	if s.ModelWires == nil {
		s.ModelWires = map[string]string{}
	}
	s.ModelWires[key] = name
	return settings.Save(s)
}

// DropUpstreamName takes a name off under the key it is stored at, and says
// whether there was one to take. A name outlives the provider it was given
// for: that provider can be deleted, and the name is then in force for
// whichever provider takes that id next — its requests would go out renamed
// to a vendor that never heard of it, and nothing would say so — so a
// removal that resolved the provider first would leave the user no way to
// take away the one name doing that. A key holding no name is not an error:
// there is nothing there to take away, which is the state it is left in
// either way, and false says so to a caller that has something else to say.
//
// Which key a ref is taken at is wireKey's: the name comes off from where it
// is really kept, rather than from under whichever provider the ref names
// now.
func DropUpstreamName(key string) (bool, error) {
	key = strings.TrimPrefix(strings.TrimSpace(key), "magpie/")
	if strings.HasPrefix(key, GroupPrefix) {
		return false, errors.New("that is a routing group, not a provider's model")
	}
	pid, model, ok := strings.Cut(key, "/")
	if !ok || pid == "" || model == "" {
		return false, fmt.Errorf("name a model as provider/model, not %q", key)
	}
	s := settings.Load()
	key = wireKey(s.ModelWires, pid, model)
	if err := settings.CheckModelKey("wire name", key); err != nil {
		return false, err
	}
	if _, ok := s.ModelWires[key]; !ok {
		return false, nil
	}
	delete(s.ModelWires, key)
	return true, settings.Save(s)
}

// HasUpstreamName reports whether a name is kept under exactly the key it is
// given, the provider's part spelled as it is. No other spelling of the
// provider is tried: a caller deciding whether to remove a name by this key
// has to know that this key is the one a name is kept under, which is the
// one question wireKey's order is about.
//
// A key spelled as an agent spells one — with magpie/ in front of it, and
// the spaces a ref is typed with — is that same key, as it is for a removal
// (DropUpstreamName): a name kept under it is there whichever way it is
// spelled, and a check that answered for one spelling only would say a name
// was not kept where it is. The one caller has had that prefix taken off
// already (splitModelRef), so nothing depends on it yet.
func HasUpstreamName(key string) bool {
	key = strings.TrimPrefix(strings.TrimSpace(key), "magpie/")
	_, ok := settings.Load().ModelWires[key]
	return ok
}

// wireKey is the key a name for "pid/model", as the user spelled it, is kept
// under and taken off by, and the three spellings are tried in one order:
// the ref as it is given, the id the provider has or had, the name it is
// shown by. The first is where a name really is once the provider it was
// given for is gone — nothing rewrites the key, so it is still there under
// an id no provider has now, and it is the one a removal has to reach.
// Resolving that id through the name another provider is shown by instead
// would take away that provider's own name instead, and leave the one that
// is in force for whoever takes the id next in the file.
//
// The id a provider has, or had before a rename (Was), is where a name is
// written and read (SetUpstreamName, UpstreamName), and the name a provider
// is shown by is the last of the three, for a ref spelled that way and with
// nothing kept under either of the other two.
func wireKey(wires map[string]string, pid, model string) string {
	key := pid + "/" + model
	if _, ok := wires[key]; ok {
		return key
	}
	if p, ok := byIDOrWas(pid); ok {
		return p.ID + "/" + model
	}
	if p, err := Find(pid); err == nil {
		return p.ID + "/" + model
	}
	return key
}

// UpstreamNames are the names the user gave the provider's models, by model
// id, with the one given for all of them under "*" — each as it will be sent,
// trimmed, "*" and all, which is what a list of them shows. A name that is
// only whitespace is none, and is not listed.
func (p Provider) UpstreamNames() map[string]string {
	out := map[string]string{}
	for k, n := range settings.Load().ModelWires {
		if m, ok := strings.CutPrefix(k, p.ID+"/"); ok {
			if n = strings.TrimSpace(n); n != "" {
				out[m] = n
			}
		}
	}
	return out
}

// asksOwnBackend reports whether p is a signed-in account magpie reaches
// through that agent's own backend rather than through a vendor API: the
// request goes out carrying the model id the agent already knows, with no
// model field for magpie to name differently, so a wire name given for one
// of its models would never be sent. It is the account dispatch in
// internal/gateway that decides which agents those are, so this and that
// have to keep saying the same thing — a name accepted for an account the
// gateway asks its own way is a name never sent — and
// TestAsksOwnBackendAgreesWithTheGatewayDispatch reads the dispatch itself
// rather than a list written out a second time to keep them so.
//
// Codex is deliberately not among them: it is served by the ChatGPT backend
// (codex_backend.go), which has no provider id to key a name by.
func (p Provider) asksOwnBackend() bool {
	if p.Account == nil {
		return false
	}
	switch p.Account.Agent {
	case "claude", "cursor", "devin", "kiro", "zed", CommandCodePlanID:
		return true
	}
	// Qoder is one backend however many sites it is signed in to, and the
	// gateway's dispatch covers the whole family rather than the site the
	// account was made on, so the family is asked of rather than one id of
	// it: a name refused for the one subscription has to be refused for
	// the other too, which is as much magpie's own there.
	return slices.Contains(qoderAgents, p.Account.Agent)
}
