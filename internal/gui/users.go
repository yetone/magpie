package gui

import (
	"encoding/json"
	"net/http"

	"github.com/yetone/magpie/internal/access"
)

func userRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/users", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		users, err := access.List()
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, map[string]any{"users": users})
	})
	mux.HandleFunc("POST /api/users/{action}", func(w http.ResponseWriter, r *http.Request) {
		var in access.Change
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, err)
			return
		}
		secret, err := access.Update(r.PathValue("action"), in)
		if err != nil {
			fail(w, err)
			return
		}
		users, err := access.List()
		if err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, map[string]any{"users": users, "secret": secret})
	})
}
