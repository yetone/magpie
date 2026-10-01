package gui

import (
	"encoding/json"
	"net/http"

	"github.com/yetone/magpie/internal/access"
)

func callerKeyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/caller-keys", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		access.MigrateLegacyLANKeyBestEffort()
		keys, err := access.List()
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, map[string]any{"keys": keys})
	})
	mux.HandleFunc("POST /api/caller-keys/{action}", func(w http.ResponseWriter, r *http.Request) {
		var in access.Change
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, err)
			return
		}
		access.MigrateLegacyLANKeyBestEffort()
		secret, err := access.Update(r.PathValue("action"), in)
		if err != nil {
			fail(w, err)
			return
		}
		keys, err := access.List()
		if err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, map[string]any{"keys": keys, "secret": secret})
	})
}
