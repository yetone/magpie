package gui

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// Each webview waits on the same setting, even when their browser storage is
// separate. Changes wake every waiting window without a page reload.
func privacyRoutes(mux *http.ServeMux) {
	var mu sync.Mutex
	changed := make(chan struct{})
	on := settings.Load().PrivacyMode
	type state struct {
		On bool `json:"on"`
	}
	read := func() state {
		current := settings.Load().PrivacyMode
		if current != on {
			on = current
			close(changed)
			changed = make(chan struct{})
		}
		return state{on}
	}
	mux.HandleFunc("GET /api/settings/privacy", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		mu.Lock()
		s, notify := read(), changed
		mu.Unlock()
		want := "0"
		if s.On {
			want = "1"
		}
		if r.URL.Query().Get("wait") == "1" && r.URL.Query().Get("on") == want {
			timer := time.NewTimer(25 * time.Second)
			defer timer.Stop()
			select {
			case <-r.Context().Done():
				return
			case <-notify:
			case <-timer.C:
			}
			mu.Lock()
			s = read()
			mu.Unlock()
		}
		writeJSON(w, s)
	})
	mux.HandleFunc("POST /api/settings/privacy", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ On *bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.On == nil {
			http.Error(w, "on must be true or false", http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		s := settings.Load()
		s.PrivacyMode = *in.On
		if err := settings.Save(s); err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, read())
	})
}
