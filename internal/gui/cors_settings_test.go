package gui

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// The web pages that may call the gateway (#1051) are saved as origins,
// a wildcard or a URL that isn't one is refused with the list left as it
// was, and a save of the rest of the Settings page leaves them alone.
func TestCORSOriginsSetting(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	srv := Handler(nil, nil)
	post := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return rec
	}
	rec := post("/api/settings/cors", `{"origins":["http://LocalHost:3000/","https://app.example.com:443"]}`)
	want := []string{"http://localhost:3000", "https://app.example.com"}
	if rec.Code != http.StatusOK || !slices.Equal(settings.Load().CORSOrigins, want) || !strings.Contains(rec.Body.String(), `"corsOrigins":["http://localhost:3000","https://app.example.com"]`) {
		t.Fatalf("%d %s, saved %v", rec.Code, rec.Body, settings.Load().CORSOrigins)
	}
	for _, bad := range []string{`["*"]`, `["http://localhost:3000","localhost:4000"]`, `["https://app.example.com/v1"]`} {
		if rec := post("/api/settings/cors", `{"origins":`+bad+`}`); rec.Code == http.StatusOK || !slices.Equal(settings.Load().CORSOrigins, want) {
			t.Errorf("%s: %d, saved %v", bad, rec.Code, settings.Load().CORSOrigins)
		}
	}
	if rec := post("/api/settings", `{"theme":"dark","lang":"en"}`); rec.Code != http.StatusOK || !slices.Equal(settings.Load().CORSOrigins, want) {
		t.Fatalf("the Settings page's save changed the origins: %d %v", rec.Code, settings.Load().CORSOrigins)
	}
	if rec := post("/api/settings/cors", `{"origins":[]}`); rec.Code != http.StatusOK || len(settings.Load().CORSOrigins) != 0 {
		t.Fatalf("emptied: %d %v", rec.Code, settings.Load().CORSOrigins)
	}
}
