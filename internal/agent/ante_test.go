package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// anteHome is a HOME with an Ante of the user's: a catalog holding a provider
// of their own and a model patch, and a settings.json naming the provider and
// model Ante starts a session on.
func anteHome(t *testing.T) (home, path, settings string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Key: "k",
		Chat: "https://api.deepseek.com/v1", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	// one model of magpie's with levels of its own, so what Ante is told of
	// them — and what it is not — is said by a real catalog entry
	if err := catalog.SaveLive("deepseek", "https://api.deepseek.com/v1", []catalog.Model{
		{ID: "pro", Efforts: []string{"none", "minimal", "low", "high", "max"}, Context: 200000, Output: 8000},
		{ID: "flash"},
	}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".ante")
	writeFile(t, filepath.Join(dir, "catalog.json"), `{
  "providers": {
    "together": {
      "display_name": "Together AI",
      "base_url": "https://api.together.xyz/v1",
      "wire_style": "OpenAiCompatible",
      "auth": { "bearer": { "env_key": "TOGETHER_API_KEY" } }
    }
  },
  "models": { "claude-opus-5-5": { "effort": "max" } }
}
`)
	settings = filepath.Join(dir, "settings.json")
	writeFile(t, settings, `{
  "provider": "together",
  "model": "moonshotai/Kimi-K2-Instruct",
  "permission_mode": "auto",
  "theme": "default"
}
`)
	return home, filepath.Join(dir, "catalog.json"), settings
}

func anteRead(t *testing.T, p string) map[string]any {
	t.Helper()
	var m map[string]any
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s is not JSON: %v\n%s", p, err, b)
	}
	return m
}

func anteProv(t *testing.T, path string) map[string]any {
	t.Helper()
	ps, _ := anteRead(t, path)["providers"].(map[string]any)
	return ps
}

func anteEntry(t *testing.T, path string) map[string]any {
	t.Helper()
	e, _ := anteProv(t, path)[magpieID].(map[string]any)
	return e
}

func anteModelList(t *testing.T, path string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, x := range anteEntry(t, path)["preferred_models"].([]any) {
		e, _ := x.(map[string]any)
		out = append(out, e)
	}
	return out
}

// Ante's catalog.json is merged on top of the providers it ships: magpie's
// entry is added to the providers map under its own id, with the user's own
// providers and the models section left as they are, and it goes again when
// magpie steps out. A file magpie made goes with it. Being in the catalog is
// not enough for Ante to use it — its settings say which provider a session
// starts on — so magpie sets those too and puts back what the user had.
func TestAnte(t *testing.T) {
	home, path, settings := anteHome(t)
	want := anteRead(t, path)
	wantSettings := anteRead(t, settings)

	a := ante(home)
	if a.Path != path || a.Dir != filepath.Dir(path) || a.Bin != "ante" {
		t.Fatalf("path %q dir %q bin %q", a.Path, a.Dir, a.Bin)
	}
	f, m := a.Field("provider"), a.Field("model")
	// not wired yet: the user's provider is there, magpie's is not, and
	// nothing is said about wiring magpie never did
	if got := f.Get(); got != "" || a.Check() != "" {
		t.Fatalf("own: %q %q", got, a.Check())
	}
	var vals []string
	for _, o := range f.Options(a.Values()) {
		vals = append(vals, o.Value)
	}
	if !reflect.DeepEqual(vals, []string{"magpie"}) {
		t.Fatalf("options: %v", vals)
	}

	for range 2 { // twice: one magpie provider, and the user's left alone
		if err := f.Set("magpie"); err != nil {
			t.Fatal(err)
		}
	}
	ps := anteProv(t, path)
	if len(ps) != 2 || !reflect.DeepEqual(ps["together"], want["providers"].(map[string]any)["together"]) {
		t.Fatalf("providers: %v", ps)
	}
	if got := anteRead(t, path)["models"]; !reflect.DeepEqual(got, want["models"]) {
		t.Fatalf("models: %v", got)
	}
	mine := anteEntry(t, path)
	if mine["base_url"] != gatewayV1() || mine["wire_style"] != "OpenAiCompatible" || mine["display_name"] != "magpie" {
		t.Fatalf("magpie provider: %v", mine)
	}
	// no auth block of magpie's: the gateway lets this machine in with any
	// token, and Ante counts a provider with none as authenticated
	if _, ok := mine["auth"]; ok {
		t.Fatalf("auth written: %v", mine["auth"])
	}
	ms := anteModelList(t, path)
	if len(ms) == 0 {
		t.Fatalf("no models: %v", mine)
	}
	for _, e := range ms {
		id, _ := e["id"].(string)
		if id == "" {
			t.Fatalf("a model without an id: %v", e)
		}
		// support_vision is never written: Ante takes a model as seeing
		// images unless told otherwise, and magpie's own answer is only as
		// good as a provider's list — a relay's models are often not
		// described — so writing false would drop the reader's images
		if _, ok := e["support_vision"]; ok {
			t.Fatalf("%s: support_vision written: %v", id, e)
		}
		// and no default effort either: written, Ante sends it on every turn,
		// and the top rung is its slowest and dearest one
		if _, ok := e["effort"]; ok {
			t.Fatalf("%s: effort written: %v", id, e)
		}
	}
	if f.Get() != "magpie" || a.Check() != "" {
		t.Fatalf("on: %q %q", f.Get(), a.Check())
	}
	// Ante starts on magpie: its provider named, and the provider and model
	// the user had kept out of the way for when magpie steps out. The model
	// goes rather than staying: Ante's settings name the two apart, so
	// together's model id beside magpie's provider would have the gateway
	// asked for a model it may not serve.
	if got := anteRead(t, settings); got["provider"] != magpieID {
		t.Fatalf("settings: %v", got)
	}
	if _, ok := anteRead(t, settings)["model"]; ok {
		t.Fatalf("the user's model left beside magpie's provider: %v", anteRead(t, settings))
	}
	for _, k := range []string{"permission_mode", "theme"} {
		if anteRead(t, settings)[k] != wantSettings[k] {
			t.Fatalf("%s lost: %v", k, anteRead(t, settings))
		}
	}

	// magpie's provider pointed elsewhere: that is said, and Sync brings it
	// back
	writeFile(t, path, strings.Replace(readFile(path), gatewayV1(), "http://elsewhere/v1", 1))
	if !strings.Contains(a.Check(), "http://elsewhere/v1") {
		t.Fatalf("check: %q", a.Check())
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := anteEntry(t, path)["base_url"]; got != gatewayV1() {
		t.Fatalf("sync: %v", got)
	}
	// Ante moved to one of its own providers: magpie's entry is still in the
	// catalog, and that is said rather than kept quiet about (#1210)
	writeFile(t, settings, `{"provider":"together","model":"moonshotai/Kimi-K2-Instruct","permission_mode":"auto","theme":"default"}`)
	if got := a.Check(); !strings.Contains(got, "together") || !strings.Contains(got, "no longer asks magpie") {
		t.Fatalf("moved off magpie: %q", got)
	}

	// stepping out takes magpie's provider away, leaves the user's file as it
	// was, and gives Ante back the provider the user had — the one moved off
	// to there being their own choice, which stands
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if got := anteRead(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("restore: %v", got)
	}
	if got := anteRead(t, settings); got["provider"] != "together" {
		t.Fatalf("restore settings: %v", got)
	}
	if f.Get() != "" || m.Get() != "" || a.Check() != "" {
		t.Fatalf("off: %q %q %q", f.Get(), m.Get(), a.Check())
	}

	// a pair of files magpie made go again, and what Ante had before them was
	// nothing: its auto-detection, not a made-up choice
	os.Remove(path)
	os.Remove(settings)
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if _, ok := anteProv(t, path)[magpieID]; !ok {
		t.Fatalf("new: %v", anteRead(t, path))
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err == nil {
		t.Fatalf("made catalog left: %s", b)
	}
	if b, err := os.ReadFile(settings); err == nil {
		t.Fatalf("made settings left: %s", b)
	}
}

// The model field is the same wiring spelled as a model: Ante names its
// model on its own, with the provider kept apart from it, so magpie writes
// the catalog id and its provider beside it. A model of the user's own takes
// magpie out again, and Disconnect puts back what Ante had.
func TestAnteModelField(t *testing.T) {
	home, path, settings := anteHome(t)
	a := ante(home)
	m := a.Field("model")
	if err := m.Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	got := anteRead(t, settings)
	if got["provider"] != magpieID || got["model"] != "deepseek/pro" {
		t.Fatalf("on: %v", got)
	}
	if !a.Wired() || a.Check() != "" || m.Get() != "deepseek/pro" {
		t.Fatalf("wired: %v %q %q", a.Wired(), a.Check(), m.Get())
	}
	if _, ok := anteProv(t, path)[magpieID]; !ok {
		t.Fatalf("no catalog entry: %v", anteProv(t, path))
	}
	// one of Ante's own models: magpie steps out, and Ante is left on it —
	// its provider back with it, since a bare id means nothing without one
	if err := m.Set("moonshotai/Kimi-K2-Instruct"); err != nil {
		t.Fatal(err)
	}
	got = anteRead(t, settings)
	if got["provider"] != "together" || got["model"] != "moonshotai/Kimi-K2-Instruct" {
		t.Fatalf("own: %v", got)
	}
	if _, ok := anteProv(t, path)[magpieID]; ok {
		t.Fatalf("magpie's entry left: %v", anteProv(t, path))
	}
	if a.Wired() || m.Get() != "" {
		t.Fatalf("still wired: %v %q", a.Wired(), m.Get())
	}
	// Disconnect finds nothing left to undo and writes nothing new
	was := readFile(settings)
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(settings); got != was {
		t.Fatalf("rewritten by Disconnect:\n%s\n---\n%s", was, got)
	}
}

// Connect starts Ante on magpie and Disconnect gives Ante back what it had:
// the save-and-restore the catalog entry alone never was (#1210).
func TestAnteConnectDisconnect(t *testing.T) {
	home, path, settings := anteHome(t)
	a := ante(home)
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	if !a.Wired() {
		t.Fatal("not wired after Connect")
	}
	got := anteRead(t, settings)
	if got["provider"] != magpieID {
		t.Fatalf("Ante starts on %v, not magpie: %v", got["provider"], got)
	}
	if got["model"] == nil || got["model"] == "moonshotai/Kimi-K2-Instruct" {
		t.Fatalf("Connect left the user's model beside magpie's provider: %v", got)
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("drift after Connect: %+v", d)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	got = anteRead(t, settings)
	if got["provider"] != "together" || got["model"] != "moonshotai/Kimi-K2-Instruct" {
		t.Fatalf("not put back: %v", got)
	}
	if _, ok := anteProv(t, path)[magpieID]; ok {
		t.Fatalf("magpie's entry left: %v", anteProv(t, path))
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("drift after Disconnect: %+v", d)
	}
}

// What the user set in Ante itself is what comes back: a provider picked in
// Ante's /providers while magpie's entry stands is their choice, and magpie
// does not hand over the one it found when it wired in.
func TestAnteKeepsWhatTheUserPickedWhileWired(t *testing.T) {
	home, path, settings := anteHome(t)
	a := ante(home)
	f := a.Field("provider")
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	// the user moves to their own provider, and to one of magpie's models
	// there, in Ante's picker
	writeFile(t, settings, `{"provider":"together","model":"x-ai/grok-4.7","theme":"default"}`+"\n")
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	got := anteRead(t, settings)
	if got["provider"] != "together" || got["model"] != "x-ai/grok-4.7" {
		t.Fatalf("the user's pick was overwritten: %v", got)
	}
	if got["theme"] != "default" {
		t.Fatalf("the user's other keys: %v", got)
	}
	if _, ok := anteProv(t, path)[magpieID]; ok {
		t.Fatalf("magpie's entry left: %v", anteProv(t, path))
	}
}

// sameText is whether two reads of the same key say the same thing.
// edit.GetJSON hands back an object as JSON and a scalar unquoted (900,
// concise), so a JSON compare alone misses the second: both spellings are
// tried. Indentation is the file's own, not a change of what the user wrote.
func sameText(a, b string) bool {
	if a == b {
		return true
	}
	if sameJSON(a, json.RawMessage(b)) || sameJSON(b, json.RawMessage(a)) {
		return true
	}
	return strings.TrimSpace(a) == strings.TrimSpace(b)
}

// anteEffortModel is a provider of magpie's with one model at the levels c
// names, and the catalog entry Ante is handed for it.
func anteEffortModel(t *testing.T, id string, efforts []string) map[string]any {
	t.Helper()
	if err := catalog.SaveLive("eff", "https://example.test/v1", []catalog.Model{
		{ID: id, Efforts: efforts},
	}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "eff", Name: "Eff", Key: "k",
		Chat: "https://example.test/v1", Models: []string{id}}); err != nil {
		t.Fatal(err)
	}
	for _, m := range magpieModels("ante") {
		if m.ID == "eff/"+id {
			e, _ := json.Marshal(anteModelJSON(m))
			var out map[string]any
			if err := json.Unmarshal(e, &out); err != nil {
				t.Fatal(err)
			}
			return out
		}
	}
	t.Fatalf("eff/%s is not in magpie's catalog", id)
	return nil
}

// The effort levels magpie knows are put on Ante's ladder, and none of them
// is a default: Ante's lowest rung, min, is the level it sends as
// `reasoning_effort: minimal` — magpie's "minimal", never its "none" — and a
// model magpie knows only "none" of says so with an empty list, which is
// Ante's own way of saying the model takes no effort setting (#1210).
func TestAnteEfforts(t *testing.T) {
	for _, c := range []struct {
		name   string
		levels []string
		want   []string
	}{
		{"a ladder", []string{"low", "medium", "high"}, []string{"low", "medium", "high"}},
		{"minimal is Ante's min", []string{"minimal", "high"}, []string{"min", "high"}},
		{"none is no rung of Ante's", []string{"none", "high"}, []string{"high"}},
		{"none alone takes no effort", []string{"none"}, []string{}},
		{"magpie's order, Ante's ladder", []string{"max", "low", "none", "minimal"}, []string{"min", "low", "max"}},
		{"all of them", []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"},
			[]string{"min", "low", "medium", "high", "xhigh", "max"}},
		{"nothing known", nil, []string{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := anteEfforts(c.levels)
			if len(got) != len(c.want) {
				t.Fatalf("anteEfforts(%v) = %v, want %v", c.levels, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("anteEfforts(%v) = %v, want %v", c.levels, got, c.want)
				}
			}
		})
	}
	// and what magpie writes into the catalog for each of them
	for _, c := range []struct {
		efforts []string
		want    string // supported_efforts as JSON; "" for the key not written
	}{
		{[]string{"low", "medium", "high"}, `["low","medium","high"]`},
		{[]string{"minimal", "low"}, `["min","low"]`},
		{[]string{"none"}, `[]`},
		{nil, ""},
	} {
		e := anteEffortModel(t, "one", c.efforts)
		got, written := "", false
		if v, ok := e["supported_efforts"]; ok {
			b, _ := json.Marshal(v)
			got, written = string(b), true
		}
		if written != (c.want != "") || got != c.want {
			t.Fatalf("efforts %v: supported_efforts %q (%v), want %q", c.efforts, got, written, c.want)
		}
		if _, ok := e["effort"]; ok {
			t.Fatalf("efforts %v: a default effort written: %v", c.efforts, e)
		}
	}
}

// Sync and a pick keep the keys the user added inside magpie's own entry —
// the auth block a gateway shared on the local network or reached from WSL
// needs to send a key, its headers, its extra body, a longer idle timeout —
// while the endpoint and the whole model list still follow the catalog
// (#1103, #1210).
func TestAnteSyncKeepsTheUsersKeys(t *testing.T) {
	const theirs = `{
  "providers": {
    "together": { "base_url": "https://api.together.xyz/v1", "wire_style": "OpenAiCompatible" },
    "magpie": {
      "display_name": "old",
      "base_url": "http://127.0.0.1:1/v1",
      "wire_style": "OpenAiCompatible",
      "auth": { "bearer": { "env_key": "MAGPIE_KEY" } },
      "http_headers": { "X-Org": "mine" },
      "extra_body": { "service_tier": "priority" },
      "stream_idle_timeout_secs": 900,
      "thinking_display": "concise",
      "preferred_models": [
        { "id": "deepseek/flash", "user_note": "keep me" }
      ]
    }
  },
  "models": { "claude-opus-5-5": { "effort": "max" } }
}
`
	for _, action := range []string{"sync", "pick"} {
		t.Run(action, func(t *testing.T) {
			home, path, settings := anteHome(t)
			a := ante(home)
			f := a.Field("provider")
			if err := f.Set("magpie"); err != nil {
				t.Fatal(err)
			}
			writeFile(t, path, theirs)
			kept := map[string]string{}
			for _, k := range []string{"auth", "http_headers", "extra_body", "stream_idle_timeout_secs", "thinking_display"} {
				kept[k], _ = edit.GetJSON(path, anteProvider+"."+k)
			}
			check := func() {
				t.Helper()
				for k, was := range kept {
					if v, ok := edit.GetJSON(path, anteProvider+"."+k); !ok || !sameText(v, was) {
						t.Errorf("the user's %s lost or changed: got %q, want %q", k, v, was)
					}
				}
				if v, _ := edit.GetJSON(path, anteProvider+".preferred_models"); strings.Contains(v, "user_note") {
					t.Errorf("preferred_models merged instead of replaced: %s", v)
				}
				for k, want := range map[string]string{
					"providers.together.base_url":   "https://api.together.xyz/v1",
					"models.claude-opus-5-5.effort": "max",
				} {
					if v, ok := edit.GetJSON(path, k); !ok || v != want {
						t.Errorf("%s = %q (%v), want %q", k, v, ok, want)
					}
				}
				// the fields magpie owns still follow the gateway
				for k, want := range map[string]string{"base_url": gatewayV1(), "display_name": "magpie", "wire_style": "OpenAiCompatible"} {
					if v, _ := edit.GetJSON(path, anteProvider+"."+k); v != want {
						t.Errorf("%s = %q, want %q", k, v, want)
					}
				}
				// and every model magpie lists is in Ante's picker, and only
				// those: flash, gone from the catalog, left the list
				got := anteModelList(t, path)
				want := magpieModels("ante")
				if len(got) != len(want) {
					t.Fatalf("preferred_models: %v, want %d models", got, len(want))
				}
				for i, e := range got {
					if e["id"] != want[i].ID {
						t.Fatalf("preferred_models[%d] = %v, want %s", i, e, want[i].ID)
					}
				}
			}
			if action == "sync" {
				if err := a.Sync(); err != nil {
					t.Fatal(err)
				}
			} else if err := f.Set("magpie"); err != nil {
				t.Fatal(err)
			}
			check()
			// Ante still starts on magpie, and names no model of
			// together's beside it — the default of preferred_models, one of
			// magpie's, is what it starts on
			if v, _ := edit.GetJSON(settings, "provider"); v != magpieID {
				t.Errorf("settings' provider = %q, want magpie", v)
			}
			if v, ok := edit.GetJSON(settings, "model"); ok && v != "" {
				t.Errorf("settings' model = %q, want none", v)
			}
			// a catalog that says the same as it does is not written again
			before := readFile(path)
			stamp := time.Unix(1000000000, 0)
			if err := os.Chtimes(path, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			if err := a.Sync(); err != nil {
				t.Fatal(err)
			}
			if st, err := os.Stat(path); err != nil {
				t.Fatal(err)
			} else if !st.ModTime().Equal(stamp) || readFile(path) != before {
				t.Error("an unchanged catalog was rewritten")
			}
			// a model gone from the catalog leaves Ante's list
			if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Key: "k",
				Chat: "https://api.deepseek.com/v1", Models: []string{"pro"}}); err != nil {
				t.Fatal(err)
			}
			if err := a.Sync(); err != nil {
				t.Fatal(err)
			}
			check()
		})
	}
}

// ANTE_HOME moves the folder Ante reads its catalog and its settings from, as
// its reference says of it: magpie writes where Ante looks, not beside it in
// the home folder (#1210).
func TestAnteHome(t *testing.T) {
	home, catalogPath, settingsPath := anteHome(t)
	elsewhere := filepath.Join(home, "elsewhere", "ante")
	t.Setenv("ANTE_HOME", elsewhere)
	a := ante(home)
	path := filepath.Join(elsewhere, "catalog.json")
	settings := filepath.Join(elsewhere, "settings.json")
	if a.Dir != elsewhere || a.Path != path {
		t.Fatalf("$ANTE_HOME ignored: dir %q path %q", a.Dir, a.Path)
	}
	if got := anteDir(here(home)); got != elsewhere {
		t.Fatalf("anteDir = %q, want %q", got, elsewhere)
	}
	// a relative ANTE_HOME names no folder to write under the working
	// directory (appdir's rule for every agent's path variable)
	t.Setenv("ANTE_HOME", "ante-relative")
	if got := anteDir(here(home)); got != filepath.Join(home, ".ante") {
		t.Fatalf("a relative $ANTE_HOME was taken: %q", got)
	}
	t.Setenv("ANTE_HOME", elsewhere)
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if _, ok := anteProv(t, path)[magpieID]; !ok {
		t.Fatalf("nothing at $ANTE_HOME: %v", anteProv(t, path))
	}
	if v, _ := edit.GetJSON(settings, "provider"); v != magpieID {
		t.Fatalf("the settings at $ANTE_HOME: %q", v)
	}
	// the home folder's own .ante is left exactly as the user had it
	if b := readFile(catalogPath); strings.Contains(b, magpieID) {
		t.Fatalf("magpie written beside the home folder: %s", b)
	}
	if b := readFile(settingsPath); b != `{
  "provider": "together",
  "model": "moonshotai/Kimi-K2-Instruct",
  "permission_mode": "auto",
  "theme": "default"
}
` {
		t.Fatalf("the home folder's settings touched: %s", b)
	}
	// stepping out writes back to $ANTE_HOME, and nowhere else
	if err := a.Field("provider").Set(""); err != nil {
		t.Fatal(err)
	}
	if _, ok := anteProv(t, path)[magpieID]; ok {
		t.Fatalf("magpie left at $ANTE_HOME: %v", anteProv(t, path))
	}
	if b := readFile(catalogPath); strings.Contains(b, magpieID) {
		t.Fatalf("magpie written beside the home folder on the way out: %s", b)
	}
	// A distro's ANTE_HOME is one magpie can't read, so its Ante is ~/.ante
	// there, as MiniMax Code's mcode (see wsl.go).
	t.Setenv("ANTE_HOME", elsewhere)
	wslHome := filepath.Join(home, "wsl")
	// a distro's place spells its paths as the distro names them (wsl.go),
	// which is also how magpie knows it can't read the distro's variables
	in := anteIn(place{home: wslHome, id: "ante@wsl:Distro", spell: func(p string) string { return p }})
	if want := filepath.Join(wslHome, ".ante", "catalog.json"); in.Path != want {
		t.Fatalf("a distro's Ante: %q, want %q", in.Path, want)
	}
}

// Stepping out writes Ante's settings and then its catalog. When the catalog
// can't be written, the settings go back as they were: an agent left with
// magpie's provider taken out of its catalog, or the other way round, is one
// that asks a provider it no longer has. (A catalog that isn't JSON is left
// untouched rather than edited by what gjson of it could read — internal/edit
// since #1242 — which is how this fails one step in.)
func TestAnteWriteFailureRollsBothFilesBack(t *testing.T) {
	home, path, settings := anteHome(t)
	a := ante(home)
	f := a.Field("provider")
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	beforeSettings := readFile(settings)
	if v, _ := edit.GetJSON(settings, "provider"); v != magpieID {
		t.Fatalf("not wired: %q", v)
	}
	bad := `{"providers": {"magpie": {"base_url": "http://127.0.0.1:1/v1"`
	writeFile(t, path, bad)
	if err := f.Set(""); err == nil {
		t.Fatal("a catalog that can't be written was stepped over")
	}
	if got := readFile(path); got != bad {
		t.Fatalf("the broken catalog was edited:\n%s\n---\n%s", bad, got)
	}
	if got := readFile(settings); got != beforeSettings {
		t.Fatalf("settings.json kept half of a change that failed:\n%s\n---\n%s", beforeSettings, got)
	}
}

// The same pair is written coming the other way: a settings.json that can't be
// written leaves the catalog as it was, and Ante is not left started on a
// provider magpie took back out.
func TestAnteConnectWriteFailureRollsBack(t *testing.T) {
	home, path, settings := anteHome(t)
	before := readFile(path)
	// nothing of it is written over, so the first settings write fails
	bad := `{"provider": "together",`
	writeFile(t, settings, bad)
	if err := ante(home).Field("provider").Set("magpie"); err == nil {
		t.Fatal("a settings.json that can't be written was stepped over")
	}
	if got := readFile(path); got != before {
		t.Fatalf("the catalog kept half of a change that failed:\n%s\n---\n%s", before, got)
	}
	if _, ok := anteProv(t, path)[magpieID]; ok {
		t.Fatalf("magpie's entry stayed beside settings magpie never wrote: %v", anteProv(t, path))
	}
	if got := readFile(settings); got != bad {
		t.Fatalf("the broken settings.json was edited:\n%s\n---\n%s", bad, got)
	}
}

// A catalog Ante can't parse costs magpie's provider, not the user's: Ante
// skips an entry that doesn't deserialize and loads the rest, and magpie
// writes only its own key.
func TestAnteKeepsTheUsersFile(t *testing.T) {
	home, path, _ := anteHome(t)
	a := ante(home)
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	ps := anteProv(t, path)
	if _, ok := ps["together"]; !ok {
		t.Fatalf("the user's provider went: %v", ps)
	}
	if _, ok := ps[magpieID]; !ok {
		t.Fatalf("magpie's provider went: %v", ps)
	}
}

// A routing group says neither what it reasons at nor what it takes in: which
// member answers, and what that member can do, is the group's own to decide
// per turn. magpie's other agents leave both off for one, and Ante would
// otherwise filter images out before a group whose member takes them, or ask
// for an effort the member the group picked doesn't have.
func TestAnteGroupSaysNoCapabilities(t *testing.T) {
	home, path, _ := anteHome(t)
	// a group of magpie's own, over a model of a provider of the user's
	provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k",
		Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}})
	if err := provider.SaveGroup(provider.Group{ID: "g", Name: "G", Members: []string{"relay/m1"}}); err != nil {
		t.Fatal(err)
	}
	a := ante(home)
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	var sawGroup bool
	for _, e := range anteModelList(t, path) {
		id, _ := e["id"].(string)
		if !strings.HasPrefix(id, provider.GroupPrefix) {
			continue
		}
		sawGroup = true
		for _, k := range []string{"effort", "supported_efforts", "support_vision"} {
			if _, ok := e[k]; ok {
				t.Fatalf("the group %s declares %s: %v", id, k, e)
			}
		}
	}
	if !sawGroup {
		t.Fatalf("no group in the list: %v", anteModelList(t, path))
	}
}
