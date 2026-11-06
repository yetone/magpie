package gui

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

func TestSettingsSearchProviderOff(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	h := Handler(nil, nil)
	for _, body := range []string{`{"searcher":"off"}`, `{"theme":"dark","searcher":"off"}`} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("saving %s: %d %s", body, rec.Code, rec.Body)
		}
		if got := settings.Load().Searcher; got != "off" {
			t.Fatalf("searcher = %q, want off", got)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", strings.NewReader(`{"searcher":"missing-provider"}`)))
	if rec.Code != http.StatusBadRequest || settings.Load().Searcher != "off" {
		t.Fatalf("invalid provider changed the choice: %d %s", rec.Code, rec.Body)
	}
}

// The Settings page sends only its own choices. What the settings keep per
// model — every name, level, price and image answer the user gave of a model
// on a page of its own — is set elsewhere, so a save from this page has to
// carry it over. This drives the real handler: POST /api/settings with a body
// naming nothing but the theme.
//
// The tables are the settings' own — settings.PerModelKeys, the walk the
// save itself goes by — each seeded with an entry saying which table it was
// written for, so a table added to the settings later is carried and checked
// here without this test being changed.
func TestSettingsPageSaveCarriesEveryPerModelTable(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))

	seed := settings.Load()
	if seedPerModelTables(t, &seed) == 0 {
		t.Fatal("the settings hold no per-model map: there is nothing here for a save to carry over")
	}
	if err := settings.Save(seed); err != nil {
		t.Fatal(err)
	}
	// what the settings say once written and read back, so what is compared
	// after the request is what really reached the disk, not what was set
	before := perModelTables(t)

	rec := httptest.NewRecorder()
	Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", strings.NewReader(`{"theme":"dark"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("saving the theme: %d %s", rec.Code, rec.Body)
	}

	if s := settings.Load(); s.Theme != "dark" {
		t.Fatalf("the theme was saved as %q, not dark", s.Theme)
	}
	now := perModelTables(t)
	for name, was := range before {
		got, ok := now[name]
		if !ok {
			t.Fatalf("%s is not one of the settings' per-model maps any more", name)
		}
		if !reflect.DeepEqual(was.Interface(), got.Interface()) {
			t.Errorf("%s did not survive the Settings page's save:\n\tsaved %v\n\tnow   %v", name, was.Interface(), got.Interface())
		}
	}
}

// CarryPerModel walks the Model* tables only. Which models an agent is shown
// is keyed by the agent, not by "<provider>/<model>", so Visible and
// HiddenModels are not among them and the save has to name them itself — the
// one place this page still lists what it keeps. A table added to that list
// later without a line here is dropped by a save; this says so rather than
// leaving it to be found.
func TestSettingsPageSaveKeepsWhichModelsAnAgentIsShown(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))

	seed := settings.Load()
	seed.Visible = map[string][]string{"claude": {"p/m"}}
	seed.HiddenModels = map[string][]string{"codex": {"p/n", "group/g"}}
	if err := settings.Save(seed); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", strings.NewReader(`{"theme":"dark"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("saving the theme: %d %s", rec.Code, rec.Body)
	}
	got := settings.Load()
	if !reflect.DeepEqual(got.Visible, map[string][]string{"claude": {"p/m"}}) {
		t.Errorf("Visible after a save of the Settings page: %v; want map[claude:[p/m]]", got.Visible)
	}
	if !reflect.DeepEqual(got.HiddenModels, map[string][]string{"codex": {"p/n", "group/g"}}) {
		t.Errorf("HiddenModels after a save of the Settings page: %v; want map[codex:[p/n group/g]]", got.HiddenModels)
	}
}

// perModelTables are the settings' per-model maps as they stand now, by
// name: the settings' own walk of them (settings.PerModelKeys, which is the
// walk the save goes by too) rather than a list written out here, which a
// table added to the settings later would not be in.
func perModelTables(t *testing.T) map[string]reflect.Value {
	t.Helper()
	s := settings.Load()
	out := map[string]reflect.Value{}
	settings.PerModelKeys(&s, func(name string, m reflect.Value) { out[name] = m })
	return out
}

// seedPerModelTables puts an entry into every per-model map of s, saying
// which table it was written for, so a table that came back empty, or with
// another's entry in it, is told apart. It counts the tables it seeded.
func seedPerModelTables(t *testing.T, s *settings.Settings) int {
	t.Helper()
	var n int
	settings.PerModelKeys(s, func(name string, m reflect.Value) {
		m.Set(reflect.MakeMap(m.Type()))
		m.SetMapIndex(reflect.ValueOf("p/"+name).Convert(m.Type().Key()), perModelValue(m.Type().Elem(), name))
		n++
	})
	return n
}

// perModelValue is a value of a per-model table's own type, saying which
// table it was written for, so a table that came back empty, or with another's
// entry in it, is told apart. A type with nothing said of it is left zero:
// the key alone gives the table away.
func perModelValue(typ reflect.Type, table string) reflect.Value {
	switch typ.Kind() {
	case reflect.String:
		return reflect.ValueOf("said of " + table).Convert(typ)
	case reflect.Bool:
		return reflect.ValueOf(true).Convert(typ)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflect.ValueOf(int64(len(table))).Convert(typ)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return reflect.ValueOf(uint64(len(table))).Convert(typ)
	case reflect.Float32, reflect.Float64:
		return reflect.ValueOf(float64(len(table))).Convert(typ)
	case reflect.Slice:
		out := reflect.MakeSlice(typ, 1, 1)
		out.Index(0).Set(perModelValue(typ.Elem(), table))
		return out
	case reflect.Array:
		out := reflect.New(typ).Elem()
		out.Index(0).Set(perModelValue(typ.Elem(), table))
		return out
	case reflect.Map:
		out := reflect.MakeMap(typ)
		out.SetMapIndex(reflect.ValueOf(table).Convert(typ.Key()), perModelValue(typ.Elem(), table))
		return out
	}
	return reflect.Zero(typ)
}

// How often a refused request is asked again (DD on Discord) is set by the
// Settings page, null for every one left, and kept by a save from a page
// that doesn't send it.
func TestSettingsRefusalRetries(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	handler := Handler(nil, nil)
	for _, x := range []struct {
		body string
		want int // -1 for unset
	}{
		{`{"theme":"dark","refusalRetries":0}`, 0},
		{`{"theme":"light"}`, 0},
		{`{"theme":"light","refusalRetries":2}`, 2},
		{`{"theme":"light","refusalRetries":null}`, -1},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", strings.NewReader(x.body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", x.body, rec.Code, rec.Body)
		}
		got := settings.Load().RefusalRetries
		if x.want < 0 && got != nil || x.want >= 0 && (got == nil || *got != x.want) {
			t.Fatalf("%s: refusalRetries %v", x.body, got)
		}
	}
}

// How long an image is given to be drawn (neighbads on X) is set by the
// Settings page, 0 for the default 5 minutes, kept by a save from a page
// that doesn't send it, and refused past 60 with the saved value kept.
func TestSettingsImageTimeout(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	handler := Handler(nil, nil)
	for _, x := range []struct {
		body string
		code int
		want int
	}{
		{`{"theme":"dark","imageTimeout":30}`, http.StatusOK, 30},
		{`{"theme":"light"}`, http.StatusOK, 30},
		{`{"theme":"light","imageTimeout":61}`, http.StatusBadRequest, 30},
		{`{"theme":"light","imageTimeout":0}`, http.StatusOK, 0},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", strings.NewReader(x.body)))
		if rec.Code != x.code {
			t.Fatalf("%s: %d %s", x.body, rec.Code, rec.Body)
		}
		if got := settings.Load().ImageTimeout; got != x.want {
			t.Fatalf("%s: imageTimeout %d", x.body, got)
		}
	}
}

// How long a routing group's classifier is given (#2934) is set by the
// Settings page, 0 for the default 8 seconds, kept by a save from a page
// that doesn't send it, and refused past 60 with the saved value kept.
func TestSettingsClassifyTimeout(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	handler := Handler(nil, nil)
	for _, x := range []struct {
		body string
		code int
		want int
	}{
		{`{"theme":"dark","classifyTimeout":30}`, http.StatusOK, 30},
		{`{"theme":"light"}`, http.StatusOK, 30},
		{`{"theme":"light","classifyTimeout":61}`, http.StatusBadRequest, 30},
		{`{"theme":"light","classifyTimeout":0}`, http.StatusOK, 0},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", strings.NewReader(x.body)))
		if rec.Code != x.code {
			t.Fatalf("%s: %d %s", x.body, rec.Code, rec.Body)
		}
		if got := settings.Load().ClassifyTimeout; got != x.want {
			t.Fatalf("%s: classifyTimeout %d", x.body, got)
		}
	}
}

func TestSettingsColorblindMode(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	handler := Handler(nil, nil)

	getBoot := func() string {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "/boot.js", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /boot.js: %d %s", rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	if boot := getBoot(); strings.Contains(boot, `"colorblind":true`) {
		t.Fatalf("colorblind off by default, got boot.js: %s", boot)
	}

	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"theme":"dark","colorblind":true}`, true},
		{`{"theme":"dark","colorblind":false}`, false},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", strings.NewReader(tc.body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", tc.body, rec.Code, rec.Body)
		}
		if got := settings.Load().Colorblind; got != tc.want {
			t.Fatalf("%s: Colorblind = %v, want %v", tc.body, got, tc.want)
		}
		if boot := getBoot(); strings.Contains(boot, `"colorblind":true`) != tc.want {
			t.Fatalf("%s: boot.js = %s, want colorblind=%v", tc.body, boot, tc.want)
		}
	}
}

// Masked words in any case is the Privacy page's: a page from before it
// sends no redactWordsAnyCase, and its save leaves the user's choice as it
// was; a page that sends it sets it (ZoeCats on Discord).
func TestSettingsRedactWordsAnyCaseKept(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	h := Handler(nil, nil)
	for _, c := range []struct {
		body string
		want bool
	}{
		{`{"theme":"dark","redactWords":["Nightjar"],"redactWordsAnyCase":true}`, true},
		{`{"theme":"light","redactWords":["Nightjar"]}`, true},
		{`{"theme":"light","redactWords":["Nightjar"],"redactWordsAnyCase":false}`, false},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", strings.NewReader(c.body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("saving %s: %d %s", c.body, rec.Code, rec.Body)
		}
		if got := settings.Load(); got.RedactWordsAnyCase != c.want || !reflect.DeepEqual(got.RedactWords, []string{"Nightjar"}) {
			t.Fatalf("after %s: any case %v, words %v; want %v", c.body, got.RedactWordsAnyCase, got.RedactWords, c.want)
		}
	}
}

// Whole words only is the Privacy page's too: a page from before it sends
// no redactWordsWhole, and its save leaves the user's choice as it was; a
// page that sends it sets it (acmilan_ on Discord).
func TestSettingsRedactWordsWholeKept(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	h := Handler(nil, nil)
	for _, c := range []struct {
		body string
		want bool
	}{
		{`{"theme":"dark","redactWords":["ABC"],"redactWordsWhole":true}`, true},
		{`{"theme":"light","redactWords":["ABC"],"redactWordsAnyCase":true}`, true},
		{`{"theme":"light","redactWords":["ABC"],"redactWordsWhole":false}`, false},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", strings.NewReader(c.body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("saving %s: %d %s", c.body, rec.Code, rec.Body)
		}
		if got := settings.Load(); got.RedactWordsWhole != c.want || !reflect.DeepEqual(got.RedactWords, []string{"ABC"}) {
			t.Fatalf("after %s: whole words %v, words %v; want %v", c.body, got.RedactWordsWhole, got.RedactWords, c.want)
		}
	}
}

// Reduce motion (#3813) is saved by the Settings page, handed to the page
// in boot.js so it is still from the first paint, and kept by a save from
// a page from before it, which doesn't send it.
func TestSettingsReduceMotion(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	handler := Handler(nil, nil)

	getBoot := func() string {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "/boot.js", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /boot.js: %d %s", rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	if boot := getBoot(); strings.Contains(boot, `"reduceMotion"`) {
		t.Fatalf("reduceMotion off by default, got boot.js: %s", boot)
	}
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"theme":"dark","reduceMotion":true}`, true},
		{`{"theme":"light"}`, true},
		{`{"theme":"dark","reduceMotion":false}`, false},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", strings.NewReader(tc.body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", tc.body, rec.Code, rec.Body)
		}
		if got := settings.Load().ReduceMotion; got != tc.want {
			t.Fatalf("%s: ReduceMotion = %v, want %v", tc.body, got, tc.want)
		}
		if boot := getBoot(); strings.Contains(boot, `"reduceMotion":true`) != tc.want {
			t.Fatalf("%s: boot.js = %s, want reduceMotion=%v", tc.body, boot, tc.want)
		}
	}
}

// The loading bird's switch (Yayoi_no_yume on X) is on unless turned off:
// boot.js says noBird only when it is, a save that leaves it out keeps it,
// and false brings the bird back.
func TestSettingsNoBird(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	handler := Handler(nil, nil)

	getBoot := func() string {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "/boot.js", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /boot.js: %d %s", rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	if boot := getBoot(); strings.Contains(boot, `"noBird"`) {
		t.Fatalf("the bird is on by default, got boot.js: %s", boot)
	}
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"theme":"dark","noBird":true}`, true},
		{`{"theme":"light"}`, true},
		{`{"theme":"dark","noBird":false}`, false},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", strings.NewReader(tc.body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", tc.body, rec.Code, rec.Body)
		}
		if got := settings.Load().NoBird; got != tc.want {
			t.Fatalf("%s: NoBird = %v, want %v", tc.body, got, tc.want)
		}
		if boot := getBoot(); strings.Contains(boot, `"noBird":true`) != tc.want {
			t.Fatalf("%s: boot.js = %s, want noBird=%v", tc.body, boot, tc.want)
		}
	}
}

// "Models that search by themselves" (Settings › Models › Web search) is
// set on its own: the endpoint sets it, and a save of the whole page, which
// doesn't name it, keeps it.
func TestSettingsSearchAllSetOnItsOwn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	h := Handler(nil, nil)
	post := func(path, body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", path, body, rec.Code, rec.Body)
		}
	}
	post("/api/settings/search-all", `{"on":true}`)
	if !settings.Load().SearchAll {
		t.Fatal("search-all didn't set it")
	}
	post("/api/settings", `{"theme":"dark"}`)
	if !settings.Load().SearchAll {
		t.Fatal("a save of the page dropped searchAll")
	}
	post("/api/settings/search-all", `{"on":false}`)
	if settings.Load().SearchAll {
		t.Fatal("search-all didn't turn it off")
	}
}

// How many results a search is answered with (Settings › Models › Web
// search, #5545) is set on its own: the endpoint sets it and says it
// back, a save of the whole page keeps it, and one out of 1 to 20 is
// refused with nothing written.
func TestSettingsSearchCountSetOnItsOwn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	h := Handler(nil, nil)
	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return rec
	}
	rec := post("/api/settings/search-count", `{"n":12}`)
	if rec.Code != http.StatusOK || settings.Load().SearchCount != 12 {
		t.Fatalf("search-count didn't set it: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"searchCount":12`) {
		t.Errorf("the state sent back doesn't say it: %s", rec.Body)
	}
	if rec := post("/api/settings", `{"theme":"dark"}`); rec.Code != http.StatusOK || settings.Load().SearchCount != 12 {
		t.Fatalf("a save of the page dropped searchCount: %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{`{"n":21}`, `{"n":-1}`} {
		if rec := post("/api/settings/search-count", bad); rec.Code == http.StatusOK || settings.Load().SearchCount != 12 {
			t.Errorf("%s: %d, count now %d", bad, rec.Code, settings.Load().SearchCount)
		}
	}
	if rec := post("/api/settings/search-count", `{"n":0}`); rec.Code != http.StatusOK || settings.Load().SearchCount != 0 {
		t.Fatalf("0 didn't put the default back: %d %s", rec.Code, rec.Body)
	}
}
