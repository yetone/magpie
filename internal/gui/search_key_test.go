package gui

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"reflect"
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

// Settings sets a search API's base URL (#1580): Add with an address saves
// it, an address alone keeps the key, a URL that isn't http(s) is refused,
// and each vendor says the address it is asked at while none is set.
func TestSearchAPIBaseURLFromSettings(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings/search-api", strings.NewReader(body)))
		return rec
	}
	if rec := post(`{"vendor":"tavily","key":"tvly-1","url":"https://search.example.com/tavily/"}`); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if as := provider.SearchAPIs(); len(as) != 1 || as[0].Base() != "https://search.example.com/tavily" || as[0].Key != "tvly-1" {
		t.Fatalf("%+v", as)
	}
	// an address alone keeps the key
	if rec := post(`{"vendor":"tavily","key":"","url":"https://other.example.com"}`); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if as := provider.SearchAPIs(); len(as) != 1 || !reflect.DeepEqual(as[0], provider.SearchAPI{Vendor: "tavily", Key: "tvly-1", URL: "https://other.example.com"}) {
		t.Fatalf("%+v", as)
	}
	for _, bad := range []string{"ftp://x.example.com", "search.example.com", "https://x.example.com/?key=1"} {
		if rec := post(`{"vendor":"tavily","key":"tvly-1","url":"` + bad + `"}`); rec.Code < 400 {
			t.Errorf("%s taken: %d", bad, rec.Code)
		}
	}
	if as := provider.SearchAPIs(); as[0].URL != "https://other.example.com" {
		t.Fatalf("a refused address changed it: %+v", as)
	}
	var s settingsJSON
	searchState(&s)
	if len(s.SearchAPIs) != 1 || s.SearchAPIs[0].URL != "https://other.example.com" {
		t.Fatalf("the settings list %+v", s.SearchAPIs)
	}
	bases := map[string]string{}
	for _, v := range s.SearchVendors {
		bases[v.ID] = v.Base
	}
	if bases["tavily"] != "https://api.tavily.com" || bases["exa"] != "https://api.exa.ai" || bases["searxng"] != "" {
		t.Fatalf("vendors' own addresses %v", bases)
	}
}

// Settings sends the search APIs' new order and an API switched on or off
// (#2286); one off is listed off, keeps its key, and isn't asked.
func TestSearchAPIOrderAndOffFromSettings(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings/search-api", strings.NewReader(body)))
		return rec
	}
	for _, b := range []string{`{"vendor":"tavily","key":"tvly-1"}`, `{"vendor":"exa","key":"e-1"}`, `{"arrange":["exa","tavily"]}`, `{"vendor":"tavily","on":false}`} {
		if rec := post(b); rec.Code != 200 {
			t.Fatalf("%s: %d %s", b, rec.Code, rec.Body)
		}
	}
	var s settingsJSON
	searchState(&s)
	if len(s.SearchAPIs) != 2 || s.SearchAPIs[0].Vendor != "exa" || s.SearchAPIs[0].Off || s.SearchAPIs[1].Vendor != "tavily" || !s.SearchAPIs[1].Off || s.SearchAPIs[1].Key == "" {
		t.Fatalf("%+v", s.SearchAPIs)
	}
	if as := provider.SearchAPIs(); len(as) != 1 || as[0].Vendor != "exa" {
		t.Fatalf("asked %+v", as)
	}
	for _, bad := range []string{`{"arrange":["exa","google"]}`, `{"vendor":"brave","on":true}`} {
		if rec := post(bad); rec.Code < 400 {
			t.Errorf("%s taken: %d", bad, rec.Code)
		}
	}
	if rec := post(`{"vendor":"tavily","on":true}`); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if as := provider.SearchAPIs(); len(as) != 2 || as[1].Vendor != "tavily" {
		t.Fatalf("switched on %+v", as)
	}
}

// A search API's keys are a list in Settings (辰林鸭~ on Discord): each
// listed masked by its ref, never whole; added (several at once, one
// already there counted), switched off, put in order and removed by
// ref; a key typed in Add for an API already set goes after its keys;
// and Show reads the one key its ref names.
func TestSearchKeyListFromSettings(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	keys := []string{"tvly-dev-aaaaaaaaaaaa1111", "tvly-dev-bbbbbbbbbbbb2222", "tvly-dev-cccccccccccc3333", "tvly-dev-dddddddddddd4444"}
	ref := provider.SearchKeyRef
	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return rec
	}
	list := func() []searchKeyJSON {
		t.Helper()
		var s settingsJSON
		searchState(&s)
		b, _ := json.Marshal(s)
		for _, k := range keys {
			if strings.Contains(string(b), k) {
				t.Fatalf("the settings hold a whole key: %s", b)
			}
		}
		if len(s.SearchAPIs) != 1 {
			t.Fatalf("%+v", s.SearchAPIs)
		}
		return s.SearchAPIs[0].KeyList
	}
	order := func(l []searchKeyJSON) (out []string) {
		for _, k := range l {
			s := k.Ref
			if k.Off {
				s += " off"
			}
			out = append(out, s)
		}
		return out
	}
	if rec := post("/api/settings/search-api", `{"vendor":"tavily","key":"`+keys[0]+`"}`); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// a key added in Add once the API is set goes after its key
	if rec := post("/api/settings/search-api", `{"vendor":"tavily","key":"`+keys[1]+`","append":true}`); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec := post("/api/settings/search-keys", `{"vendor":"tavily","add":"`+keys[1]+`\n`+keys[2]+`, `+keys[3]+`"}`)
	var added struct{ Added, Had int }
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &added) != nil || added.Added != 2 || added.Had != 1 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	l := list()
	if !reflect.DeepEqual(order(l), []string{ref(keys[0]), ref(keys[1]), ref(keys[2]), ref(keys[3])}) || l[0].Masked != provider.MaskSearchKey(keys[0]) {
		t.Fatalf("%+v", l)
	}
	for _, body := range []string{
		`{"vendor":"tavily","ref":"` + ref(keys[1]) + `","on":false}`,
		`{"vendor":"tavily","arrange":["` + ref(keys[3]) + `","` + ref(keys[1]) + `"]}`,
		`{"vendor":"tavily","ref":"` + ref(keys[0]) + `","remove":true}`,
	} {
		if rec := post("/api/settings/search-keys", body); rec.Code != 200 {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if got := order(list()); !reflect.DeepEqual(got, []string{ref(keys[3]), ref(keys[1]) + " off", ref(keys[2])}) {
		t.Fatalf("%v", got)
	}
	for _, body := range []string{
		`{"vendor":"tavily","ref":"` + ref(keys[0]) + `","remove":true}`, // gone already
		`{"vendor":"tavily","arrange":["` + ref(keys[0]) + `"]}`,         // a key not here
		`{"vendor":"exa","add":"` + keys[0] + `"}`,                       // an API not set
		`{"vendor":"tavily"}`, // nothing asked
	} {
		if rec := post("/api/settings/search-keys", body); rec.Code < 400 {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}
	var got struct{ Key string }
	if rec := post("/api/settings/search-key", `{"vendor":"tavily","ref":"`+ref(keys[1])+`"}`); rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Key != keys[1] {
		t.Fatalf("an off key's Show: %d %s", rec.Code, rec.Body)
	}
	// a new address keeps every key, those off too
	if rec := post("/api/settings/search-api", `{"vendor":"tavily","url":"https://t.example.com"}`); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := order(list()); len(got) != 3 {
		t.Fatalf("%v", got)
	}
}
