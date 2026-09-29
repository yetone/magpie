package provider

// Cursor lists a model once for each effort and speed it serves it at
// ("grok-4.7-low", "grok-4.7-low-fast", … "grok-4.7-xhigh-fast"), and a
// Claude with thinking apart from one without. magpie offers one model a
// family, with the efforts there are, and the gateway asks for the id the
// effort picks (CursorVariants). Fast stays a model of its own: its context
// isn't always the same ("GPT-5.5 1M", "GPT-5.5 Fast"), and only Codex has a
// way to ask for it.

import (
	"regexp"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// cursorEfforts are the effort words of Cursor's ids, the levels magpie
// names them and the words Cursor's names say them in; extra-high before
// high.
var cursorEfforts = []struct{ word, level, label string }{
	{"extra-high", "xhigh", "Extra High"}, {"xhigh", "xhigh", "Extra High"}, {"minimal", "minimal", "Minimal"},
	{"none", "none", "None"}, {"low", "low", "Low"}, {"medium", "medium", "Medium"}, {"high", "high", "High"}, {"max", "max", "Max"},
}

var cursorLevelRank = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// splitCursorID is a Cursor id taken apart: its family, and the effort
// ("" for none named) it is at. Thinking comes before the effort or after
// it ("claude-opus-5-thinking-high", "claude-4.6-opus-high-thinking"); in
// the family it goes after, as does fast.
func splitCursorID(id string) (family, effort string) {
	s := id
	fast, thinking := false, false
	if b, ok := strings.CutSuffix(s, "-fast"); ok && b != "" {
		s, fast = b, true
	}
	if b, ok := strings.CutSuffix(s, "-thinking"); ok && b != "" {
		s, thinking = b, true
	}
	for _, e := range cursorEfforts {
		if b, ok := strings.CutSuffix(s, "-"+e.word); ok && b != "" {
			s, effort = b, e.level
			break
		}
	}
	if !thinking {
		if b, ok := strings.CutSuffix(s, "-thinking"); ok && b != "" {
			s, thinking = b, true
		}
	}
	if thinking {
		s += "-thinking"
	}
	if fast {
		s += "-fast"
	}
	return s, effort
}

func cursorLabel(level string) string {
	for _, e := range cursorEfforts {
		if e.level == level {
			return e.label
		}
	}
	return ""
}

// withoutWords is name with the first run of these words taken out, and
// whether it had them.
func withoutWords(name, words string) (string, bool) {
	ns, ws := strings.Fields(name), strings.Fields(words)
	if len(ws) == 0 {
		return name, false
	}
	for i := 0; i+len(ws) <= len(ns); i++ {
		if slices.Equal(ns[i:i+len(ws)], ws) {
			return strings.Join(append(slices.Clone(ns[:i]), ns[i+len(ws):]...), " "), true
		}
	}
	return name, false
}

// cursorFamily is one family of Cursor's ids.
type cursorFamily struct {
	id       string
	variants []catalog.Model
	efforts  []string // each variant's, "" for none named
}

// byEffort is the id for each effort, "" the one Cursor picks by default:
// the one without an effort in its id, else the one whose name doesn't
// say its effort ("Claude Opus 5.5 1M" is claude-opus-5-5-medium), else
// medium, else the first.
func (f *cursorFamily) byEffort() map[string]string {
	out := map[string]string{}
	def := slices.Index(f.efforts, "")
	for i, v := range f.variants {
		e := f.efforts[i]
		if _, ok := out[e]; !ok {
			out[e] = v.ID
		}
		if def < 0 && e != "" {
			if _, said := withoutWords(v.Name, cursorLabel(e)); !said {
				def = i
			}
		}
	}
	if def < 0 {
		def = max(0, slices.Index(f.efforts, "medium"))
	}
	out[""] = f.variants[def].ID
	return out
}

// model is the family as one model: named as its default is, without
// the effort; with the efforts there are, lowest first; holding what the
// least of its variants does.
func (f *cursorFamily) model() catalog.Model {
	def := f.byEffort()[""]
	m := catalog.Model{ID: f.id}
	for i, v := range f.variants {
		if v.ID == def {
			m.Name, _ = withoutWords(v.Name, cursorLabel(f.efforts[i]))
		}
		if v.Context > 0 && (m.Context == 0 || v.Context < m.Context) {
			m.Context = v.Context
		}
	}
	for _, l := range cursorLevelRank {
		if slices.Contains(f.efforts, l) {
			m.Efforts = append(m.Efforts, l)
		}
	}
	return m
}

// cursorFamilies are the families of a list of Cursor's ids, in the order
// the list first has them.
func cursorFamilies(raw []catalog.Model) []*cursorFamily {
	var out []*cursorFamily
	by := map[string]*cursorFamily{}
	for _, m := range raw {
		id, effort := splitCursorID(m.ID)
		f := by[id]
		if f == nil {
			f = &cursorFamily{id: id}
			by[id] = f
			out = append(out, f)
		}
		f.variants = append(f.variants, m)
		f.efforts = append(f.efforts, effort)
	}
	return out
}

// collapseCursorModels is Cursor's list with each family one model; a
// family of one keeps Cursor's id and name.
func collapseCursorModels(raw []catalog.Model) []catalog.Model {
	var out []catalog.Model
	for _, f := range cursorFamilies(raw) {
		if len(f.variants) == 1 {
			out = append(out, f.variants[0])
			continue
		}
		out = append(out, f.model())
	}
	return out
}

// cursorCapacity is the context Cursor puts in a model's name ("Claude
// Opus 5.5 1M", "GPT-5.5 (1M)").
var cursorCapacity = regexp.MustCompile(`\(\s*\d+M\s*\)|\b\d+M\b`)

// withoutCursorCapacity is Cursor's list, collapsed, with the context taken
// out of each name: it is the model's Context, shown apart. A name that
// would then be another model's ("GPT-5.5 1M" beside a "GPT-5.5") keeps
// it, as that is what tells the two apart. Ids stay as they are.
func withoutCursorCapacity(ms []catalog.Model) []catalog.Model {
	strip := func(name string) string {
		return strings.Join(strings.Fields(cursorCapacity.ReplaceAllString(name, " ")), " ")
	}
	names := map[string]int{} // each name, as listed or stripped, by how many models have it
	for _, m := range ms {
		names[strings.ToLower(m.Name)]++
		if s := strip(m.Name); s != m.Name {
			names[strings.ToLower(s)]++
		}
	}
	out := slices.Clone(ms)
	for i, m := range out {
		s := strip(m.Name)
		if s == m.Name || s == "" || names[strings.ToLower(s)] > 1 {
			continue
		}
		out[i].Name = s
	}
	return out
}

// CursorVariants are Cursor's ids a model magpie offers stands for, by
// effort, "" the default; ok is false for a model that stands for none,
// which goes to Cursor as it is.
func CursorVariants(model string) (map[string]string, bool) {
	raw, _, ok := catalog.Live("cursor")
	if !ok {
		return nil, false
	}
	return cursorVariantsIn(raw, model)
}

func cursorVariantsIn(raw []catalog.Model, model string) (map[string]string, bool) {
	for _, f := range cursorFamilies(raw) {
		if f.id == model && len(f.variants) > 1 {
			return f.byEffort(), true
		}
	}
	return nil, false
}

// CursorBase is the model magpie offers for one of Cursor's ids at an
// effort ("grok-4.7-low" is grok-4.7 at low, "grok-4.7-low-fast"
// grok-4.7-fast at low), and that effort; ok is false for an id that is
// itself a model magpie offers, or that no family has. Picks and agents'
// models saved before the families were one model still name these ids.
func CursorBase(id string) (base, effort string, ok bool) {
	raw, _, live := catalog.Live("cursor")
	if !live {
		return "", "", false
	}
	return cursorBaseIn(raw, id)
}

func cursorBaseIn(raw []catalog.Model, id string) (string, string, bool) {
	family, effort := splitCursorID(id)
	if family == id {
		return "", "", false
	}
	for _, f := range cursorFamilies(raw) {
		if f.id == family && len(f.variants) > 1 && slices.ContainsFunc(f.variants, func(m catalog.Model) bool { return m.ID == id }) {
			return family, effort, true
		}
	}
	return "", "", false
}

// cursorPicks are the user's picks of Cursor's models with each of
// Cursor's own ids the model magpie offers for it, in the order picked,
// each once.
func cursorPicks(ids []string) []string {
	raw, _, live := catalog.Live("cursor")
	if !live {
		return ids
	}
	var out []string
	for _, id := range ids {
		if base, _, ok := cursorBaseIn(raw, id); ok {
			id = base
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}
