package gui

import (
	"net/http"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// speedJSON is how fast a model answered on one provider over the last
// seven days, from the usage ledger: the mean time to its first token and
// how fast it wrote after it, the numbers the Requests page shows per call.
type speedJSON struct {
	TTFT int64   `json:"ttft,omitempty"` // ms
	TPS  float64 `json:"tps,omitempty"`  // output tokens a second after the first
	N    int     `json:"n"`              // the timed replies they are of
}

// modelSpeeds is speedJSON by catalog ref ("provider/model", an option's
// ref in an agent's model picker), for the models with a timed reply this
// week. The model picker says it by each row, so a model several providers
// serve shows which of them answers it fastest (axuhere on X). It reads
// the summary the Usage page reads, cached, never the log on its own.
func modelSpeeds() map[string]speedJSON {
	out := map[string]speedJSON{}
	for _, g := range usage.SummarizeTop(usage.Week, 0).Models {
		// a model whose calls went to more than one place is told apart
		// by Host, and its numbers aren't the provider's of today
		if g.Host != "" || g.Provider == "" || g.Model == "" || g.Timed == 0 {
			continue
		}
		s := speedJSON{TTFT: g.MeanTTFT(), TPS: g.Speed(), N: g.Timed}
		if s.TTFT <= 0 && s.TPS <= 0 {
			continue
		}
		out[g.Provider+"/"+g.Model] = s
	}
	return out
}

func speedsAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/model-speeds", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, modelSpeeds())
	})
	// how busy each model's plugin says it is now, by the same ref
	// lowercased, for the Agents page's model and its picker (axelburks
	// on Discord: only the provider editor showed it). Read when a picker
	// opens and with the Agents page's drift check, each at most every
	// 30s; with no plugin reporting it is an empty map at once.
	mux.HandleFunc("GET /api/model-loads", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, provider.ModelLoads(time.Now()))
	})
}
