package provider

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// What a model makes, as the user says it (shen-joe, #1399): a provider set
// up with a base URL tells magpie its image and video models by its own
// model list ("kind": "image" or "video") or, for images, by a model's name.
// A relay whose list says neither — most don't — leaves a video model
// making nothing, and an image model with an odd name drawing nothing. The
// user can say so of one model instead, kept in settings' ModelKinds by
// "<provider id>/<model id>" as a model's name is: an image model is drawn
// with at the images API under the provider's base, a video model asked at
// its videos API, as one its list marks is (gateway's Drawers and
// customVideomakers).
const (
	KindImage = "image"
	KindVideo = "video"
)

// MarksKinds is whether p's models can be said to make images or videos: a
// provider set up with a base URL, which magpie asks at the OpenAI-shaped
// images and videos APIs under it. A subscription's account, another magpie
// (whose own list says) and a decision API's can't.
func (p Provider) MarksKinds() bool {
	return p.Account == nil && !p.IsRemoteMagpie() && !p.DecideOnly() &&
		(p.Base(Chat) != "" || p.Base(Responses) != "")
}

// ModelKind is what the user said p's model makes: KindImage, KindVideo,
// or "" when they said nothing, or p can't be told (MarksKinds).
func (p Provider) ModelKind(model string) string {
	if !p.MarksKinds() {
		return ""
	}
	return modelKindIn(heldSettings(), p.ID, model)
}

func modelKindIn(s settings.Settings, pid, model string) string {
	switch k := s.ModelKinds[pid+"/"+model]; k {
	case KindImage, KindVideo:
		return k
	}
	return ""
}

// KindModels are the ids of p's models the user said make kind, those it
// still serves, sorted (a provider's id has no "/", so a model's may:
// OpenRouter's).
func (p Provider) KindModels(kind string) []string {
	if !p.MarksKinds() {
		return nil
	}
	var out []string
	prefix := p.ID + "/"
	for key, k := range heldSettings().ModelKinds {
		if model, ok := strings.CutPrefix(key, prefix); ok && k == kind && model != "" {
			out = append(out, model)
		}
	}
	if len(out) == 0 {
		return nil
	}
	slices.Sort(out)
	return slices.DeleteFunc(out, func(m string) bool { return !p.hasKindable(m) })
}

// hasKindable is whether p has model: one it serves to chat with, or one
// its fetched list has that it leaves out of those, as making images or
// videos.
func (p Provider) hasKindable(model string) bool {
	if p.serves(model) {
		return true
	}
	all, _ := catalog.LiveAll(p.ID)
	return slices.ContainsFunc(all, func(m catalog.Model) bool { return m.ID == model })
}

// SetModelKind says what a provider's model, spelt "provider/model", makes:
// KindImage, KindVideo, or "" to leave it to its vendor's list and name.
func SetModelKind(ref, kind string) error {
	_, err := setModelKind(ref, kind)
	return err
}

func setModelKind(ref, kind string) (bool, error) {
	p, model, err := splitRef(ref)
	if err != nil {
		return false, err
	}
	kind = strings.TrimSpace(kind)
	if kind != "" && kind != KindImage && kind != KindVideo {
		return false, fmt.Errorf("a model makes %q or %q, not %q", KindImage, KindVideo, kind)
	}
	if kind != "" {
		if !p.MarksKinds() {
			return false, errors.New(p.ID + " makes images and videos only as its own API does: none of its models can be said to make them")
		}
		if !p.hasKindable(model) {
			return false, fmt.Errorf("%s has no model %s (magpie provider %s lists them)", p.ID, model, p.ID)
		}
	}
	s := settings.Load()
	key := p.ID + "/" + model
	if s.ModelKinds[key] == kind {
		return false, nil
	}
	if kind == "" {
		delete(s.ModelKinds, key)
	} else {
		if s.ModelKinds == nil {
			s.ModelKinds = map[string]string{}
		}
		s.ModelKinds[key] = kind
	}
	if err := settings.Save(s); err != nil {
		return false, err
	}
	return true, nil
}

// DrawerHidden is whether the user unticked p's image model in its editor
// (HiddenDrawers): it is left out of the lists an image model is picked
// from and of Automatic's choice, and still draws when named.
func (p Provider) DrawerHidden(model string) bool {
	return slices.Contains(p.HiddenDrawers, model)
}

// VideomakerHidden is whether the user unticked p's video model in its
// editor (HiddenVideomakers): it is left out of the lists a video model is
// picked from and of Automatic's choice, and still makes videos when
// named.
func (p Provider) VideomakerHidden(model string) bool {
	return slices.Contains(p.HiddenVideomakers, model)
}
