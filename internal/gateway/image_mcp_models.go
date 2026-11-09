package gateway

import (
	"cmp"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// generate_image and generate_video take the model to use (XYenon on
// Discord): one of those set up for it, as provider/model, else the one
// Settings › Models › Images picks. The tool's schema lists those the caller may use
// (mediaModelsFor), and a model none of them is, is a tool error naming
// them, never a drawing with another. The model chosen is named to the
// images and videos APIs, so a gateway key's models hold it there as they
// hold one named at /v1/images (keyHolds, #882): that check is the API's,
// on every route to it.

// MediaModelMethod is the image MCP server's JSON-RPC method, not a tool,
// that the stdio Magpie Image server asks which model a call's model
// argument stands for: {"tool": …, "model": …} → {"model": provider/model},
// or an error naming those it may be.
const MediaModelMethod = "magpie/media_model"

// mediaEnumMost is the most models a tool's schema lists as its model's
// enum; more are described instead, and named in full by the error an
// unknown one gets.
const mediaEnumMost = 40

// mediaModels are the models generate_image (video false) or
// generate_video (true) may be asked for, as provider/model, the default
// first, and the default itself: "" when image generation is off or
// nothing makes them. Image models are each provider's OfferedDrawers (one
// the user unticked is left out, as Automatic leaves it), videos its
// OfferedVideomakers (one unticked left out the same way).
func mediaModels(video bool) (all []string, def string) {
	if video {
		def, _ = videomaker()
	} else {
		def = Drawer()
	}
	if def == "" {
		return nil, ""
	}
	all = []string{def}
	for _, p := range provider.All() {
		if !p.On() || p.DecideOnly() {
			continue
		}
		ms := OfferedVideomakers(p)
		if !video {
			ms = OfferedDrawers(p)
		}
		for _, m := range ms {
			if ref := p.ID + "/" + m.ID; !slices.Contains(all, ref) {
				all = append(all, ref)
			}
		}
	}
	return all, def
}

// ImageModel is a model magpie draws with, as an agent's own list of
// image models names it (Pi's, internal/agent/pi_extension.go).
type ImageModel struct {
	ID     string // provider/model, what /v1/images takes as model
	Name   string // the model's name · its provider's, as /v1/models labels it
	Images bool   // it takes images as input too
}

// ImageModels are the models generate_image may be asked for
// (mediaModels), the Settings › Models › Images default first, each with its label;
// none when image generation is off.
func ImageModels() []ImageModel {
	all, _ := mediaModels(false)
	if len(all) == 0 {
		return nil
	}
	known := map[string]ImageModel{}
	for _, p := range provider.All() {
		if !p.On() || p.DecideOnly() {
			continue
		}
		// every drawer, so a default the user unticked is labelled too
		for _, m := range Drawers(p) {
			ref := p.ID + "/" + m.ID
			known[ref] = ImageModel{ID: ref, Name: cmp.Or(m.Name, m.ID) + " · " + p.Name, Images: m.Images}
		}
	}
	out := make([]ImageModel, 0, len(all))
	for _, ref := range all {
		m, ok := known[ref]
		if !ok {
			m = ImageModel{ID: ref, Name: ref}
		}
		out = append(out, m)
	}
	return out
}

// mediaModelsFor is mediaModels less those r's gateway key may not use.
func mediaModelsFor(r *http.Request, video bool) (all, shown []string, def string) {
	all, def = mediaModels(video)
	who, held := keyListHolds(r)
	if !held {
		if !withGatewayKey(r) || !provider.GroupsBound() {
			return all, all, def
		}
		for _, ref := range all { // less a group bound to other keys
			if g, _, ok := provider.FindGroup(ref); !ok || !groupBarred(who, g) {
				shown = append(shown, ref)
			}
		}
		return all, shown, def
	}
	for _, ref := range all {
		if refAllowed(who, ref) {
			shown = append(shown, ref)
		}
	}
	return all, shown, def
}

// refAllowed says the key may use ref, a model or a routing group.
func refAllowed(who access.Identity, ref string) bool {
	if provider.IsGroupRef(ref) {
		g, ms, ok := provider.FindGroup(ref)
		return ok && groupAllowed(who, g, ms)
	}
	return allows(who, ref)
}

// mediaModelProp is the model argument's schema: the models shown, as an
// enum while they are few.
func mediaModelProp(video bool, shown []string, def string) map[string]any {
	what := "image"
	if video {
		what = "video"
	}
	d := "The " + what + " model to use, as provider/model."
	switch {
	case def == "":
		d += " Leave it out: none is set up in Magpie."
	case video:
		d += " Leave it out to use the video model set up in Magpie (now " + def + ")."
	default:
		d += " Leave it out to use the one set in Magpie (Settings → Models → Images → Image generation; now " + def + ")."
	}
	prop := map[string]any{"type": "string"}
	switch n := len(shown); {
	case n > mediaEnumMost:
		d += fmt.Sprintf(" One of the %d %s models set up in Magpie, such as %s; an unknown one is refused with the full list.", n, what, strings.Join(shown[:3], ", "))
	case n > 0:
		prop["enum"] = slices.Clone(shown)
	case def != "":
		d += " No other can be picked here."
	}
	prop["description"] = d
	return prop
}

// pickMediaModel is the model asked stands for: one of all, by its whole
// id or by a bare id only one of them has; a ChatGPT account's other GPT
// Image names as its one (ListedDrawer); or a newer model of a family a
// subscription serves before magpie lists it (drawsFamily,
// grokVideoFamily), as the APIs take it. Else an error that names those
// shown.
func pickMediaModel(tool, asked string, video bool, all, shown []string, def string) (string, error) {
	asked = strings.TrimSpace(asked)
	if !video && strings.Contains(asked, "/") {
		if l := ListedDrawer(asked); slices.Contains(all, l) {
			asked = l
		}
	}
	if slices.Contains(all, asked) {
		return asked, nil
	}
	if !strings.Contains(asked, "/") && asked != "" {
		var same []string
		for _, ref := range all {
			if _, id, _ := strings.Cut(ref, "/"); id == asked && !provider.IsGroupRef(ref) {
				same = append(same, ref)
			}
		}
		switch len(same) {
		case 1:
			return same[0], nil
		case 0:
		default:
			return "", fmt.Errorf("%s: more than one provider has %q; name it with its provider: %s", tool, asked, strings.Join(same, ", "))
		}
	}
	if p, id, ok := provider.Resolve(asked); ok && strings.Contains(asked, "/") && p.On() && !p.DecideOnly() {
		if video && drawsGrok(p) && strings.HasPrefix(id, grokVideoFamily) || !video && drawsFamily(p, id) {
			return asked, nil
		}
	}
	what, a := "image", "an image"
	if video {
		what, a = "video", "a video"
	}
	if def == "" {
		return "", fmt.Errorf("%s: no %s model is set up in Magpie", tool, what)
	}
	if len(shown) == 0 {
		return "", fmt.Errorf("%s: %q isn't %s model that can be used here; leave model out to use the default", tool, asked, a)
	}
	list := shown
	more := ""
	if len(list) > 100 {
		list, more = list[:100], ", and "+strconv.Itoa(len(shown)-100)+" more"
	}
	return "", fmt.Errorf("%s: %q isn't %s model that can be used here. Pick one of: %s%s; or leave model out to use the default (%s)", tool, asked, a, strings.Join(list, ", "), more, def)
}

// mediaModelFor is the model r's call of tool asked for (pickMediaModel),
// among those r's key may use.
func mediaModelFor(r *http.Request, tool, asked string) (string, error) {
	video := tool == GenerateVideoTool
	all, shown, def := mediaModelsFor(r, video)
	return pickMediaModel(tool, asked, video, all, shown, def)
}
