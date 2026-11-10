package gui

import (
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/mtls"
	"github.com/yetone/magpie/internal/settings"
)

// mtlsCert is a client certificate magpie issued (hhdaxia on X, #5596),
// for Settings → Network and sharing: one still good, the newest last.
type mtlsCert struct {
	Name    string    `json:"name"`
	Serial  string    `json:"serial"`
	Expires time.Time `json:"expires"`
}

// mtlsCerts: the certificates still good, and the list's error when it
// can't be read (then none is admitted either).
func mtlsCerts() ([]mtlsCert, string) {
	l, err := gateway.MTLSStore().List()
	if err != nil {
		return nil, err.Error()
	}
	out := []mtlsCert{}
	now := time.Now()
	for _, c := range l {
		if c.Good(now) {
			out = append(out, mtlsCert{Name: c.Name, Serial: c.Serial, Expires: c.Expires})
		}
	}
	return out, ""
}

// pageCert is the client certificate a browser page came with (magpie
// web, or the app's page, opened from another computer): revoking it, or
// issuing its name again, would shut that page out at its next request.
func pageCert(w Windows, r *http.Request) *x509.Certificate {
	if !isWeb(w) || r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return nil
	}
	return r.TLS.PeerCertificates[0]
}

var errOwnCert = errors.New("this page came with that certificate, so it would be shut out: do this from another device, or from the computer magpie runs on")

func mtlsRoutes(mux *http.ServeMux, w Windows) {
	// on or off: on makes the CA first. A page open from another computer
	// without a certificate this magpie issued can't turn it on, which
	// would shut that page out at its next request.
	mux.HandleFunc("POST /api/settings/mtls", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ On bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if in.On {
			if isWeb(w) && gateway.NeedsClientCert(r) != "" {
				fail(rw, errors.New("this page is open from another computer without a client certificate this magpie issued, so turning this on would shut it out: issue one, install it in this browser, and open the page again over https://, or turn it on from the computer magpie runs on"))
				return
			}
			if _, _, err := gateway.MTLSStore().CA(); err != nil {
				fail(rw, err)
				return
			}
		}
		if err := settings.SetMTLS(in.On); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// a certificate for a device, saved to Downloads as magpie-<name>:
	// client.pem, client-key.pem and ca.pem; the key is kept nowhere else
	mux.HandleFunc("POST /api/settings/mtls/issue", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Name string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if c := pageCert(w, r); c != nil && strings.EqualFold(c.Subject.CommonName, strings.TrimSpace(in.Name)) {
			fail(rw, errOwnCert)
			return
		}
		iss, err := gateway.MTLSStore().Issue(in.Name)
		if err != nil {
			fail(rw, err)
			return
		}
		dir, err := mtls.Export(iss, downloads())
		if err != nil {
			fail(rw, fmt.Errorf("issued, but the files couldn't be saved, so the certificate is revoked again: %w", err))
			gateway.MTLSStore().Revoke(iss.Serial)
			return
		}
		_ = w.OpenFolder(dir) // saved either way; the path is in the answer
		st := settingsState()
		st.MTLSSaved = tilde(dir)
		writeJSON(rw, st)
	})
	mux.HandleFunc("POST /api/settings/mtls/revoke", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Serial string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if c := pageCert(w, r); c != nil && strings.EqualFold(c.SerialNumber.Text(16), strings.TrimSpace(in.Serial)) {
			fail(rw, errOwnCert)
			return
		}
		if _, err := gateway.MTLSStore().Revoke(in.Serial); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
}
