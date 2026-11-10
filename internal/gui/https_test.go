package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/settings"
)

// webTab is the page served to a browser (magpie web).
type webTab struct{ folderOnly }

func (webTab) browserTab() {}

func httpsMux(t *testing.T, w Windows) func(method, path, body, from string) (int, httpsState, string) {
	t.Helper()
	mux := http.NewServeMux()
	httpsRoutes(mux, w)
	return func(method, path, body, from string) (int, httpsState, string) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if from != "" {
			r.RemoteAddr = from
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		var s httpsState
		json.Unmarshal(rec.Body.Bytes(), &s)
		return rec.Code, s, rec.Body.String()
	}
}

// Settings → Network and sharing → HTTPS (zhangly1 on X): on makes
// magpie's CA and serves a certificate it signs, its fingerprint shown;
// Save puts magpie-ca.crt in Downloads; off goes back to plain http://.
func TestSettingsHTTPSOnSaveOff(t *testing.T) {
	h := sandboxHome(t)
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3499")
	t.Setenv(gateway.TLSCertEnv, "")
	t.Setenv(gateway.TLSKeyEnv, "")
	os.MkdirAll(filepath.Join(h, "Downloads"), 0o755)
	call := httpsMux(t, folderOnly{})

	if code, s, out := call("GET", "/api/settings/https", "", ""); code != 200 || s.On || s.Served {
		t.Fatalf("at first: %d %s", code, out)
	}
	code, s, out := call("POST", "/api/settings/https", `{"on":true}`, "")
	if code != 200 || !s.On || !s.Served || !settings.Shared().HTTPS || s.Cert == nil || s.Cert.From != "magpie" || s.CA == "" {
		t.Fatalf("on: %d %s", code, out)
	}
	for _, u := range s.CAURLs {
		if !strings.HasPrefix(u, "https://") || !strings.HasSuffix(u, gateway.CAPath) {
			t.Errorf("CA's address %q", u)
		}
	}
	code, s, out = call("POST", "/api/settings/https/ca", `{}`, "")
	if code != 200 || s.Saved == "" {
		t.Fatalf("save: %d %s", code, out)
	}
	b, err := os.ReadFile(filepath.Join(h, "Downloads", "magpie-ca.crt"))
	want, fp, _ := gateway.CA()
	if err != nil || string(b) != string(want) || fp != s.CA {
		t.Fatalf("saved CA: %v %q", err, b)
	}
	code, s, out = call("POST", "/api/settings/https", `{"on":false}`, "")
	if code != 200 || s.On || s.Served || settings.Shared().HTTPS {
		t.Fatalf("off: %d %s", code, out)
	}
}

// A page open in a browser on another computer can't turn HTTPS on or
// off under itself; one on this computer can, and the app's window can.
func TestSettingsHTTPSFarPage(t *testing.T) {
	sandboxHome(t)
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3499")
	call := httpsMux(t, webTab{})
	if code, _, out := call("POST", "/api/settings/https", `{"on":true}`, "192.168.1.9:5000"); code < 400 || settings.Shared().HTTPS {
		t.Fatalf("far page turned it on: %d %s", code, out)
	}
	if code, s, out := call("POST", "/api/settings/https", `{"on":true}`, "127.0.0.1:5000"); code != 200 || !s.On {
		t.Fatalf("this computer's page: %d %s", code, out)
	}
	// the same value again changes nothing, and is answered
	if code, s, out := call("POST", "/api/settings/https", `{"on":true}`, "192.168.1.9:5000"); code != 200 || !s.On {
		t.Fatalf("far page, unchanged: %d %s", code, out)
	}
}

// An import that doesn't parse is refused with the reason, and nothing is
// kept.
func TestSettingsHTTPSImportRefused(t *testing.T) {
	sandboxHome(t)
	call := httpsMux(t, folderOnly{})
	code, _, out := call("POST", "/api/settings/https/import", `{"files":["not a certificate"]}`, "")
	if code < 400 || !strings.Contains(out, "no PEM certificate or key") {
		t.Fatalf("import: %d %s", code, out)
	}
	if c, _ := gateway.ImportedFiles(); c != "" {
		t.Fatalf("kept %s", c)
	}
}
