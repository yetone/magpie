package provider

import (
	"cmp"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// ReleaseDate is the day e's model came out (YYYY-MM-DD): its list's
// date, else the one models.dev gives the model by its id
// (catalog.ReleasedOf), so a relay's or a plugin's model whose own list
// gives none is dated too. "" for a routing group and for a model
// neither dates.
func (e Entry) ReleaseDate() string {
	if e.Group != "" {
		return ""
	}
	return cmp.Or(e.Released, catalog.ReleasedOf(e.Model))
}

// NewestFirst is a copy of es with each provider's models the newest
// release first, the order the pickers offer them in (elis_pina on X:
// 根据模型的推出时间进行排序, an older model added later listed above the
// new ones): a dated one before one with no date, two of a day by name,
// and the undated in the order the list gives them, which is the
// provider's own. Each provider's models stay together, where its
// first stands, and the routing groups keep their own order. Only the
// order a picker draws: what an agent is handed is es as it is.
func NewestFirst(es []Entry) []Entry {
	type row struct {
		e          Entry
		run, at    int
		day, named string
		group      bool
	}
	runs := map[string]int{}
	rows := make([]row, len(es))
	for i, e := range es {
		k := "\x00group"
		if e.Group == "" && e.Provider != nil {
			k = e.Provider.ID
		} else if e.Group == "" {
			k = ""
		}
		r, ok := runs[k]
		if !ok {
			r = len(runs)
			runs[k] = r
		}
		rows[i] = row{e: e, run: r, at: i, group: e.Group != ""}
		if !rows[i].group {
			rows[i].day, rows[i].named = e.ReleaseDate(), strings.ToLower(cmp.Or(e.Name, e.ID))
		}
	}
	slices.SortFunc(rows, func(a, b row) int {
		if a.run != b.run {
			return a.run - b.run
		}
		if !a.group {
			// the later day first, and "" after every day
			if c := strings.Compare(b.day, a.day); c != 0 {
				return c
			}
			if c := strings.Compare(a.named, b.named); a.day != "" && c != 0 {
				return c
			}
		}
		return a.at - b.at
	})
	out := make([]Entry, len(rows))
	for i, r := range rows {
		out[i] = r.e
	}
	return out
}

// PickOrder is es as agent's pickers offer them: in the order the user
// dragged its list into (ModelOrder), which is theirs, else the newest
// first (NewestFirst).
func PickOrder(agent string, es []Entry) []Entry {
	if len(ModelOrder(agent)) > 0 {
		return es
	}
	return NewestFirst(es)
}
