package gui

import (
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// catalogModel is one model of the synced models.dev catalog, as the model
// editor's "Same as" picker lists it.
type catalogModel struct {
	ID      string  `json:"id"`
	Name    string  `json:"name,omitempty"`
	Maker   string  `json:"maker,omitempty"`
	Context int     `json:"context,omitempty"`
	Output  int     `json:"output,omitempty"`
	Input   float64 `json:"input,omitempty"`
	Cost    float64 `json:"cost,omitempty"`
	Images  bool    `json:"images,omitempty"`
}

// catalogModels lists the models of the catalog magpie already synced
// (nothing is fetched), one a model: the providers that serve the same
// model (catalog.SameModel) are one entry, taken from the one that lists
// its window and price. Sorted by name.
func catalogModels() []catalogModel {
	best := map[string]catalog.Model{}
	score := func(m catalog.Model) int {
		n := 0
		if m.Price != nil {
			n += 2
		}
		if m.Context > 0 {
			n++
		}
		return n
	}
	for _, pid := range catalog.Providers() {
		for _, m := range catalog.Provider(pid) {
			k := catalog.SameModel(m.ID)
			if k == "" {
				continue
			}
			if old, ok := best[k]; !ok || score(m) > score(old) {
				best[k] = m
			}
		}
	}
	out := make([]catalogModel, 0, len(best))
	for _, m := range best {
		c := catalogModel{ID: m.ID, Name: m.Name, Maker: catalog.ProviderName(m.Provider), Context: m.Context, Output: m.Output, Images: m.Images}
		if m.Price != nil {
			c.Input, c.Cost = m.Price.Input, m.Price.Output
		}
		if c.Name == c.ID {
			c.Name = ""
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].ID), strings.ToLower(out[j].ID)
		if a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return slices.Clip(out)
}

func catalogModelsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/catalog/models", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, catalogModels())
	})
}
