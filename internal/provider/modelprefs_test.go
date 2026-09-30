package provider

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// prefsHome is a sandbox home where two providers serve the same model,
// which models.dev names and gives four reasoning levels.
func prefsHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	// a `security` that finds nothing: no Keychain of the user's is read
	if runtime.GOOS != "windows" {
		bin := t.TempDir()
		os.WriteFile(filepath.Join(bin, "security"), []byte("#!/bin/sh\nexit 44\n"), 0o755)
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	data := `{"a":{"models":{"sol":{"id":"sol","name":"Sol","reasoning_options":[{"type":"effort","values":["low","medium","high","max"]}]}}},
		"b":{"models":{"sol":{"id":"sol","name":"Sol","reasoning_options":[{"type":"effort","values":["low","medium","high","max"]}]}}}}`
	if err := os.WriteFile(catalog.CachePath(), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	for _, id := range []string{"a", "b"} {
		if err := Save(Provider{ID: id, Name: strings.ToUpper(id), Catalog: id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"sol"}}); err != nil {
			t.Fatal(err)
		}
	}
}

func entry(t *testing.T, id string) Entry {
	t.Helper()
	for _, e := range Catalog() {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("no %s in the catalog", id)
	return Entry{}
}

// A model's name of the user's is kept by provider/model in settings, is
// what the catalog and the lists agents get call it, leaves the same model
// from another provider (and the group they make) alone, and goes when
// reset. The agents are told each time.
func TestModelName(t *testing.T) {
	prefsHome(t)
	touched := 0
	catalog.Changed = func() { touched++ }
	t.Cleanup(func() { catalog.Changed = nil })

	if err := SetModelName("a/sol", "  My   Sol "); err != nil {
		t.Fatal(err)
	}
	if touched != 1 {
		t.Fatalf("agents told %d times", touched)
	}
	if got := settings.Load().ModelNames["a/sol"]; got != "My Sol" {
		t.Fatalf("settings hold %q", got)
	}
	if n, ok := ModelName("a", "sol"); !ok || n != "My Sol" {
		t.Fatal(n, ok)
	}
	a, b := entry(t, "a/sol"), entry(t, "b/sol")
	// a custom name still carries its provider, the same as the vendor's
	// own name does, so the picker can still be told apart by vendor
	if a.Name != "My Sol" || a.Default != "Sol" || a.Label() != "My Sol · A" || a.Model != "sol" {
		t.Fatalf("%+v %q", a, a.Label())
	}
	if b.Name != "Sol" || b.Default != "" || b.Label() != "Sol · B" {
		t.Fatalf("%+v %q", b, b.Label())
	}
	if g := entry(t, "group/auto-sol"); g.Name != "Sol" {
		t.Fatalf("the group took a provider's name: %+v", g)
	}
	if p, m, ok := Resolve("a/sol"); !ok || p.ID != "a" || m != "sol" {
		t.Fatal("a named model no longer resolves", p.ID, m, ok)
	}
	var listed []string
	for _, m := range CodexListed() {
		listed = append(listed, m.ID+"="+m.Name)
	}
	if !slices.Contains(listed, "a/sol=My Sol · A") || !slices.Contains(listed, "b/sol=Sol · B") {
		t.Fatal(listed)
	}

	// unless the custom name already says the provider: it isn't repeated
	if err := SetModelName("a/sol", "Sol on A"); err != nil {
		t.Fatal(err)
	}
	if a := entry(t, "a/sol"); a.Label() != "Sol on A" {
		t.Fatalf("provider repeated: %+v %q", a, a.Label())
	}
	if got := (Provider{ID: "a"}).ModelNames(); got["sol"] != "Sol on A" || len(got) != 1 {
		t.Fatal(got)
	}

	// a provider's name, or a Find by it, is the same provider
	if err := SetModelName("B/sol", "Sol from B"); err != nil {
		t.Fatal(err)
	}
	if b := entry(t, "b/sol"); b.Name != "Sol from B" {
		t.Fatalf("%+v", b)
	}
	// a rename takes its names with it
	if err := Rename("b", "c"); err != nil {
		t.Fatal(err)
	}
	if c := entry(t, "c/sol"); c.Name != "Sol from B" {
		t.Fatalf("%+v", c)
	}
	if _, ok := settings.Load().ModelNames["b/sol"]; ok {
		t.Fatal("the old id's name stayed")
	}

	if err := SetModelName("a/sol", ""); err != nil {
		t.Fatal(err)
	}
	if a := entry(t, "a/sol"); a.Name != "Sol" || a.Default != "" || a.Label() != "Sol · A" {
		t.Fatalf("%+v", a)
	}
	if _, ok := settings.Load().ModelNames["a/sol"]; ok {
		t.Fatal("a reset name is still kept")
	}
	for _, bad := range []string{"sol", "nobody/sol", "group/auto-sol", "a/", "a/no-such-model"} {
		if err := SetModelName(bad, "x"); err == nil {
			t.Errorf("%s was named", bad)
		}
	}
}

// Keeping some of a model's reasoning levels offers only those, in the
// vendor's order, where magpie lists it (and so in the groups it is in);
// asking for one it hasn't fails, and all of them, or none, is a reset.
func TestModelEfforts(t *testing.T) {
	prefsHome(t)
	if err := SetModelEfforts("a/sol", []string{"high", "low", "high"}); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load().ModelEfforts["a/sol"]; !slices.Equal(got, []string{"low", "high"}) {
		t.Fatal(got)
	}
	if got := entry(t, "a/sol").Efforts; !slices.Equal(got, []string{"low", "high"}) {
		t.Fatal(got)
	}
	if got := entry(t, "b/sol").Efforts; !slices.Equal(got, []string{"low", "medium", "high", "max"}) {
		t.Fatal(got)
	}
	if got := entry(t, "group/auto-sol").Efforts; !slices.Equal(got, []string{"low", "high"}) {
		t.Fatal(got)
	}
	// the vendor is still asked at any level it has
	if got := (Provider{ID: "a", Catalog: "a"}).Efforts("sol"); len(got) != 4 {
		t.Fatal(got)
	}
	if err := SetModelEfforts("a/sol", []string{"ultra"}); err == nil {
		t.Fatal("kept a level the model hasn't")
	}
	if err := SetModelEfforts("a/sol", []string{"low", "medium", "high", "max"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := settings.Load().ModelEfforts["a/sol"]; ok {
		t.Fatal("every level kept is still a narrowing")
	}
	SetModelEfforts("a/sol", []string{"max"})
	if err := SetModelEfforts("a/sol", nil); err != nil {
		t.Fatal(err)
	}
	if got := entry(t, "a/sol").Efforts; len(got) != 4 {
		t.Fatal(got)
	}
	// levels kept that the vendor no longer has offer them all
	if got := effortsKept([]string{"low", "high"}, []string{"max"}); !slices.Equal(got, []string{"low", "high"}) {
		t.Fatal(got)
	}
}

// A model whose levels aren't known can be given some, which the catalog,
// the lists agents get and the gateway then take it to have; none takes
// them away, and a model with levels of its own takes no others.
func TestModelEffortsGiven(t *testing.T) {
	prefsHome(t)
	if err := Save(Provider{ID: "c", Name: "C", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"mystery-7"}}); err != nil {
		t.Fatal(err)
	}
	p, _, _ := Resolve("c/mystery-7")
	if e := entry(t, "c/mystery-7"); len(e.Efforts) != 0 || len(p.Efforts("mystery-7")) != 0 {
		t.Fatalf("unknown model has levels %v", e.Efforts)
	}
	if err := SetModelEfforts("c/mystery-7", []string{"max", "low", "high"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"low", "high", "max"}
	if e := entry(t, "c/mystery-7"); !slices.Equal(e.Efforts, want) || !slices.Equal(p.Efforts("mystery-7"), want) {
		t.Fatalf("given: %v %v", e.Efforts, p.Efforts("mystery-7"))
	}
	if err := SetModelEfforts("c/mystery-7", []string{"turbo"}); err == nil {
		t.Fatal("turbo taken")
	}
	if err := SetModelEfforts("c/mystery-7", nil); err != nil {
		t.Fatal(err)
	}
	if e := entry(t, "c/mystery-7"); len(e.Efforts) != 0 || settings.Load().ModelEfforts["c/mystery-7"] != nil {
		t.Fatalf("taken away: %v", e.Efforts)
	}
	if err := SetModelEfforts("a/sol", []string{"xhigh"}); err == nil {
		t.Fatal("sol took xhigh")
	}
}

// Whether a model takes images is the vendor's answer until the user says
// otherwise. That answer is what agents and a group's image check see.
func TestModelImage(t *testing.T) {
	prefsHome(t)
	yes := true
	if err := SetModelImage("a/sol", &yes); err != nil {
		t.Fatal(err)
	}
	if e := entry(t, "a/sol"); !e.Images || e.ImageInput == nil || !*e.ImageInput {
		t.Fatalf("a sees %+v", e.ImageInput)
	}
	if e := entry(t, "b/sol"); e.Images || (e.ImageInput != nil && *e.ImageInput) {
		t.Fatalf("b changed %+v images %v", e.ImageInput, e.Images)
	}
	no := false
	if err := SetModelImage("a/sol", &no); err != nil {
		t.Fatal(err)
	}
	if e := entry(t, "a/sol"); e.Images || e.ImageInput == nil || *e.ImageInput {
		t.Fatalf("a text %+v", e.ImageInput)
	}
	if err := SetModelImage("a/sol", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := settings.Load().ModelImages["a/sol"]; ok {
		t.Fatal("override kept")
	}
	if err := Save(Provider{ID: "c", Name: "C", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"mystery-7"}}); err != nil {
		t.Fatal(err)
	}
	if e := entry(t, "c/mystery-7"); e.ImageInput != nil {
		t.Fatalf("unknown said %v", *e.ImageInput)
	}
	if err := SetModelImage("c/mystery-7", &yes); err != nil {
		t.Fatal(err)
	}
	if e := entry(t, "c/mystery-7"); !e.Images || e.ImageInput == nil || !*e.ImageInput {
		t.Fatalf("given %+v", e.ImageInput)
	}
}

// Renaming a provider moves what the user gave its models in every
// per-model map. The settings walk them by the convention themselves — each
// map[string]X of Settings named Model*, the ones there are now and any
// added later — and this holds the provider's own call to it: a map that
// walk leaves out, or skips as soon as an earlier one has moved something,
// is caught here instead of by a user whose overrides went on answering for
// the id the provider had. A map that isn't per-model — Visible, which is by
// agent id — is left as it was.
//
// The maps come from that walk and not from the convention written out
// again here: a copy keeps passing while the walk it copies goes on
// changing, so a map added to the settings later would be held to the copy
// and to nothing else.
func TestRenameMovesEveryPerModelMap(t *testing.T) {
	const atLeast = 3 // only a floor against a walk that found nothing at all
	s := settings.Settings{Visible: map[string][]string{"code": {"old/model", "old"}}}
	tables := map[string]reflect.Value{}
	settings.PerModelKeys(&s, func(name string, m reflect.Value) {
		m.Set(reflect.MakeMap(m.Type()))
		m.SetMapIndex(reflect.ValueOf("old/model").Convert(m.Type().Key()), reflect.New(m.Type().Elem()).Elem())
		tables[name] = m
	})
	if len(tables) < atLeast {
		t.Fatalf("%d per-model maps in the settings, expected at least %d", len(tables), atLeast)
	}
	if !renameModelPrefs(&s, "old", "new") {
		t.Fatal("renaming moved nothing")
	}
	keys := func(m reflect.Value) []string {
		var out []string
		for _, k := range m.MapKeys() {
			out = append(out, k.String())
		}
		slices.Sort(out)
		return out
	}
	for name, m := range tables {
		old, now := reflect.ValueOf("old/model").Convert(m.Type().Key()), reflect.ValueOf("new/model").Convert(m.Type().Key())
		if !m.MapIndex(now).IsValid() || m.MapIndex(old).IsValid() {
			t.Errorf("%s holds %v", name, keys(m))
		}
	}
	if got := s.Visible["code"]; !slices.Equal(got, []string{"old/model", "old"}) {
		t.Errorf("Visible, which is by agent id, was moved to %v", got)
	}
}
