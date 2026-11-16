package gui

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Settings › Web search › Advanced asks the search APIs together (#2845's
// mix, heyiwei5 on X): "mix" names two or more to ask at once, an empty
// list asks them one at a time again, and the settings say which are
// mixed. One named alone, or one not set up, is refused and changes
// nothing.
func TestSearchAPIsMixedFromSettings(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	post := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings/search-api", strings.NewReader(body)))
		return rec
	}
	for _, b := range []string{`{"vendor":"tavily","key":"tvly-dev-aaaaaaaaaaaa1111"}`, `{"vendor":"brave","key":"BSAaaaaaaaaaaaaaaaaaaaaaaaa"}`, `{"vendor":"exa","key":"exa-aaaaaaaaaaaaaaaaaaaa"}`} {
		if rec := post(b); rec.Code != 200 {
			t.Fatalf("%s: %d %s", b, rec.Code, rec.Body)
		}
	}
	mixed := func() (out []string) {
		t.Helper()
		var s settingsJSON
		searchState(&s)
		for _, a := range s.SearchAPIs {
			if a.Mix {
				out = append(out, a.Vendor)
			}
		}
		return out
	}
	if rec := post(`{"mix":["tavily","brave","exa"]}`); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := strings.Join(mixed(), ","); got != "tavily,brave,exa" {
		t.Fatalf("mixed %q", got)
	}
	for _, as := range provider.SearchAPIs() {
		if !as.Mix {
			t.Fatalf("not saved as mixed: %+v", as.Vendor)
		}
	}
	for _, b := range []string{`{"mix":["tavily"]}`, `{"mix":["tavily","you"]}`} {
		if rec := post(b); rec.Code < 400 {
			t.Errorf("%s: %d", b, rec.Code)
		}
	}
	if got := strings.Join(mixed(), ","); got != "tavily,brave,exa" {
		t.Fatalf("a refused mix changed it: %q", got)
	}
	// a key changed keeps the mark, as an edit from Settings sends none
	if rec := post(`{"vendor":"brave","key":"BSAbbbbbbbbbbbbbbbbbbbbbbbb"}`); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := strings.Join(mixed(), ","); got != "tavily,brave,exa" {
		t.Fatalf("a new key unmixed it: %q", got)
	}
	if rec := post(`{"mix":[]}`); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := mixed(); len(got) != 0 {
		t.Fatalf("still mixed %v", got)
	}
}
