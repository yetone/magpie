package gui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/redact"
	"github.com/yetone/magpie/internal/settings"
)

// What the models' page keeps in settings — names, levels, whether a model
// takes images — outlives a save of the Settings page, which never sends it.
func TestSettingsSaveKeepsModelChoices(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	if err := settings.Save(settings.Settings{
		ModelNames:   map[string]string{"p/m": "Mine"},
		ModelEfforts: map[string][]string{"p/m": {"low"}},
		ModelImages:  map[string]bool{"p/m": true},
	}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", strings.NewReader(`{"theme":"dark","lang":"en"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	s := settings.Load()
	if s.Theme != "dark" || s.ModelNames["p/m"] != "Mine" || len(s.ModelEfforts["p/m"]) != 1 || !s.ModelImages["p/m"] {
		t.Fatalf("theme %q names %v efforts %v images %v", s.Theme, s.ModelNames, s.ModelEfforts, s.ModelImages)
	}
}

// Everything in settings the Settings page doesn't send — the models hidden
// from an agent one by one among it — outlives a save of that page: each
// field prefsKeep leaves out is set here, and must be as it was after a
// save of the theme alone. A field added to settings is either sent by the
// page or has to be set here, and kept by the handler.
func TestSettingsSaveKeepsWhatItDoesNotSend(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	js, err := os.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	_, fn, _ := strings.Cut(string(js), "\nfunction prefsKeep(s) {")
	fn, _, _ = strings.Cut(fn, "\n}\n")
	sent := map[string]bool{}
	for _, m := range regexp.MustCompile(`(\w+):`).FindAllStringSubmatch(fn, -1) {
		sent[m[1]] = true
	}
	if !sent["theme"] || !sent["proxy"] {
		t.Fatalf("prefsKeep not read from app.js: %v", sent)
	}
	one, two := 1.0, 2.0
	was := settings.Settings{
		AgentOrder:      []string{"codex"},
		AgentsHidden:    []string{"goose"},
		AgentsShown:     []string{"pi"},
		Visible:         map[string][]string{"claude": {"anthropic"}},
		HiddenModels:    map[string][]string{"claude": {"p/m"}, "codex": {"p/n", "group/g"}},
		ModelNames:      map[string]string{"p/m": "Mine"},
		ModelEfforts:    map[string][]string{"p/m": {"low"}},
		ModelImages:     map[string]bool{"p/m": true},
		ModelOutputs:    map[string]int{"p/m": 131072},
		ModelPrices:     map[string]settings.ModelPrice{"p/m": {Input: &one, Output: &two}},
		RedactRules:     []redact.Rule{{Kind: "prefix", Prefix: "oc_sk_"}},
		LAN:             true,
		LANKey:          "sk-lan",
		QuotaLeft:       true,
		PlainNames:      true,
		CodexAutoReset:  []string{"me@example.com"},
		ClaudeAutoReset: []string{"me@example.com"},
		TextSize:        125,
		Window:          []int{900, 700},
	}
	if err := settings.Save(was); err != nil {
		t.Fatal(err)
	}
	was = settings.Load()
	rec := httptest.NewRecorder()
	Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", strings.NewReader(`{"theme":"dark"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	now := settings.Load()
	if now.Theme != "dark" {
		t.Fatalf("theme %q", now.Theme)
	}
	wv, nv, typ := reflect.ValueOf(was), reflect.ValueOf(now), reflect.TypeOf(was)
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if sent[name] {
			continue
		}
		if wv.Field(i).IsZero() {
			t.Errorf("%s: not sent by the Settings page; set it in this test and keep it in POST /api/settings", typ.Field(i).Name)
			continue
		}
		if !reflect.DeepEqual(wv.Field(i).Interface(), nv.Field(i).Interface()) {
			t.Errorf("%s: %v after a save of the Settings page, was %v", typ.Field(i).Name, nv.Field(i).Interface(), wv.Field(i).Interface())
		}
	}
}
