package gui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/settings"
)

const m365KeyName = "Claude for Microsoft 365"

// ensureM365Key keeps one named, revocable key for the Office add-in. Its
// credential remains in caller-keys.json and is returned only by the explicit
// copy route.
func ensureM365Key(s *settings.Settings) error {
	keys, err := access.List()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(keys, func(k access.Key) bool { return k.ID == s.M365KeyID })
	if i >= 0 {
		if keys[i].Off {
			if _, err := access.Update("on-key", access.Change{Key: keys[i].ID}); err != nil {
				return err
			}
		}
		return nil
	}
	if _, err := access.Update("add-key", access.Change{Name: m365KeyName}); err != nil {
		return err
	}
	keys, err = access.List()
	if err != nil {
		return err
	}
	// The one just appended is the key just made, even when the user has
	// another key with the same display name.
	if len(keys) == 0 {
		return errors.New("the Microsoft 365 gateway key was not saved")
	}
	s.M365KeyID = keys[len(keys)-1].ID
	return nil
}

func m365Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/settings/m365", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ On bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, err)
			return
		}
		if served.Load() == nil {
			fail(w, errors.New("this magpie is not serving the gateway; take it over or restart it here first"))
			return
		}
		s := settings.Load()
		if in.On {
			if err := ensureM365Key(&s); err != nil {
				fail(w, err)
				return
			}
			if !slices.Contains(s.CORSOrigins, gateway.M365Origin) {
				s.CORSOrigins = append(s.CORSOrigins, gateway.M365Origin)
			}
		}
		s.M365 = in.On
		if err := settings.Save(s); err != nil {
			fail(w, err)
			return
		}
		// Stop the old HTTPS listener before the gateway restart starts the
		// replacement, so its fixed loopback port is already free.
		c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = m365.Stop(c)
		cancel()
		if out := restartGateway(); !out.OK {
			fail(w, errors.New("the gateway could not restart: "+out.Reason+" "+out.Error))
			return
		}
		if in.On && !m365.Status().Running {
			fail(w, errors.New(m365.Status().Error))
			return
		}
		writeJSON(w, map[string]any{"settings": settingsState()})
	})
	mux.HandleFunc("POST /api/settings/m365/key", func(w http.ResponseWriter, r *http.Request) {
		s := settings.Load()
		if s.M365KeyID == "" {
			fail(w, errors.New("turn on Claude for Microsoft 365 first"))
			return
		}
		secret, err := access.Update("copy-key", access.Change{Key: s.M365KeyID})
		if err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, map[string]string{"secret": secret})
	})
	mux.HandleFunc("POST /api/settings/m365/certificate/{action}", func(w http.ResponseWriter, r *http.Request) {
		var (
			status gateway.M365CertificateStatus
			err    error
		)
		switch r.PathValue("action") {
		case "install":
			status, err = gateway.InstallM365Certificate(r.Context())
		case "remove":
			status, err = gateway.RemoveM365Certificate(r.Context())
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, status)
	})
}
