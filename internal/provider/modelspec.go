package provider

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/settings"
)

// A model's specs — its context window, its reply limit, whether it takes
// images and which reasoning levels it has — come from the vendor's own
// list, else models.dev. A custom provider's model neither lists has none
// of them known, and the GUI shows it with no badges, the gateway routes
// it with no window and agents are told nothing of it (Dani Ariyanto on
// Discord: kenari/*:free, experiential-labs/deepseek-v4.1-flash-uncensored).
// `magpie model spec` says them all at once, over every one of those
// sources. Each part is kept where the setting of its own already keeps
// it, so there is one value per part, which every reader already goes by
// (entryFor and the helpers it uses): the window in the provider's own
// Contexts (SetContext, a routing input), the reply limit in settings'
// ModelOutputs, vision in ModelImages and the levels in ModelEfforts and
// ModelReasonless. None of them is touched by a catalog sync.

// SpecChange is what `magpie model spec` changes of one model. Each part
// left nil stays as it is.
type SpecChange struct {
	// Context and Output are numbers of tokens; 0 takes the user's away.
	Context, Output *int
	// Vision is "on", "off", or "auto" for the vendor's list's answer.
	Vision *string
	// Reasoning is the levels the model offers: a range ("low-high"), a
	// list ("low,medium,high"), "off" for no reasoning ask at all, or
	// "auto" for the levels its list gives.
	Reasoning *string
}

// Empty is whether c changes nothing.
func (c SpecChange) Empty() bool {
	return c.Context == nil && c.Output == nil && c.Vision == nil && c.Reasoning == nil
}

// Auto is the word that takes one part of a spec back to the vendor's list
// and models.dev.
const Auto = "auto"

// ParseVision is a typed vision answer: true or false, nil for auto.
func ParseVision(v string) (*bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "yes", "true", "1":
		return new(true), nil
	case "off", "no", "false", "0":
		return new(false), nil
	case Auto, "default", "reset":
		return nil, nil
	}
	return nil, fmt.Errorf("vision is on, off or auto, not %q", v)
}

// ParseLevels is a typed reasoning answer for p's model: the levels to keep
// in their order, and off for no reasoning ask. "auto" is no levels and not
// off. Levels are the model's own when its list gives them, else any of
// Levels; a range runs from one to the other in that order, both
// included.
func (p Provider) ParseLevels(model, v string) (levels []string, off bool, err error) {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "off", "no", "false":
		return nil, true, nil
	case Auto, "default", "reset":
		return nil, false, nil
	case "":
		return nil, false, errors.New("reasoning is a range of levels (low-high), a list (low,medium,high), off or auto")
	}
	order := p.Known(model)
	if len(order) == 0 {
		order = Levels
	}
	notLevel := func(l string) error {
		if len(p.Known(model)) > 0 {
			return fmt.Errorf("%s/%s has no reasoning level %q (it has %s)", p.ID, model, l, strings.Join(order, ", "))
		}
		return fmt.Errorf("%q is not a reasoning level (they are %s)", l, strings.Join(order, ", "))
	}
	for _, part := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '/' }) {
		from, to, isRange := strings.Cut(strings.ReplaceAll(part, "–", "-"), "-")
		a := slices.Index(order, from)
		if a < 0 {
			return nil, false, notLevel(from)
		}
		b := a
		if isRange {
			if b = slices.Index(order, to); b < 0 {
				return nil, false, notLevel(to)
			}
			if b < a {
				return nil, false, fmt.Errorf("the range %s runs backwards: levels go %s", part, strings.Join(order, ", "))
			}
		}
		for _, l := range order[a : b+1] {
			if !slices.Contains(levels, l) {
				levels = append(levels, l)
			}
		}
	}
	if len(levels) == 0 {
		return nil, false, errors.New("reasoning is a range of levels (low-high), a list (low,medium,high), off or auto")
	}
	return levels, false, nil
}

// SetModelSpec makes the changes c says to a provider's model, spelt
// "provider/model", or "provider/*" for every model of that provider (the
// window and the reply limit only, as `magpie model context` and `output`
// take it). Every part is checked before anything is written, so a bad
// one writes nothing; the agents are told once, after them all.
func SetModelSpec(ref string, c SpecChange) error {
	_, err := ChangeModelSpec(ref, c)
	return err
}

// ChangeModelSpec is SetModelSpec, saying whether anything changed: a
// spec the model goes by already (every reasoning level it has, a window
// it was given) saves nothing, which the caller says rather than "saved"
// (@czhhcz on X).
func ChangeModelSpec(ref string, c SpecChange) (bool, error) {
	var did bool
	err := setModelSpec(ref, c, &did)
	return did, err
}

func setModelSpec(ref string, c SpecChange, did *bool) error {
	if c.Empty() {
		return errors.New("say what to change: --context, --output, --vision or --reasoning")
	}
	r := strings.TrimPrefix(strings.TrimSpace(ref), "magpie/")
	if strings.HasPrefix(r, settings.AnyProvider) {
		// a window or a vision answer is one provider's serving of a
		// model, not the model's own: two relays serve one model at two
		// windows, and a price's "*/<model>" has no such reading here
		return fmt.Errorf("a spec is one provider's model: name it as provider/model, not %q", ref)
	}
	p, model, err := splitRef(ref)
	if err != nil {
		return err
	}
	every := model == "*"
	if every && (c.Vision != nil || c.Reasoning != nil) {
		return fmt.Errorf("vision and reasoning are set one model at a time, as a provider's models differ in them; %s takes --context and --output", p.ID+"/*")
	}
	for _, n := range []*int{c.Context, c.Output} {
		if n != nil && *n < 0 {
			return fmt.Errorf("a number of tokens is not %d", *n)
		}
	}
	setting := c.Context != nil && *c.Context > 0 || c.Output != nil && *c.Output > 0 ||
		c.Vision != nil && !isAuto(*c.Vision) || c.Reasoning != nil && !isAuto(*c.Reasoning)
	if setting && !every && !p.serves(model) {
		return fmt.Errorf("%s has no model %s (magpie provider %s lists them)", p.ID, model, p.ID)
	}
	key := p.ID + "/" + model
	if c.Output != nil {
		if err := settings.CheckModelKey("an output limit", key); err != nil {
			return err
		}
	}
	var images *bool
	if c.Vision != nil {
		if images, err = ParseVision(*c.Vision); err != nil {
			return err
		}
	}
	var levels []string
	var off bool
	if c.Reasoning != nil {
		if levels, off, err = p.ParseLevels(model, *c.Reasoning); err != nil {
			return err
		}
		if _, err := keptEfforts(p, model, levels); err != nil {
			return err
		}
	}

	// every part is good: write them
	changed, saved := false, false
	if c.Output != nil {
		ch, err := setOutput(key, *c.Output)
		if err != nil {
			return touchedIf(changed, err)
		}
		changed = changed || ch
	}
	if c.Vision != nil {
		ch, err := setModelImage(key, images)
		if err != nil {
			return touchedIf(changed, err)
		}
		changed = changed || ch
	}
	if c.Reasoning != nil {
		var ch bool
		if off {
			ch, err = setModelReasonless(key, true)
		} else {
			// levels given, or none for auto, also take back an "off"
			ch, err = setModelEfforts(key, levels)
		}
		if err != nil {
			return touchedIf(changed, err)
		}
		changed = changed || ch
	}
	if c.Context != nil {
		// the provider's Save tells the agents itself, after the settings
		// above are written
		if *c.Context > 0 && p.Contexts[model] != *c.Context {
			err = SetContext(*p, model, *c.Context)
			saved = err == nil
		} else if *c.Context == 0 {
			saved, err = DropContext(*p, model)
		}
		if err != nil {
			return touchedIf(changed, err)
		}
	}
	*did = changed || saved
	return touchedIf(changed && !saved, nil)
}

func isAuto(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case Auto, "default", "reset":
		return true
	}
	return false
}

// setOutput is SetModelOutput without telling the agents: n over 0 is the
// limit, 0 takes the user's away. It reports whether anything changed.
func setOutput(key string, n int) (bool, error) {
	s := settings.Load()
	if n == 0 {
		if _, ok := s.ModelOutputs[key]; !ok {
			return false, nil
		}
		delete(s.ModelOutputs, key)
	} else {
		if s.ModelOutputs[key] == n {
			return false, nil
		}
		if s.ModelOutputs == nil {
			s.ModelOutputs = map[string]int{}
		}
		s.ModelOutputs[key] = n
	}
	return true, settings.Save(s)
}

// ResetModelSpec takes every spec the user gave a provider's model away —
// its window, reply limit, vision answer and levels — leaving it to the
// vendor's list and models.dev again. "provider/*" takes away the window
// and reply limit given every model of the provider. It reports what it
// took away, by part ("context", "output", "vision", "reasoning"). A
// provider that has gone still has its settings' parts taken away, under
// the key as typed: a spec outlives the provider it was set for, and `magpie
// model specs` still lists it.
func ResetModelSpec(ref string) ([]string, error) {
	r := strings.TrimPrefix(strings.TrimSpace(ref), "magpie/")
	if IsGroupRef(r) {
		return nil, errors.New("that is a routing group, not a provider's model")
	}
	pid, model, ok := strings.Cut(r, "/")
	if !ok || pid == "" || model == "" {
		return nil, fmt.Errorf("name a model as provider/model, not %q", ref)
	}
	var p *Provider
	if found, err := Find(pid); err == nil {
		p, pid = found, found.ID
	}
	key := pid + "/" + model
	s := settings.Load()
	var gone []string
	if _, ok := s.ModelOutputs[key]; ok {
		delete(s.ModelOutputs, key)
		gone = append(gone, "output")
	}
	if _, ok := s.ModelImages[key]; ok {
		delete(s.ModelImages, key)
		gone = append(gone, "vision")
	}
	_, narrowed := s.ModelEfforts[key]
	if narrowed || s.ModelReasonless[key] {
		delete(s.ModelEfforts, key)
		delete(s.ModelReasonless, key)
		gone = append(gone, "reasoning")
	}
	if len(gone) > 0 {
		if err := settings.Save(s); err != nil {
			return nil, err
		}
	}
	if p != nil {
		dropped, err := DropContext(*p, model) // its Save tells the agents
		if err != nil {
			return gone, touchedIf(len(gone) > 0, err)
		}
		if dropped {
			return append([]string{"context"}, gone...), nil
		}
	}
	return gone, touchedIf(len(gone) > 0, nil)
}

// SpecPart is one part of a model's spec as it is in force: its value and
// where it came from — "model" for what the user set on it, "provider" for
// what they set on every model of its provider, "" for the vendor's list
// and models.dev.
type SpecPart struct {
	Value string
	From  string
}

// Spec is a model's spec as magpie goes by it: the entry the gateway routes
// and the GUI and agents are shown, with where each part came from.
type Spec struct {
	Context, Output, Vision, Reasoning SpecPart
	Entry                              Entry
}

// SpecOf is the spec of a model a provider serves, by "provider/model".
func SpecOf(ref string) (Spec, bool) {
	p, model, err := splitRef(ref)
	if err != nil || model == "*" {
		return Spec{}, false
	}
	e, ok := ServedEntryOf(p.ID + "/" + model)
	if !ok {
		return Spec{}, false
	}
	s := settings.Load()
	key := e.ID
	sp := Spec{Entry: e}
	sp.Context.Value = tokenWord(e.Context)
	switch {
	case p.Contexts[model] > 0:
		sp.Context.From = "model"
	case p.Contexts["*"] > 0:
		sp.Context.From = "provider"
	}
	sp.Output.Value = tokenWord(e.PublishedOutput())
	switch {
	case s.ModelOutputs[key] > 0:
		sp.Output.From = "model"
	case s.ModelOutputs[p.ID+"/*"] > 0:
		sp.Output.From = "provider"
	}
	switch {
	case e.Images:
		sp.Vision.Value = "on"
	case e.ImageInput != nil:
		sp.Vision.Value = "off"
	default:
		sp.Vision.Value = "unknown"
	}
	if _, ok := s.ModelImages[key]; ok {
		sp.Vision.From = "model"
	}
	switch {
	case e.Reasonless:
		sp.Reasoning.Value = "off"
	case len(e.Efforts) > 0:
		sp.Reasoning.Value = strings.Join(e.Efforts, ",")
	case e.Reasoning:
		sp.Reasoning.Value = "thinks, no levels"
	default:
		sp.Reasoning.Value = "unknown"
	}
	if _, ok := s.ModelEfforts[key]; ok || s.ModelReasonless[key] {
		sp.Reasoning.From = "model"
	}
	return sp, true
}

// SpecRow is one model the user gave a spec, as `magpie model specs` lists
// it: each part as they set it, "" where they set none.
type SpecRow struct {
	Key                                string
	Context, Output, Vision, Reasoning string
	// Gone: no provider of that id is there any more
	Gone bool
}

// ModelSpecs are the models the user gave any part of a spec, by key.
func ModelSpecs() []SpecRow {
	rows := map[string]*SpecRow{}
	row := func(k string) *SpecRow {
		if rows[k] == nil {
			rows[k] = &SpecRow{Key: k}
		}
		return rows[k]
	}
	for _, p := range All() {
		for m, n := range p.Contexts {
			if n > 0 {
				row(p.ID + "/" + m).Context = tokenWord(n)
			}
		}
	}
	s := settings.Load()
	for k, n := range s.ModelOutputs {
		if n > 0 {
			row(k).Output = tokenWord(n)
		}
	}
	for k, v := range s.ModelImages {
		row(k).Vision = map[bool]string{true: "on", false: "off"}[v]
	}
	for k, es := range s.ModelEfforts {
		if len(es) > 0 {
			row(k).Reasoning = strings.Join(es, ",")
		}
	}
	for k, off := range s.ModelReasonless {
		if off {
			row(k).Reasoning = "off"
		}
	}
	var out []SpecRow
	for _, k := range slices.Sorted(maps.Keys(rows)) {
		r := *rows[k]
		pid, _, _ := strings.Cut(k, "/")
		if lookIn(All(), pid) == nil {
			r.Gone = true
		}
		out = append(out, r)
	}
	return out
}

// tokenWord is a number of tokens as the CLI says it: 1M, 200k, 12345.
func tokenWord(n int) string {
	switch {
	case n <= 0:
		return "unknown"
	case n%1000000 == 0:
		return fmt.Sprintf("%dM", n/1000000)
	case n%1000 == 0:
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%d", n)
}
