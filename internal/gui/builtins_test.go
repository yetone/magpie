package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/pluginrt"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	_ "github.com/yetone/magpie/plugins/all"
)

func builtinHome(t *testing.T) http.Handler {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	return Handler(nil, nil)
}

func postBuiltin(h http.Handler, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/plugins/builtin", strings.NewReader(body)))
	return rec
}

// A switch is kept in plugins.yml as the user's own choice and the
// runtime follows it; one in use is refused off and stays on; an unknown
// one is refused.
func TestBuiltinSwitch(t *testing.T) {
	h := builtinHome(t)
	if _, err := pluginrt.Settle(); err != nil {
		t.Fatal(err)
	}
	// a new install has every one on, so a new user sees all magpie does
	for _, b := range builtinsState() {
		if !b.On || b.State != pluginrt.Active {
			t.Fatalf("a new install: %+v", b)
		}
	}
	if err := pluginrt.SetPreset(pluginrt.PresetEssentials); err != nil {
		t.Fatal(err)
	}
	if rec := postBuiltin(h, `{"id":"library","on":true}`); rec.Code != http.StatusOK {
		t.Fatalf("library on: %d %s", rec.Code, rec.Body)
	} else {
		var out []builtinJSON
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if len(out) != 14 {
			t.Fatalf("the built-ins: %+v", out)
		}
		for _, b := range out {
			// the Gateway, Routing and Usage pages, the routing strategies,
			// the gateway middleware, privacy masking, web search, key
			// limits and plugin tools are essential: on in every preset
			if b.On != (b.ID == "budget" || b.ID == "library" || b.ID == "gateway" || b.ID == "middleware" || b.ID == "redact" || b.ID == "rotation" || b.ID == "routing" || b.ID == "search" || b.ID == "tools" || b.ID == "usage") || b.On != (b.State == pluginrt.Active) {
				t.Fatalf("after library on: %+v", out)
			}
		}
	}
	if !pluginrt.Running().Active("library") || pluginrt.Running().Active("sessions") {
		t.Fatalf("the runtime: %+v", pluginrt.Running().Statuses())
	}
	if rec := postBuiltin(h, `{"id":"nope","on":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown plugin: %d", rec.Code)
	}
	// Observability in use: sending to a collector
	s := settings.Load()
	s.OTel.Enabled = true
	s.OTel.Endpoint = "http://127.0.0.1:4318"
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if rec := postBuiltin(h, `{"id":"otel","on":false}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "in use") {
		t.Fatalf("otel off while in use: %d %s", rec.Code, rec.Body)
	}
	for _, b := range builtinsState() {
		if b.ID == "otel" && (!b.On || b.Chosen || b.InUse != 1 || b.Use != "Sending to a collector" || b.Source != pluginrt.FromInUse) {
			t.Fatalf("otel in use: %+v", b)
		}
	}
}

// A plugins.yml that doesn't parse leaves every plugin on, says so, and
// no switch writes over it.
func TestBuiltinBrokenFile(t *testing.T) {
	h := builtinHome(t)
	if err := os.MkdirAll(filepath.Dir(pluginrt.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pluginrt.Path(), []byte("plugins: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, b := range builtinsState() {
		if b.Broken == "" {
			t.Fatalf("not told broken: %+v", b)
		}
	}
	if rec := postBuiltin(h, `{"id":"library","on":false}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("switched over a broken file: %d", rec.Code)
	}
	if b, _ := os.ReadFile(pluginrt.Path()); string(b) != "plugins: [\n" {
		t.Fatalf("written over: %q", b)
	}
}

// boot.js says what is off before the page paints: nothing for an
// install as it starts, what Essentials leaves off once the user picks it.
func TestBootSaysBuiltinsOff(t *testing.T) {
	h := builtinHome(t)
	boot := func() string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/boot.js", nil))
		return rec.Body.String()
	}
	if _, err := pluginrt.Settle(); err != nil {
		t.Fatal(err)
	}
	if b := boot(); !strings.Contains(b, `"featuresOff":[]`) {
		t.Fatalf("an install as it starts: %s", b)
	}
	if err := pluginrt.SetPreset(pluginrt.PresetEssentials); err != nil {
		t.Fatal(err)
	}
	if err := pluginrt.Set("library", true); err != nil {
		t.Fatal(err)
	}
	if b := boot(); !strings.Contains(b, `"featuresOff":["groups","otel","sessions","warmups"]`) {
		t.Fatalf("Essentials picked: %s", b)
	}
}

// boot.js says where the gateway listens when this process serves it, so
// the sidebar's line is drawn before the first paint (#3429), and nothing
// when it doesn't: one served by another magpie is the providers' read to
// find, never this process's guess.
func TestBootSaysWhereItsGatewayListens(t *testing.T) {
	h := builtinHome(t)
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:3481")
	boot := func() string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/boot.js", nil))
		return rec.Body.String()
	}
	prev := served.Swap(nil)
	t.Cleanup(func() { served.Store(prev) })
	if b := boot(); strings.Contains(b, "sideGateway") {
		t.Fatalf("no gateway served here: %s", b)
	}
	served.Store(gateway.New())
	if b := boot(); !strings.Contains(b, `"sideGateway":{"boot":true,"running":true,"url":"http://127.0.0.1:3481"}`) {
		t.Fatalf("served here: %s", b)
	}
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3483")
	// on the network, it says so, with the addresses other machines use,
	// for the window's line to name them (coeo91 on Discord)
	if b := boot(); !strings.Contains(b, `"sideGateway":{"boot":true,"lan":true,`) || !strings.Contains(b, `"running":true,"url":"http://127.0.0.1:3483"}`) {
		t.Fatalf("every interface, reached on loopback: %s", b)
	}
	// MAGPIE_PUBLIC_URL goes with it while other machines are let in, for
	// magpie web's page to show (noahhrreion on Discord, in Docker), and
	// not while the gateway is this computer's alone
	t.Setenv("MAGPIE_PUBLIC_URL", "http://nas.local:3425")
	if b := boot(); !strings.Contains(b, `"sideGateway":{"boot":true,"lan":true,"lanURLs":["http://nas.local:3425"],"publicURL":"http://nas.local:3425","running":true,"url":"http://127.0.0.1:3483"}`) {
		t.Fatalf("on the network, with MAGPIE_PUBLIC_URL: %s", b)
	}
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:3483")
	if b := boot(); strings.Contains(b, "nas.local") || strings.Contains(b, `"lan"`) {
		t.Fatalf("on loopback, not shared: %s", b)
	}
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	served.Store(nil)
	if b := boot(); strings.Contains(b, "sideGateway") {
		t.Fatalf("stopped: %s", b)
	}
}

// A plugin's settings are set only as their schema allows, kept in
// plugins.yml, and handed to the page in boot.js; a request says on or
// config, not both.
func TestBuiltinConfig(t *testing.T) {
	h := builtinHome(t)
	if _, err := pluginrt.Settle(); err != nil {
		t.Fatal(err)
	}
	gw := func() builtinJSON {
		for _, b := range builtinsState() {
			if b.ID == "gateway" {
				return b
			}
		}
		t.Fatal("no gateway plugin")
		return builtinJSON{}
	}
	if b := gw(); b.Schema == nil || b.Schema.Properties["callsPage"] == nil || b.Config["callsPage"] != int64(20) {
		t.Fatalf("before: %+v", b)
	}
	if rec := postBuiltin(h, `{"id":"gateway","config":{"callsPage":50}}`); rec.Code != http.StatusOK {
		t.Fatalf("50: %d %s", rec.Code, rec.Body)
	}
	if b := gw(); b.Config["callsPage"] != int64(50) || !b.On {
		t.Fatalf("after 50: %+v", b)
	}
	for _, bad := range []string{
		`{"id":"gateway","config":{"callsPage":30}}`,
		`{"id":"gateway","config":{"other":1}}`,
		`{"id":"gateway","on":true,"config":{"callsPage":10}}`,
		`{"id":"gateway"}`,
		`{"id":"library","config":{"callsPage":10}}`,
	} {
		if rec := postBuiltin(h, bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", bad, rec.Code, rec.Body)
		}
	}
	if b := gw(); b.Config["callsPage"] != int64(50) {
		t.Fatalf("a refused one written: %+v", b)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/boot.js", nil))
	if !strings.Contains(rec.Body.String(), `"pluginConfig":{"gateway":{"callsPage":50}}`) {
		t.Fatalf("boot.js: %s", rec.Body)
	}
}

// Every file a built-in plugin says it has is on disk, loaded by the page
// and named with its hash so a new one isn't read from the cache: a script
// after the kernel and before app.js, which looks for its pages, a
// stylesheet in the head with the others.
func TestBuiltinClientsLoaded(t *testing.T) {
	page, err := os.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	src := string(page)
	at := func(name string) int {
		tag := `<script src="` + name + `"></script>`
		if strings.HasSuffix(name, ".css") {
			tag = `<link rel="stylesheet" href="` + name + `">`
		}
		i := strings.Index(src, tag)
		if i < 0 {
			t.Fatalf("index.html doesn't load %s", name)
		}
		return i
	}
	kernel, app, start := at("kernel.js"), at("app.js"), at("start.js")
	if kernel > app || app > start {
		t.Fatal("kernel.js, app.js and start.js out of order")
	}
	n := 0
	for _, m := range pluginrt.Registered() {
		for _, c := range m.Client {
			name := "plugins/" + m.ID + "/" + c
			if _, err := os.Stat(filepath.Join("assets", name)); err != nil {
				t.Errorf("%s: %v", name, err)
			}
			attr := "src"
			if strings.HasSuffix(name, ".css") {
				attr = "href"
				if i := at(name); i > strings.Index(src, "</head>") {
					t.Errorf("%s isn't in the head", name)
				}
			} else if c == "page.js" {
				// a page's code that runs on app.js's helpers: after app.js,
				// before start.js shows the first page
				if i := at(name); i < app || i > start {
					t.Errorf("%s isn't between app.js and start.js", name)
				}
			} else if i := at(name); i < kernel || i > app {
				t.Errorf("%s isn't between kernel.js and app.js", name)
			}
			if !strings.Contains(string(versionedPage(page)), attr+`="`+name+`?v=`) {
				t.Errorf("%s isn't named with its hash", name)
			}
			n++
		}
	}
	if n == 0 {
		t.Fatal("no plugin has a script")
	}
}

// Every string of every built-in plugin is said in every language.
func TestBuiltinsTranslated(t *testing.T) {
	src, err := os.ReadFile("assets/i18n.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"zh: {", `"zh-TW": {`, "ja: {", "de: {"} {
		_, block, ok := strings.Cut(string(src), "\n  "+lang+"\n")
		if !ok {
			t.Fatalf("no %s block", lang)
		}
		block, _, _ = strings.Cut(block, "\n  },")
		for _, m := range pluginrt.Registered() {
			says := []string{m.Name, m.Sub, m.One, m.Many}
			if sc := m.Schema(); sc != nil {
				for _, p := range sc.Properties {
					says = append(says, p.Title, p.Description)
				}
			}
			for _, s := range says {
				if s == "" {
					continue
				}
				k, _ := json.Marshal(s)
				if !strings.Contains(block, "\n    "+string(k)+": ") {
					t.Errorf("%s doesn't say %s", lang, k)
				}
			}
		}
	}
}

// Web search's row says what a model that can't search is searched with
// now (netube99 on X): nothing yet in a new home, a search API once one
// is set, and none while it is off.
func TestBuiltinSearchSaysWhatSearches(t *testing.T) {
	builtinHome(t)
	if _, err := pluginrt.Settle(); err != nil {
		t.Fatal(err)
	}
	search := func() builtinJSON {
		for _, b := range builtinsState() {
			if b.ID == "search" {
				return b
			}
		}
		t.Fatal("no search row")
		return builtinJSON{}
	}
	if b := search(); !b.On || len(b.Searches) != 0 {
		t.Fatalf("a new home: %+v", b)
	}
	if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: "tavily", Key: "tvly-test"}); err != nil {
		t.Fatal(err)
	}
	if b := search(); !b.On || !slices.Equal(b.Searches, []string{"Tavily"}) {
		t.Fatalf("with Tavily: %+v", b)
	}
	if err := provider.RemoveSearchAPI("tavily"); err != nil {
		t.Fatal(err)
	}
	if err := pluginrt.Set("search", false); err != nil {
		t.Fatal(err)
	}
	if b := search(); b.On || b.Searches != nil {
		t.Fatalf("off: %+v", b)
	}
}

// A built-in with settings says where they are, and its page on or off,
// for its row to go to both (netube99 on X: masking's switch, settings and
// page didn't name each other)
func TestBuiltinSaysWhereItsSettingsAre(t *testing.T) {
	builtinHome(t)
	if _, err := pluginrt.Settle(); err != nil {
		t.Fatal(err)
	}
	if err := pluginrt.Set("redact", false); err != nil {
		t.Fatal(err)
	}
	want := map[string][3]string{
		"redact":   {"privacy #redactList", "masking", ""},
		"search":   {"models #searchList", "", ""},
		"otel":     {"otel #otelList", "", ""},
		"gateway":  {"network #portList", "gateway", ""},
		"library":  {"", "library", ""},
		"groups":   {"", "", "groups"},
		"budget":   {"", "", "gateway"},
		"rotation": {"", "", "providers"},
		"warmups":  {"", "", "providers"},
		"tools":    {"", "", "plugins"},
	}
	for _, b := range builtinsState() {
		w, ok := want[b.ID]
		if !ok {
			continue
		}
		delete(want, b.ID)
		if got := [3]string{b.SetAt, b.Page, b.Opens}; got != w {
			t.Errorf("%s (on %v): %q, want %q", b.ID, b.On, got, w)
		}
		if b.ID == "redact" && b.On {
			t.Error("redact stayed on")
		}
	}
	if len(want) > 0 {
		t.Errorf("not listed: %v", want)
	}
}
