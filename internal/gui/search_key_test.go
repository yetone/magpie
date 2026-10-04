package gui

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A search API's saved key comes back whole for its row's Show and Copy
// (OnurBen on Discord), while the settings still list it masked; one not
// added is an error.
func TestSearchKeyShown(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	const key = "tvly-dev-0123456789abcdef"
	if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: "tavily", Key: key}); err != nil {
		t.Fatal(err)
	}
	post := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return rec
	}
	rec := post("/api/settings/search-key", `{"vendor":"tavily"}`)
	var got struct{ Key string }
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Key != key {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := post("/api/settings/search-key", `{"vendor":"exa"}`); rec.Code < 400 {
		t.Fatalf("a search API not added: %d %s", rec.Code, rec.Body)
	}
	var s settingsJSON
	searchState(&s)
	if len(s.SearchAPIs) != 1 || s.SearchAPIs[0].Key == key || s.SearchAPIs[0].Key == "" {
		t.Fatalf("the settings list %+v", s.SearchAPIs)
	}
}
