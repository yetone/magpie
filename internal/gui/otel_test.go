package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

func TestOTelPreferencesAPI(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("MAGPIE_OTEL_ENDPOINT", "https://environment.test")
	t.Setenv("MAGPIE_OTEL_HEADERS", "Authorization=PRIVATE-ENV-SECRET")
	h := Handler(nil, nil)
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("POST", "/api/settings", strings.NewReader(`{"otel":{"enabled":true,"endpoint":"https://saved.test/","headers":{"Authorization":"Bearer saved"}}}`)))
	if r.Code != http.StatusOK {
		t.Fatalf("save: %d %s", r.Code, r.Body)
	}
	r = httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("GET", "/api/settings", nil))
	var got settingsJSON
	if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OTelEnv || !got.OTel.Enabled || got.OTel.Endpoint != "https://saved.test" || strings.Contains(r.Body.String(), "PRIVATE-ENV-SECRET") {
		t.Fatalf("preferences: %s", r.Body)
	}
	r = httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("POST", "/api/settings", strings.NewReader(`{"otel":{"enabled":true,"endpoint":"file:///tmp/secret"}}`)))
	if r.Code == http.StatusOK || settings.Load().OTel.Endpoint != "https://saved.test" {
		t.Fatalf("invalid endpoint overwrote preferences: %d %s", r.Code, r.Body)
	}
}
