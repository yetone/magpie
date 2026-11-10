package provider

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/settings"
)

// A template is a routing group made of the user's own models for a need
// most users have, so a group is had in a click rather than by filling in
// the group editor: hard turns to the strongest model and easy ones to a
// cheap one, a cheap model with the strongest behind it, or the strongest
// models of different providers each behind the other (yetone, 10-10).
// Which model is strong and which is cheap is read off the models' list
// prices and release days, never a list of names kept here, so the
// templates follow the catalog as models come and go.

const (
	// TemplateSmart sends a turn the classifier judges hard to the
	// strongest model and one it judges easy to a cheap one; a compaction
	// goes to the cheap one too.
	TemplateSmart = "smart"
	// TemplateThrifty sends everything to a cheap model first, and to the
	// strongest when the agent asks for high reasoning or every cheap one
	// fails.
	TemplateThrifty = "thrifty"
	// TemplateSteady is the strongest model of each of up to three
	// providers, in order, so one vendor down or out of quota doesn't stop
	// the agent.
	TemplateSteady = "steady"
)

// TemplateKinds are the templates, in the order they are offered.
var TemplateKinds = []string{TemplateSmart, TemplateThrifty, TemplateSteady}

// The intents TemplateSmart's classifier chooses between. Hard comes
// first, so a turn the classifier can't place stays on the strong model,
// the group's first.
const (
	IntentHard = "a hard task: designing, debugging, a change across several files, or planning"
	IntentEasy = "an easy task: a quick question, a small edit, a lookup, or running a command"
)

// Template is one template as the user's models make it now.
type Template struct {
	Kind string `json:"kind"`
	// Group is the group it makes, not saved: no Members when it can't be
	// made of the models there are, Why saying what is missing
	Group Group  `json:"group"`
	Why   string `json:"why,omitempty"`
	// Need is what is missing, for the GUI to say in the user's language:
	// NeedProvider, NeedProviders (Of the only one) or NeedCheap (Of the
	// strong model)
	Need string `json:"need,omitempty"`
	Of   string `json:"of,omitempty"`
}

const (
	NeedProvider  = "provider"
	NeedProviders = "providers"
	NeedCheap     = "cheap"
)

// nonChat are words of a model id that isn't for an agent's chat: it
// draws, speaks, listens, embeds or searches rather than answering a turn.
var nonChat = lazyre.New(`embed|rerank|image|imagen|dall-e|tts|whisper|audio|realtime|transcribe|moderation|ocr|veo|sora|speech|research|guard|computer-use`)

// smallWords are words of a cheap model's id; tinyWords those of one too
// small to be trusted with an agent's turn when a larger cheap one is
// there. A size in billions ("8b") is tiny up to 15b. Words, not parts of
// one: "gemini" is no "mini".
var (
	smallWords = []string{"mini", "nano", "flash", "haiku", "lite", "small", "air", "turbo", "fast", "instant", "tiny", "highspeed"}
	tinyWords  = []string{"nano", "lite", "tiny"}
	sizeWord   = lazyre.New(`^(\d+(?:\.\d+)?)b$`)
)

// sizeOf is how small a model's id says it is: small, and tiny.
func sizeOf(model string) (small, tiny bool) {
	for _, w := range strings.FieldsFunc(strings.ToLower(model), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.'
	}) {
		if m := sizeWord.FindStringSubmatch(w); m != nil {
			var b float64
			fmt.Sscan(m[1], &b)
			small, tiny = small || b <= 72, tiny || b <= 15
			continue
		}
		small = small || slices.Contains(smallWords, w)
		tiny = tiny || slices.Contains(tinyWords, w)
	}
	return small, tiny
}

// frontierOut is the output price (USD per million tokens) a strong model
// costs at least: every vendor's flagship of 2025–26 (Opus, GPT-5, Gemini
// Pro, Grok 4, Sonnet) does, their small models don't. tooDear is past
// what an agent's model is: the "pro" models that think for minutes a
// turn, left out of the strong ones.
const (
	frontierOut = 10.0
	tooDear     = 100.0
)

// rated is a model of the catalog with what a template weighs it by.
type rated struct {
	e   Entry
	out float64 // its output price, 0 when no price is known
	// maker: a vendor among the presets makes it (MakerPrice), not one of
	// the hundreds a router lists from labs nobody has heard of
	maker bool
	small bool // its id says it is a small model
	tiny  bool
}

// templateModels are the models a template may be made of: every model
// served, not a group, that answers an agent's chat, with its price — the
// model's maker's list price when there is one, so a subscription's or a
// relay's copy weighs as the model does, else what the provider charges.
func templateModels() []rated {
	s := settings.Load()
	var out []rated
	for _, e := range Served() {
		if e.Group != "" || nonChat.MatchString(strings.ToLower(e.Model)) {
			continue
		}
		r := rated{e: e}
		if pr, ok := MakerPrice(e.Model); ok {
			r.out, r.maker = pr.Output, true
		} else if pr, ok := priceOf(s, e.Provider, true, e.Model, time.Time{}); ok {
			// without a rate window's rate (#2463): a template weighs how
			// strong a model is, which the hour it is made in doesn't change
			r.out = pr.Output
		}
		r.small, r.tiny = sizeOf(e.Model)
		out = append(out, r)
	}
	return out
}

// strongest are the models ranked strongest first: a known maker's
// before the rest, those priced as a flagship before the rest, a small one
// last; within each the newest, then the dearest. A model past tooDear is
// left out.
func strongest(ms []rated) []rated {
	var out []rated
	for _, m := range ms {
		if m.out <= tooDear {
			out = append(out, m)
		}
	}
	slices.SortStableFunc(out, func(a, b rated) int {
		return cmp.Or(
			-cmp.Compare(btoi(a.maker), btoi(b.maker)),
			-cmp.Compare(btoi(a.out >= frontierOut), btoi(b.out >= frontierOut)),
			cmp.Compare(btoi(a.small), btoi(b.small)),
			-cmp.Compare(a.e.Released, b.e.Released),
			-cmp.Compare(a.out, b.out))
	})
	return out
}

// cheapest are the models fit to be a cheap one beside strong, ranked
// best first: priced at a quarter of strong or less (or, with no prices
// to go by, a small model by its id), a known maker's first, the tiny ones
// last, then the newest and the dearest of them — the most able of the
// cheap.
func cheapest(ms []rated, strong rated) []rated {
	var out []rated
	for _, m := range ms {
		if m.e.ID == strong.e.ID {
			continue
		}
		if strong.out > 0 && m.out > 0 {
			if m.out <= strong.out/4 {
				out = append(out, m)
			}
		} else if m.small && !strong.small {
			out = append(out, m)
		}
	}
	slices.SortStableFunc(out, func(a, b rated) int {
		return cmp.Or(
			-cmp.Compare(btoi(a.maker), btoi(b.maker)),
			cmp.Compare(btoi(a.tiny), btoi(b.tiny)),
			-cmp.Compare(a.e.Released, b.e.Released),
			-cmp.Compare(a.out, b.out))
	})
	return out
}

// bareModel is a model's id without a router's vendor before it:
// "openai/gpt-6-luna" is "gpt-6-luna".
func bareModel(model string) string {
	return strings.ToLower(model[strings.LastIndex(model, "/")+1:])
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Templates are every template as the user's models make it now.
func Templates() []Template {
	ms, ds := templateModels(), OfferedDeciders(nil)
	out := make([]Template, 0, len(TemplateKinds))
	for _, k := range TemplateKinds {
		out = append(out, templateOf(k, ms, ds))
	}
	return out
}

// MakeTemplate is the template kind as the user's models make it now, or
// why it can't be made.
func MakeTemplate(kind string) (Template, error) {
	if !slices.Contains(TemplateKinds, kind) {
		return Template{}, fmt.Errorf("no template %q: one of %s", kind, strings.Join(TemplateKinds, ", "))
	}
	t := templateOf(kind, templateModels(), OfferedDeciders(nil))
	if t.Why != "" {
		return t, fmt.Errorf("%s", t.Why)
	}
	return t, nil
}

// TemplateNames are the templates' names in English, which the GUI says in
// the user's language.
var TemplateNames = map[string]string{
	TemplateSmart:   "Hard to strong, easy to cheap",
	TemplateThrifty: "Cheap first",
	TemplateSteady:  "Never stuck",
}

// TemplateIDs are the ids agents pick a template's group as, before a
// number when one is in use.
var TemplateIDs = map[string]string{TemplateSmart: "smart-split", TemplateThrifty: "cheap-first", TemplateSteady: "never-stuck"}

// templateOf is the template kind made of ms, its classifier a decider of
// ds when there is one.
func templateOf(kind string, ms []rated, ds []Entry) Template {
	t := Template{Kind: kind, Group: Group{Name: TemplateNames[kind], ID: TemplateIDs[kind], Routing: Ordered}}
	strong := strongest(ms)
	if len(strong) == 0 {
		t.Why, t.Need = "no model to make it of: add a provider first", NeedProvider
		return t
	}
	best := strong[0]
	switch kind {
	case TemplateSteady:
		// the strongest of each provider, up to three
		seen := map[string]bool{}
		for _, m := range strong {
			if len(t.Group.Members) == 3 {
				break
			}
			if !seen[m.e.Provider.ID] {
				seen[m.e.Provider.ID] = true
				t.Group.Members = append(t.Group.Members, m.e.ID)
			}
		}
		if len(t.Group.Members) < 2 {
			t.Group.Members = nil
			t.Why, t.Need, t.Of = "it needs models from two providers: "+best.e.Provider.Name+" is the only one", NeedProviders, best.e.Provider.Name
		}
		return t
	}
	cheap := cheapest(ms, best)
	if len(cheap) == 0 {
		t.Why, t.Need, t.Of = "no model cheap beside "+cmp.Or(best.e.Name, best.e.Model)+" to send the easy turns to", NeedCheap, cmp.Or(best.e.Name, best.e.Model)
		return t
	}
	low := cheap[0]
	// an image goes to the strong one when only it sees images
	images := best.e.Images && !low.e.Images
	switch kind {
	case TemplateSmart:
		t.Group.Members = []string{best.e.ID, low.e.ID}
		t.Group.Rules = []Rule{{Use: low.e.ID, Compact: true}}
		if images {
			t.Group.Rules = append(t.Group.Rules, Rule{Use: best.e.ID, Images: true})
		}
		t.Group.Rules = append(t.Group.Rules, Rule{Use: best.e.ID, Intent: IntentHard}, Rule{Use: low.e.ID, Intent: IntentEasy})
		t.Group.Classifier = classifierFor(ms, ds, low)
	case TemplateThrifty:
		t.Group.Members = []string{low.e.ID}
		// a second cheap one from another provider, before the dear one:
		// another model when there is one, as a model that fails on one
		// provider for what was asked likely fails on the next too
		other := func(m rated) bool { return m.e.Provider.ID != low.e.Provider.ID }
		i := slices.IndexFunc(cheap, func(m rated) bool { return other(m) && bareModel(m.e.Model) != bareModel(low.e.Model) })
		if i < 0 {
			i = slices.IndexFunc(cheap, other)
		}
		if i >= 0 {
			t.Group.Members = append(t.Group.Members, cheap[i].e.ID)
		}
		t.Group.Members = append(t.Group.Members, best.e.ID)
		t.Group.Rules = []Rule{{Use: best.e.ID, Effort: "high"}}
		if images {
			t.Group.Rules = append(t.Group.Rules, Rule{Use: best.e.ID, Images: true})
		}
	}
	return t
}

// classifierFor is the model TemplateSmart asks which intent a turn is: a
// decision API (Jev) when one is on, else the cheapest priced model that
// doesn't reason — quick, as the classifier is asked each turn — else the
// group's cheap one.
func classifierFor(ms []rated, ds []Entry, low rated) string {
	if len(ds) > 0 {
		return ds[0].ID
	}
	var pick *rated
	for i, m := range ms {
		if m.out > 0 && !m.e.Reasoning && !m.tiny && (pick == nil || m.out < pick.out) {
			pick = &ms[i]
		}
	}
	if pick != nil && pick.out <= low.out {
		return pick.e.ID
	}
	return low.e.ID
}

// AddTemplate saves the template kind as the user's models make it now, as
// a group of the user's named name (the template's own name when empty),
// numbered past a name or id in use, so it never replaces a group there
// is. It is listed last, as a group made in the editor is.
func AddTemplate(kind, name string) (Group, error) {
	t, err := MakeTemplate(kind)
	if err != nil {
		return Group{}, err
	}
	g := t.Group
	if name = strings.TrimSpace(name); name == "" {
		name = g.Name
	}
	g.Name, g.ID = freeGroupName(Groups(), name, g.ID)
	if err := SaveGroup(g); err != nil {
		return Group{}, err
	}
	for _, o := range Groups() {
		if o.ID == g.ID {
			return o, nil
		}
	}
	return g, nil
}
