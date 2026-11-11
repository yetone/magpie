package gui

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/yetone/magpie/internal/gateway"
)

// Settings › Models' Check (EV_John_Liu on X): each pipeline besides chat
// (image generation, speech-to-text, text-to-speech, web search) checked
// with one real call through the gateway, as an agent's would go. Each
// costs the user what the call costs, so only a press runs one.
func handlePipelineCheck(rw http.ResponseWriter, r *http.Request) {
	var in struct{ Kind string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(rw, err)
		return
	}
	if !slices.Contains(gateway.Pipelines, in.Kind) {
		fail(rw, errors.New("no such pipeline: "+in.Kind))
		return
	}
	gw := served.Load()
	if gw == nil {
		fail(rw, errors.New("Another magpie serves the gateway: check there"))
		return
	}
	writeJSON(rw, gw.CheckPipeline(r.Context(), in.Kind))
}
