package gui

import (
	"encoding/json"
	"net/http"

	"github.com/yetone/magpie/internal/access"
)

// joinRoutes: Settings → Local network → Join with a password (EV_John_Liu
// on X). A password set here opens the gateway's /join page to other
// computers while magpie is shared: each that types it gets a gateway key
// of its own, removed in Gateway keys like any other. "" turns it off.
// The password is kept as a hash only (access.SetJoinPassword) and never
// sent back.
func joinRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/settings/join", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 8<<10)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := access.SetJoinPassword(in.Password); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
}
