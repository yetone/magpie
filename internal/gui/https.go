package gui

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/mtls"
	"github.com/yetone/magpie/internal/settings"
)

// httpsState is Settings → Network and sharing's HTTPS rows (zhangly1 on
// X: Claude on another computer on the LAN needed magpie at https://).
type httpsState struct {
	On bool `json:"on"`
	// Served: other computers are served https:// now, by this switch,
	// --tls-cert (Files) or mTLS
	Served bool `json:"served"`
	Files  bool `json:"files,omitempty"`
	MTLS   bool `json:"mtls,omitempty"`
	// Cert: the certificate served, while Served
	Cert    *gateway.CertInfo `json:"cert,omitempty"`
	CertErr string            `json:"certError,omitempty"`
	// CA: magpie's CA's SHA-256 while it signs the certificate served,
	// and CAURLs where other computers download it
	CA     string   `json:"ca,omitempty"`
	CAURLs []string `json:"caURLs,omitempty"`
	// Saved: where the CA was just saved
	Saved string `json:"saved,omitempty"`
}

func httpsNow() httpsState {
	s := httpsState{On: gateway.HTTPSOn(), Served: gateway.TLSOn(), MTLS: gateway.MTLSOn()}
	c, k := gateway.TLSFiles()
	s.Files = c != "" && k != ""
	if !s.Served {
		return s
	}
	info, err := gateway.ServedCert()
	if err != nil {
		s.CertErr = err.Error()
		return s
	}
	s.Cert = &info
	if info.From == "magpie" {
		if _, fp, err := gateway.CA(); err == nil {
			s.CA = fp
		}
		for _, u := range gateway.LANURLs() {
			s.CAURLs = append(s.CAURLs, u+gateway.CAPath)
		}
	}
	return s
}

var errFarPage = errors.New("this page is open from another computer, and HTTPS changes how it is reached: turn it on or off from the computer magpie runs on")

func httpsRoutes(mux *http.ServeMux, w Windows) {
	mux.HandleFunc("GET /api/settings/https", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, httpsNow())
	})
	// on or off: on makes magpie's CA first, unless a certificate was
	// imported. A page open from another computer can't flip it under
	// itself: its next request would come in the other scheme.
	mux.HandleFunc("POST /api/settings/https", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ On bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if isWeb(w) && !gateway.FromThisComputer(r) && in.On != gateway.HTTPSOn() {
			fail(rw, errFarPage)
			return
		}
		if in.On {
			if c, _ := gateway.ImportedFiles(); c == "" {
				if _, _, err := gateway.CA(); err != nil {
					fail(rw, err)
					return
				}
			}
		}
		if err := settings.SetHTTPS(in.On); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, httpsNow())
	})
	// the user's own certificate and key: the PEM files' text, any split
	mux.HandleFunc("POST /api/settings/https/import", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Files []string }
		if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 1<<20)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		files := make([][]byte, len(in.Files))
		for i, f := range in.Files {
			files[i] = []byte(f)
		}
		if err := gateway.ImportCert(files...); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, httpsNow())
	})
	mux.HandleFunc("POST /api/settings/https/remove", func(rw http.ResponseWriter, r *http.Request) {
		if err := gateway.RemoveImportedCert(); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, httpsNow())
	})
	// magpie's CA, saved to Downloads as magpie-ca.crt for another computer
	mux.HandleFunc("POST /api/settings/https/ca", func(rw http.ResponseWriter, r *http.Request) {
		b, _, err := gateway.CA()
		if err != nil {
			fail(rw, err)
			return
		}
		p := filepath.Join(downloads(), "magpie-ca.crt")
		if err := mtls.WriteFile(p, b, 0o644); err != nil {
			fail(rw, err)
			return
		}
		_ = w.OpenFolder(filepath.Dir(p))
		s := httpsNow()
		s.Saved = tilde(p)
		writeJSON(rw, s)
	})
}
