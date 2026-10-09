package profile

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// modelsHome is the sandbox with a provider of three models and Codex set up
// with magpie as its provider, whose model list magpie writes into
// magpie-models.json, and the agents' files following a change of the
// catalog as they do in magpie (main sets catalog.Changed).
func modelsHome(t *testing.T) (catalogFile string) {
	t.Helper()
	h := sandbox(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2", "m3"}}); err != nil {
		t.Fatal(err)
	}
	catalogFile = filepath.Join(h, ".codex", "magpie-models.json")
	write(t, filepath.Join(h, ".codex", "config.toml"), "model = \"relay/m1\"\nmodel_provider = \"magpie\"\nmodel_catalog_json = \""+filepath.ToSlash(catalogFile)+"\"\n\n[model_providers.magpie]\nname = \"magpie\"\nbase_url = \"http://127.0.0.1:1/v1\"\nwire_api = \"responses\"\n")
	was := catalog.Changed
	catalog.Changed = agent.SyncCatalog
	t.Cleanup(func() { catalog.Changed = was })
	catalog.Touched()
	return catalogFile
}

// lists is which of the relay's models magpie-models.json gives Codex.
func lists(t *testing.T, file string) []string {
	t.Helper()
	var out []string
	for _, m := range []string{"relay/m1", "relay/m2", "relay/m3"} {
		if strings.Contains(read(t, file), `"`+m+`"`) {
			out = append(out, m)
		}
	}
	return out
}

// A profile keeps each agent's model list beside its fields (#1368):
// switching profiles switches which models Codex lists — the ones taken
// out, the mode of showing only the ones picked, the order — and writes
// Codex's model list again, as a pick made by hand on the Agents page does.
func TestProfileSwitchesModelList(t *testing.T) {
	file := modelsHome(t)
	if got := lists(t, file); len(got) != 3 {
		t.Fatalf("set up: %v in\n%s", got, read(t, file))
	}

	// 家里: two taken out
	if err := provider.SetHiddenModels("codex", []string{"relay/m2", "relay/m3"}); err != nil {
		t.Fatal(err)
	}
	if err := Save("home", must[Profile](t)(Snapshot())); err != nil {
		t.Fatal(err)
	}
	// 公司: every one shown, put in an order
	if err := provider.SetHiddenModels("codex", nil); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelOrder("codex", []string{"relay/m3"}); err != nil {
		t.Fatal(err)
	}
	if err := Save("work", must[Profile](t)(Snapshot())); err != nil {
		t.Fatal(err)
	}
	// only the ones picked: m2, and m1 Codex is on, which the Agents page
	// keeps among them
	if err := provider.SetOnlyPicked("codex", true); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetPickedModels("codex", []string{"relay/m1", "relay/m2"}); err != nil {
		t.Fatal(err)
	}
	if err := Save("picked", must[Profile](t)(Snapshot())); err != nil {
		t.Fatal(err)
	}

	ps := must[map[string]Profile](t)(Load())
	a := must[Applied](t)(Apply(ps["home"]))
	if a.Changed == 0 {
		t.Error("applying home changed nothing")
	}
	s := settings.Load()
	if _, only := s.PickedModels["codex"]; only || !slices.Equal(s.HiddenModels["codex"], []string{"relay/m2", "relay/m3"}) || len(s.OrderedModels["codex"]) != 0 {
		t.Fatalf("home: hidden %v picked %v order %v", s.HiddenModels, s.PickedModels, s.OrderedModels)
	}
	if got := lists(t, file); !slices.Equal(got, []string{"relay/m1"}) {
		t.Fatalf("home: Codex lists %v\n%s", got, read(t, file))
	}

	must[Applied](t)(Apply(ps["work"]))
	s = settings.Load()
	if len(s.HiddenModels["codex"]) != 0 || !slices.Equal(s.OrderedModels["codex"], []string{"relay/m3"}) {
		t.Fatalf("work: hidden %v order %v", s.HiddenModels, s.OrderedModels)
	}
	if got := lists(t, file); len(got) != 3 {
		t.Fatalf("work: Codex lists %v\n%s", got, read(t, file))
	}
	if b := read(t, file); strings.Index(b, `"relay/m3"`) > strings.Index(b, `"relay/m1"`) {
		t.Errorf("work: m3 not put first\n%s", b)
	}

	must[Applied](t)(Apply(ps["picked"]))
	if on, only := provider.PickedModels("codex"); !only || len(on) != 2 || !on["relay/m1"] || !on["relay/m2"] {
		t.Fatalf("picked: %v %v", on, only)
	}
	if got := lists(t, file); !slices.Equal(got, []string{"relay/m1", "relay/m2"}) {
		t.Fatalf("picked: Codex lists %v", got)
	}

	// applied again, nothing to change in the list
	if a := must[Applied](t)(Apply(ps["picked"])); a.Changed != 0 {
		t.Errorf("applied twice: %d changed", a.Changed)
	}

	// what the details tell before it is applied
	for name, want := range map[string]Models{"home": {Hidden: 2}, "work": {}, "picked": {Only: true, Picked: 2}} {
		var got *Models
		for _, g := range Details(ps[name]) {
			if g.ID == "codex" {
				got = g.Models
			}
		}
		if got == nil || *got != want {
			t.Errorf("%s details: %+v, want %+v", name, got, want)
		}
	}
}

// A profile saved before profiles kept the model lists leaves each agent's
// list as it is: a list it says nothing of is unknown, not empty, so none is
// shown every model, nor any hidden.
func TestOldProfileKeepsModelList(t *testing.T) {
	file := modelsHome(t)
	if err := provider.SetHiddenModels("codex", []string{"relay/m2"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetOnlyPicked("claude", true); err != nil {
		t.Fatal(err)
	}
	old := `{
  "work": {
    "codex.model": "relay/m1"
  }
}
`
	write(t, Path(), old)
	ps := must[map[string]Profile](t)(Load())
	if ps["work"].Models != nil {
		t.Fatalf("an old profile read with lists: %+v", ps["work"].Models)
	}
	must[Applied](t)(Apply(ps["work"]))
	s := settings.Load()
	if !slices.Equal(s.HiddenModels["codex"], []string{"relay/m2"}) {
		t.Errorf("codex's hidden: %v", s.HiddenModels["codex"])
	}
	if _, only := s.PickedModels["claude"]; !only {
		t.Errorf("claude's only-picked lost: %v", s.PickedModels)
	}
	if got := lists(t, file); !slices.Equal(got, []string{"relay/m1", "relay/m3"}) {
		t.Errorf("Codex lists %v", got)
	}
	// and written back as it was, with no lists made up for it
	if err := store(ps); err != nil {
		t.Fatal(err)
	}
	if b := read(t, Path()); b != old {
		t.Errorf("written back as\n%s", b)
	}
	// a profile naming one agent's list leaves the others' alone
	p := Profile{Fields: map[string]string{}, Models: map[string]provider.ModelList{"codex": {}}}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var back Profile
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	must[Applied](t)(Apply(back))
	s = settings.Load()
	if len(s.HiddenModels["codex"]) != 0 {
		t.Errorf("codex kept %v", s.HiddenModels["codex"])
	}
	if _, only := s.PickedModels["claude"]; !only {
		t.Errorf("claude's list changed by a profile naming only codex's: %v", s.PickedModels)
	}
}
