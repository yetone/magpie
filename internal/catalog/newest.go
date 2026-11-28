package catalog

import (
	"cmp"
	"slices"
	"strings"
)

// NewestFirst is a copy of one provider's models the newest release first,
// as a picker offers them (elis_pina on X): by its list's date, else the
// one models.dev gives the id (ReleasedOf); a dated one before one with no
// date, two of a day by name, and the undated in the list's own order.
func NewestFirst(ms []Model) []Model {
	type row struct {
		m          Model
		day, named string
	}
	rows := make([]row, len(ms))
	for i, m := range ms {
		rows[i] = row{m, cmp.Or(m.Released, ReleasedOf(m.ID)), strings.ToLower(cmp.Or(m.Name, m.ID))}
	}
	slices.SortStableFunc(rows, func(a, b row) int {
		if c := strings.Compare(b.day, a.day); c != 0 || a.day == "" {
			return c
		}
		return strings.Compare(a.named, b.named)
	})
	out := make([]Model, len(rows))
	for i, r := range rows {
		out[i] = r.m
	}
	return out
}
