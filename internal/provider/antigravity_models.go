package provider

// Antigravity lists some models once for each thinking level it serves
// them at ("gemini-3.7-flash-low", "-medium", "-high", named "Gemini 3.7
// Flash (Low)" …), so a client that also picks an effort had two to pick.
// magpie offers such a family as one model ("gemini-3.7-flash", "Gemini
// 3.7 Flash") with the levels there are, and the gateway asks for the id
// the effort picks (AntigravityVariants).
//
// A family is only ids that are one id with an effort word after it: a
// model named like another but with an id of its own (gemini-3-flash-agent
// is "Gemini 3.5 Flash (High)") stays a model of its own, as does an id
// with no sibling. Which level a variant is at is what Antigravity's name
// for it says, where the names say one each: gemini-3.5-flash-extra-low is
// "(Low)" and gemini-3.5-flash-low "(Medium)"; else its id's word.

import (
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// antigravityEffortWords are the effort words of Antigravity's ids and the
// level each is, longest first.
var antigravityEffortWords = []struct{ word, level string }{
	{"extra-high", "xhigh"}, {"extra-low", "minimal"}, {"xhigh", "xhigh"}, {"minimal", "minimal"},
	{"low", "low"}, {"medium", "medium"}, {"high", "high"},
}

// splitAntigravityID is an id's family and the level its effort word
// says, "" when it has none.
func splitAntigravityID(id string) (family, effort string) {
	for _, e := range antigravityEffortWords {
		if b, ok := strings.CutSuffix(id, "-"+e.word); ok && b != "" {
			return b, e.level
		}
	}
	return id, ""
}

// EffortFamily is the model an id with an effort word after it is a level
// of ("gemini-3.8-flash-medium" is gemini-3.8-flash), and the id itself
// when it has none.
func EffortFamily(id string) string {
	f, _ := splitAntigravityID(id)
	return f
}

// antigravityNameEffort is the level a name's last words in brackets say
// ("Gemini 3.7 Flash (High)" is high), and the name without them.
func antigravityNameEffort(name string) (string, string) {
	open := strings.LastIndex(name, " (")
	if open < 0 || !strings.HasSuffix(name, ")") {
		return name, ""
	}
	var level string
	switch strings.ToLower(name[open+2 : len(name)-1]) {
	case "minimal", "extra low":
		level = "minimal"
	case "low":
		level = "low"
	case "medium":
		level = "medium"
	case "high":
		level = "high"
	case "extra high", "xhigh":
		level = "xhigh"
	default:
		return name, ""
	}
	return strings.TrimSpace(name[:open]), level
}

// antigravityFamily is one family of Antigravity's ids.
type antigravityFamily struct {
	id       string
	variants []catalog.Model
	efforts  []string // each variant's level, "" for the id without one
}

// byEffort is the id for each level, "" the one asked for when no level
// is: the family's id without an effort word, if Antigravity has it; else
// high — what Gemini 3 thinks at when it isn't told a level, so a client
// that asks for none gets what the plain model would give; else the
// highest there is.
func (f *antigravityFamily) byEffort() map[string]string {
	out := map[string]string{}
	for i, v := range f.variants {
		if _, ok := out[f.efforts[i]]; !ok {
			out[f.efforts[i]] = v.ID
		}
	}
	if _, ok := out[""]; !ok {
		out[""] = out["high"]
		for i := len(cursorLevelRank) - 1; i >= 0 && out[""] == ""; i-- {
			out[""] = out[cursorLevelRank[i]]
		}
	}
	return out
}

// model is the family as one model: named as Antigravity names its
// variants, without the level; with the levels there are, lowest first;
// holding what the least of its variants does.
func (f *antigravityFamily) model() catalog.Model {
	def := f.byEffort()[""]
	var m catalog.Model
	for _, v := range f.variants {
		if v.ID == def {
			m = v
		}
	}
	m.ID, m.Efforts = f.id, nil
	m.Name, _ = antigravityNameEffort(m.Name)
	if m.Name == "" || m.Name == def {
		m.Name = f.id
	}
	for _, v := range f.variants {
		if v.Context > 0 && (m.Context == 0 || v.Context < m.Context) {
			m.Context = v.Context
		}
		if v.Output > 0 && (m.Output == 0 || v.Output < m.Output) {
			m.Output = v.Output
		}
		m.Images = m.Images && v.Images
	}
	for _, l := range cursorLevelRank {
		if slices.Contains(f.efforts, l) {
			m.Efforts = append(m.Efforts, l)
		}
	}
	return m
}

// antigravityFamilies are the families of a list of Antigravity's ids, in
// the order the list first has them.
func antigravityFamilies(raw []catalog.Model) []*antigravityFamily {
	var out []*antigravityFamily
	by := map[string]*antigravityFamily{}
	for _, m := range raw {
		id, _ := splitAntigravityID(m.ID)
		f := by[id]
		if f == nil {
			f = &antigravityFamily{id: id}
			by[id] = f
			out = append(out, f)
		}
		f.variants = append(f.variants, m)
	}
	for _, f := range out {
		// the level each name says, if each says one and no two the same
		var named []string
		for _, v := range f.variants {
			if _, l := antigravityNameEffort(v.Name); l != "" && !slices.Contains(named, l) {
				named = append(named, l)
			}
		}
		for _, v := range f.variants {
			_, l := splitAntigravityID(v.ID)
			if len(named) == len(f.variants) {
				_, l = antigravityNameEffort(v.Name)
			}
			f.efforts = append(f.efforts, l)
		}
	}
	return out
}

// collapseAntigravityModels is Antigravity's list with each family one
// model; a family of one keeps Antigravity's id and name.
func collapseAntigravityModels(raw []catalog.Model) []catalog.Model {
	var out []catalog.Model
	for _, f := range antigravityFamilies(raw) {
		if len(f.variants) == 1 {
			out = append(out, f.variants[0])
			continue
		}
		out = append(out, f.model())
	}
	return out
}

// AntigravityVariants are Antigravity's ids a model magpie offers stands
// for, by level, "" the default; ok is false for a model that stands for
// none, which goes to Antigravity as it is.
func AntigravityVariants(model string) (map[string]string, bool) {
	raw, _, ok := catalog.Live("antigravity")
	if !ok {
		return nil, false
	}
	return antigravityVariantsIn(raw, model)
}

func antigravityVariantsIn(raw []catalog.Model, model string) (map[string]string, bool) {
	for _, f := range antigravityFamilies(raw) {
		if f.id == model && len(f.variants) > 1 {
			return f.byEffort(), true
		}
	}
	return nil, false
}

// AntigravityBase is the model magpie offers for one of Antigravity's ids
// at a level ("gemini-3.7-flash-low" is gemini-3.7-flash at low), and that
// level; ok is false for an id that is itself a model magpie offers, or
// that no family has. Picks and agents' models saved before the families
// were one model still name these ids.
func AntigravityBase(id string) (base, effort string, ok bool) {
	raw, _, live := catalog.Live("antigravity")
	if !live {
		return "", "", false
	}
	return antigravityBaseIn(raw, id)
}

func antigravityBaseIn(raw []catalog.Model, id string) (string, string, bool) {
	for _, f := range antigravityFamilies(raw) {
		if len(f.variants) < 2 {
			continue
		}
		for i, v := range f.variants {
			if v.ID == id && id != f.id {
				return f.id, f.efforts[i], true
			}
		}
	}
	return "", "", false
}

// antigravityPicks are the user's picks of Antigravity's models with each
// of its ids at a level the model magpie offers for it, in the order
// picked, each once.
func antigravityPicks(ids []string) []string {
	raw, _, live := catalog.Live("antigravity")
	if !live {
		return ids
	}
	var out []string
	for _, id := range ids {
		if base, _, ok := antigravityBaseIn(raw, id); ok {
			id = base
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}
