package gui

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// modelPriceJSON is a model's price from any provider (*/model), the one
// `magpie model price '*/model'` sets: what the user said it costs, if
// anything, and its maker's list price on models.dev, which is what the
// sheet shows greyed while nothing is typed.
type modelPriceJSON struct {
	Model string         `json:"model"`
	Price *catalog.Price `json:"price"`
	Maker *catalog.Price `json:"maker,omitempty"`
}

func anyModelPrice(model string) modelPriceJSON {
	out := modelPriceJSON{Model: model}
	if m, ok := settings.Shared().ModelPrices[provider.AnyPriceKey(model)]; ok {
		if p, bad := m.Price(); bad == "" {
			out.Price = &p
		}
	}
	if p, ok := provider.MakerPrice(model); ok {
		out.Maker = &p
	}
	return out
}

// pricedModel is the model a price is asked for, or "" when it names none:
// one id, not every model ("*") nor a provider's ("*/…").
func pricedModel(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "*" || len(s) > 200 || strings.HasPrefix(s, settings.AnyProvider) {
		return ""
	}
	return s
}

// modelPriceRoutes: the Usage page's Set price… on a model's row
// (soilzhu80 on X). A model an agent ran is often not in any provider's
// list, so the provider editor's Model settings can't price it; this sets
// what it costs from any provider not priced for it, the price a session's
// model is counted at. GET ?model= reads it; POST {"model","price"} sets
// it, a null price takes it away.
func modelPriceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/model-price", func(rw http.ResponseWriter, r *http.Request) {
		model := pricedModel(r.URL.Query().Get("model"))
		if model == "" {
			http.Error(rw, "name one model", http.StatusBadRequest)
			return
		}
		writeJSON(rw, anyModelPrice(model))
	})
	mux.HandleFunc("POST /api/model-price", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Model string         `json:"model"`
			Price *catalog.Price `json:"price"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 16<<10)).Decode(&in); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		model := pricedModel(in.Model)
		if model == "" {
			http.Error(rw, "name one model", http.StatusBadRequest)
			return
		}
		if in.Price != nil {
			// a price typed in the sheet has no tiers or peak hours
			in.Price.Tiers, in.Price.Peak = nil, ""
		}
		if err := provider.SetModelPrice(provider.AnyPriceKey(model), in.Price); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		// the Sessions tab's stats are priced when they are read
		forgetStats()
		writeJSON(rw, anyModelPrice(model))
	})
}
