package provider

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

// prefsHome is a sandbox home where two providers serve the same model,
// which models.dev names and gives four reasoning levels.
func prefsHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	// a `security` that finds nothing: no Keychain of the user's is read
	if runtime.GOOS != "windows" {
		bin := t.TempDir()
		testenv.Program(t, filepath.Join(bin, "security"), "#!/bin/sh\nexit 44\n")
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

// A wire name is given only for a model the provider really serves: a relay
// lists the model under the id magpie knows and asks for it under the
// vendor's own, but one the provider does not serve is a request magpie would
// never send, so naming it is refused where it is given, as a price and a
// limit are for the same id. "*" stands for every model of the provider
// rather than for one and is allowed; a removal is exempt, so an entry left
// for a model that has since gone can still be taken away.
func TestSetUpstreamNameRefusesAModelNotServed(t *testing.T) {
	prefsHome(t)
	// "a" serves "sol" (prefsHome)
	if err := SetUpstreamName("a/nope", "vendor-c/sol"); err == nil {
		t.Error("a name for a model the provider does not serve was accepted")
	}
	if _, ok := settings.Load().ModelWires["a/nope"]; ok {
		t.Error("a refused name was stored anyway")
	}
	// the whole-provider name stands on no one model, so it is allowed
	if err := SetUpstreamName("a/*", "vendor-c/sol"); err != nil {
		t.Errorf("a name for every model of the provider was refused: %v", err)
	}
	// a model that has since gone off the provider's list can still have the
	// name it was given taken away: the removal is what the user is after
	if err := SetUpstreamName("a/sol", "vendor-c/sol-2"); err != nil {
		t.Fatal(err)
	}
	if err := SetUpstreamName("a/sol", ""); err != nil {
		t.Errorf("taking away the name of a model now gone was refused: %v", err)
	}
	if _, ok := settings.Load().ModelWires["a/sol"]; ok {
		t.Error("the name of a model now gone was not taken away")
	}
}

// A provider magpie reaches through an agent's own backend is asked for the
// model by the id that agent already knows, with no model field for a wire
// name to change; a name given for one of its models would never be sent, and
// comparing the served model against it would read every call as swapped. It
// is refused where it is given instead, so neither the gateway nor the usage
// ledger can read one off it. A key the user gave a provider is not one of
// these and still takes a name.
func TestSetUpstreamNameRefusesAnAccountBackend(t *testing.T) {
	prefsHome(t)
	// a Kiro account with a saved key is one magpie reaches through Kiro's
	// own backend; the live list is the model it serves
	if err := Save(Provider{ID: "kiro", Name: "Kiro", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("kiro", "", []catalog.Model{{ID: "auto", Name: "Auto"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("kiro")
	if err != nil {
		t.Fatal(err)
	}
	if p.Account == nil || p.Account.Agent != "kiro" {
		t.Fatalf("the provider is not the account the test set up: %+v", p.Account)
	}
	if !p.asksOwnBackend() {
		t.Fatal("a signed-in account backend is not recognised as one")
	}
	if err := SetUpstreamName("kiro/auto", "vendor-c/auto"); err == nil {
		t.Error("a name for an account backend's model was accepted")
	}
	if _, ok := settings.Load().ModelWires["kiro/auto"]; ok {
		t.Error("a refused account-backend name was stored anyway")
	}
	// Codex is not an account backend for this purpose: it is served by the
	// ChatGPT backend, which has no provider id to key a name by
	if (Provider{Account: &Account{Agent: "codex"}}).asksOwnBackend() {
		t.Error("the Codex subscription was taken for an account backend")
	}
	// a key the user gave a provider is not an account backend and takes one
	if err := SetUpstreamName("a/sol", "vendor-c/sol"); err != nil {
		t.Errorf("a name for a keyed provider's model was refused: %v", err)
	}
}

// A wire name is the name of a model of a provider, not of a spelling of
// that provider's id: it is kept, looked up and taken away under the id the
// provider has now, so a ref spelled with the id it had before a rename —
// which an agent still running on an older config sends — names that same
// model, and leaves no second key behind that no lookup will ever match.
func TestSetUpstreamNameFollowsARenamedProvider(t *testing.T) {
	prefsHome(t)
	if err := SetUpstreamName("a/sol", "vendor-c/sol"); err != nil {
		t.Fatal(err)
	}
	if err := Rename("a", "c"); err != nil {
		t.Fatal(err)
	}
	p, err := Find("c")
	if err != nil {
		t.Fatal(err)
	}
	// the rename took the name with it
	if got := settings.Load().ModelWires["c/sol"]; got != "vendor-c/sol" {
		t.Fatalf("the settings hold %q", got)
	}
	// giving one by the id the provider had is giving one for that model,
	// not a name the provider is never asked for
	if err := SetUpstreamName("a/sol", "vendor-c/sol-2"); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load().ModelWires; len(got) != 1 || got["c/sol"] != "vendor-c/sol-2" {
		t.Fatalf("the settings hold %v", got)
	}
	if got := UpstreamName(*p, "sol"); got != "vendor-c/sol-2" {
		t.Fatalf("the provider is asked for %q", got)
	}
	// and taking it away by that id takes away the one in force
	if err := SetUpstreamName("a/sol", ""); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load().ModelWires; len(got) != 0 {
		t.Fatalf("the settings still hold %v", got)
	}
	if got := UpstreamName(*p, "sol"); got != "sol" {
		t.Fatalf("the provider is asked for %q", got)
	}
}

// The same for the one name given for every model of a provider: it is kept
// under the id the provider has now, and a removal by the id it had takes
// it away rather than leaving it in force under a key nothing matches.
func TestSetUpstreamNameWildcardFollowsARenamedProvider(t *testing.T) {
	prefsHome(t)
	if err := SetUpstreamName("a/*", "vendor-c/sol"); err != nil {
		t.Fatal(err)
	}
	if err := Rename("a", "c"); err != nil {
		t.Fatal(err)
	}
	p, err := Find("c")
	if err != nil {
		t.Fatal(err)
	}
	if err := SetUpstreamName("a/*", "vendor-c/sol-2"); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load().ModelWires; len(got) != 1 || got["c/*"] != "vendor-c/sol-2" {
		t.Fatalf("the settings hold %v", got)
	}
	if got := UpstreamName(*p, "sol"); got != "vendor-c/sol-2" {
		t.Fatalf("the provider is asked for %q", got)
	}
	if err := SetUpstreamName("a/*", ""); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load().ModelWires; len(got) != 0 {
		t.Fatalf("the settings still hold %v", got)
	}
	if got := UpstreamName(*p, "sol"); got != "sol" {
		t.Fatalf("the provider is asked for %q", got)
	}
}

// The provider of a ref is the provider it names, however it is spelled, as
// it already is for the setters beside this one: a name a user reaches for
// a provider by is that provider, and the wire name is kept under the id
// it has. What the key check is for is a provider id that is not spelled
// the way a provider's is — a file magpie did not write, whose keys no
// lookup goes by — and not the name the provider is shown by.
func TestSetUpstreamNameTakesTheNameAProviderIsCalledBy(t *testing.T) {
	prefsHome(t)
	// the same list "a" serves sol from in prefsHome
	if err := Save(Provider{ID: "relay", Name: "My Relay", Catalog: "a", Key: "k",
		Chat: "http://127.0.0.1:1/v1", Models: []string{"sol"}}); err != nil {
		t.Fatal(err)
	}
	const ref = "My Relay/sol"
	if err := SetUpstreamName(ref, "vendor-c/sol"); err != nil {
		t.Fatalf("a ref spelled with the name the provider is shown by was refused: %v", err)
	}
	// the setters beside this one take the very same ref
	if err := SetModelName(ref, "My Sol"); err != nil {
		t.Fatalf("%q names a model of that provider, as the others have it: %v", ref, err)
	}
	// and both names are kept under the id the provider has, which is what
	// every lookup goes by
	if got := settings.Load().ModelWires; len(got) != 1 || got["relay/sol"] != "vendor-c/sol" {
		t.Fatalf("the settings hold %v", got)
	}
	if got := settings.Load().ModelNames["relay/sol"]; got != "My Sol" {
		t.Fatalf("the settings hold the name %q", got)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if got := UpstreamName(*p, "sol"); got != "vendor-c/sol" {
		t.Fatalf("the provider is asked for %q", got)
	}
	if n, ok := ModelName("relay", "sol"); !ok || n != "My Sol" {
		t.Fatalf("the model is called %q (%t)", n, ok)
	}
}

// The key is checked where the name is given, and what it says names the
// setting being checked: the wire name a user cannot give, and not the
// price or the limit of the same model.
func TestSetUpstreamNameSaysWhichSettingItChecks(t *testing.T) {
	prefsHome(t)
	// a providers file magpie did not write: "a" under an id that is not
	// spelled the way a provider's is, which is what the check refuses
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID == "a" {
			f.Providers[i].ID = "my_relay"
		}
	}
	if err := store(f); err != nil {
		t.Fatal(err)
	}
	err := SetUpstreamName("my_relay/sol", "vendor-c/sol")
	if err == nil {
		t.Fatal("a wire name was given for a key the settings do not keep models under")
	}
	if !strings.Contains(err.Error(), "wire name") {
		t.Fatalf("the error does not say which setting it is about: %v", err)
	}
	if got := settings.Load().ModelWires; len(got) != 0 {
		t.Fatalf("a refused name was stored anyway: %v", got)
	}
}

// A row of the ledger is judged by the name in force when the ledger is
// read: a record keeps the name the vendor's reply gave and not the name
// the request went out under, so this is the only name there is to judge by,
// and giving a name after a call re-judges that call — which is what
// usage.Ledger's comment sets out as the trade. What makes it so is that the
// lookup takes the names as the settings stand at the time of reading them,
// with nothing in it that remembers what they held when the call went out,
// and this is that side of it.
func TestUpstreamNameInFollowsTheNamesAsTheyAreNow(t *testing.T) {
	prefsHome(t)
	// "a" serves "sol" (prefsHome)
	if got := UpstreamNameIn(settings.Load().ModelWires, "a", "sol"); got != "sol" {
		t.Fatalf("with no name given, a/sol is judged as %q; want sol", got)
	}
	// a call made and answered under the name magpie knows the model by,
	// before any name is given for it
	if err := SetUpstreamName("a/sol", "vendor-c/sol"); err != nil {
		t.Fatal(err)
	}
	// the same call read afterwards is judged by the name given since, so
	// the vendor's answer naming the very model that was asked for reads
	// as another one
	if got := UpstreamNameIn(settings.Load().ModelWires, "a", "sol"); got != "vendor-c/sol" {
		t.Errorf("after a name is given, a/sol is judged as %q; want vendor-c/sol", got)
	}
	// and a model with no name of its own is judged by the provider's, the
	// same way round: whatever was in force when the call went out is not
	// what a later reading of it is measured against
	if err := SetUpstreamName("a/*", "vendor-c/one"); err != nil {
		t.Fatal(err)
	}
	if got := UpstreamNameIn(settings.Load().ModelWires, "a", "other"); got != "vendor-c/one" {
		t.Errorf("a model with no name of its own is judged as %q; want the provider's", got)
	}
	if got := UpstreamNameIn(settings.Load().ModelWires, "a", "sol"); got != "vendor-c/sol" {
		t.Errorf("with both names in force, a/sol is judged as %q; want its own over the provider's", got)
	}
}

// A ref is taken at the key its name is really stored at, and the spellings
// are tried in one order: the ref as the user spelled it, the id the provider
// has or had, and last the name it is shown by — a spelling another provider
// may be shown by as well, so it is only trusted where nothing is kept under
// either of the other two. A name outlives the provider it was given for, so
// the first is where it is when that provider is gone and its id is free
// again, and it is the one a removal has to reach.
func TestDropUpstreamNameTakesTheKeyTheNameIsStoredAt(t *testing.T) {
	prefsHome(t)
	// the name a provider is shown by, under the id it has
	if err := Save(Provider{ID: "relay", Name: "My Relay", Catalog: "a", Key: "k",
		Chat: "http://127.0.0.1:1/v1", Models: []string{"sol"}}); err != nil {
		t.Fatal(err)
	}
	if err := SetUpstreamName("My Relay/sol", "vendor-c/sol"); err != nil {
		t.Fatal(err)
	}
	if dropped, err := DropUpstreamName("My Relay/sol"); err != nil || !dropped {
		t.Fatalf("DropUpstreamName = %v, %v; want the name taken off by the name its provider is shown by", dropped, err)
	}
	// an id a provider had before a rename is the id every lookup still
	// goes by, and so is the one a name is taken off at
	if err := SetUpstreamName("a/sol", "vendor-c/sol"); err != nil {
		t.Fatal(err)
	}
	if err := Rename("a", "renamed"); err != nil {
		t.Fatal(err)
	}
	if dropped, err := DropUpstreamName("a/sol"); err != nil || !dropped {
		t.Fatalf("DropUpstreamName = %v, %v; want the name taken off by the id its provider had", dropped, err)
	}
	// an id no provider is called by and no name is kept under is a mistyped
	// one: there is nothing to take off, which is not an error, and it
	// takes nothing else away on the way to saying so
	if dropped, err := DropUpstreamName("nope/sol"); dropped || err != nil {
		t.Fatalf("DropUpstreamName on an id no provider is called by = %v, %v; want no name taken and no error", dropped, err)
	}
	if got := settings.Load().ModelWires; len(got) != 0 {
		t.Errorf("the settings hold %v; a ref naming no provider took nothing off, and nothing else either", got)
	}
}

// The id a wire name was given under is free again once its provider is
// deleted, and another provider can be shown by it — the name that provider
// is shown by is a spelling any other provider may answer to, so `b/sol`
// names a provider that is not the one a name under `b/sol` is for. Taking
// the name off through it would take away that provider's own name instead,
// a name that is really in force, and leave the one that renames a
// provider's requests for a vendor that never heard of them in the file.
func TestDropUpstreamNameWorksOffTheKeyRatherThanTheProvidersName(t *testing.T) {
	prefsHome(t)
	if err := SetUpstreamName("b/sol", "vendor-c/sol"); err != nil {
		t.Fatal(err)
	}
	if err := Delete("b"); err != nil {
		t.Fatal(err)
	}
	// a provider of an id of its own, c, shown by the id b that is now
	// nobody's, and with a name of its own to be left alone
	if err := Save(Provider{ID: "c", Name: "B", Catalog: "b", Key: "k",
		Chat: "http://127.0.0.1:1/v1", Models: []string{"sol"}}); err != nil {
		t.Fatal(err)
	}
	if err := SetUpstreamName("c/sol", "vendor-d/sol"); err != nil {
		t.Fatal(err)
	}
	if dropped, err := DropUpstreamName("b/sol"); err != nil || !dropped {
		t.Fatalf("DropUpstreamName = %v, %v; want the name under b/sol taken off by that key", dropped, err)
	}
	wires := settings.Load().ModelWires
	if _, ok := wires["b/sol"]; ok {
		t.Errorf("b/sol still has a name, %v; it is in force for whichever provider takes that id next", wires)
	}
	if got := wires["c/sol"]; got != "vendor-d/sol" {
		t.Errorf("c's own name is %q; the removal went through the name c is shown by", got)
	}
	// which is the whole of what taking the name off buys: a provider
	// taking the id b is asked for its model by the name magpie knows it by
	if err := Save(Provider{ID: "b", Name: "B", Catalog: "b", Key: "k",
		Chat: "http://127.0.0.1:1/v1", Models: []string{"sol"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("b")
	if err != nil {
		t.Fatal(err)
	}
	if got := UpstreamName(*p, "sol"); got != "sol" {
		t.Errorf("the provider taking the id b is asked for %q; want sol", got)
	}
}

// HasUpstreamName answers for the key a name is really kept under, however
// it is spelled: a name that is there is not "not kept" because the ref
// that named it carried a magpie/ in front of it, which is the spelling
// every agent uses and the one the removal standing in front of it is asked
// for by.
func TestHasUpstreamNameReadsTheKeyHoweverItIsSpelled(t *testing.T) {
	prefsHome(t)
	if err := SetUpstreamName("b/sol", "vendor-c/sol"); err != nil {
		t.Fatal(err)
	}
	if err := SetUpstreamName("b/*", "vendor-c/*"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"b/sol", "magpie/b/sol", "  b/sol  ", "magpie/b/*"} {
		if !HasUpstreamName(key) {
			t.Errorf("a name is kept under %q; HasUpstreamName says none is", key)
		}
	}
	for _, key := range []string{"b/other", "magpie/b/other", "magpie/a/sol", ""} {
		if HasUpstreamName(key) {
			t.Errorf("no name is kept under %q; HasUpstreamName says one is", key)
		}
	}
}

// A name given for one model is that model's at every level, so the vendor
// is asked for the model by the name its owner gave it whichever variant
// answers for it — the name the user is told that model goes out under, and
// never a "*" the vendor never heard of. A name given for the whole provider
// is a different thing: a "*" in it is whatever a request goes out under, so
// each level is asked for by its own id and the effort keeps choosing.
func TestUpstreamNameAsGivesAModelTheNameItWasGiven(t *testing.T) {
	prefsHome(t)
	a, err := Find("a")
	if err != nil {
		t.Fatal(err)
	}
	if err := SetUpstreamName("a/sol", "vendor-c/*"); err != nil {
		t.Fatal(err)
	}
	if got, want := UpstreamName(*a, "sol"), "vendor-c/sol"; got != want {
		t.Fatalf("a/sol is asked for as %q; want %q", got, want)
	}
	if got := UpstreamNameAs(*a, "sol", "sol-high"); got != "vendor-c/sol" {
		t.Errorf("at the sol-high variant it is asked for as %q; want the model's own name, vendor-c/sol", got)
	}

	b, err := Find("b")
	if err != nil {
		t.Fatal(err)
	}
	if err := SetUpstreamName("b/*", "vendor-c/*"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ sent, want string }{
		{"sol-high", "vendor-c/sol-high"},
		{"sol", "vendor-c/sol"},
	} {
		if got := UpstreamNameAs(*b, "sol", tc.sent); got != tc.want {
			t.Errorf("with a name for the whole provider, asking for %q gives %q; want %q", tc.sent, got, tc.want)
		}
	}
}

// The name a call goes out under is the one the vendor is asked for, and on
// an Antigravity account that is not the model magpie knows: the family is
// asked for as the variant the effort picks, each by its own name. A ledger
// reading the record back therefore judges the reply against the id that
// really went out — read against the family, every level of it reads as
// another model answering.
func TestSentNameInAsksForTheVariantTheEffortPicks(t *testing.T) {
	prefsHome(t)
	// Antigravity's ids of one model at three levels, as its fetch left them
	if err := os.MkdirAll(filepath.Dir(catalog.LivePath("antigravity")), 0o755); err != nil {
		t.Fatal(err)
	}
	live := `{"models":[{"id":"flash-9-low"},{"id":"flash-9-medium"},{"id":"flash-9-high"}]}`
	if err := os.WriteFile(catalog.LivePath("antigravity"), []byte(live), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	wires := map[string]string{"antigravity/*": "vendor-c/*"}
	for _, tc := range []struct{ effort, want string }{
		{"high", "vendor-c/flash-9-high"},
		{"medium", "vendor-c/flash-9-medium"},
		{"low", "vendor-c/flash-9-low"},
		// no level asked for is the level Antigravity thinks at when it is
		// not told one, which is the highest of the family
		{"", "vendor-c/flash-9-high"},
	} {
		if got := SentNameIn(wires, "antigravity", "flash-9", tc.effort); got != tc.want {
			t.Errorf("at %q the vendor is asked for %q; want %q", tc.effort, got, tc.want)
		}
	}
	// with no name given, it is the id itself: a reply naming the level the
	// call went out at is that model, whatever family it is a level of
	if got := SentNameIn(nil, "antigravity", "flash-9", "medium"); got != "flash-9-medium" {
		t.Errorf("with no upstream name the vendor is asked for %q; want the variant", got)
	}
	// another provider has no variants of its own, so the effort is no part
	// of the name and a star given for the provider stands for the model
	if got := SentNameIn(map[string]string{"a/*": "vendor-d/*"}, "a", "sol", "high"); got != "vendor-d/sol" {
		t.Errorf("provider a at high is asked for %q; want vendor-d/sol", got)
	}
}

// The id a call goes out under is one id, whether the request named the
// family or one of its own variant ids: an agent's model list and a pick
// saved from before the families were one model still name those, and
// Antigravity serves each of them as it stands. So an effort asked for
// picks the level of the family in either case, and no effort asked for
// leaves the id that was named alone — the rule the gateway's envelope is
// built by (codeAssistID) and the rule a ledger reads a record back against
// are one rule, and these are the two ends of it.
func TestSentNameInAsksAVariantIdForWhatTheEffortPicks(t *testing.T) {
	prefsHome(t)
	// one model at three levels, and one at two, so a family's default is
	// the top of it in one case and not the other
	if err := os.MkdirAll(filepath.Dir(catalog.LivePath("antigravity")), 0o755); err != nil {
		t.Fatal(err)
	}
	live := `{"models":[{"id":"flash-9-low"},{"id":"flash-9-medium"},{"id":"flash-9-high"},` +
		`{"id":"note-4-low"},{"id":"note-4-medium"}]}`
	if err := os.WriteFile(catalog.LivePath("antigravity"), []byte(live), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	for _, tc := range []struct{ model, effort, want string }{
		// the effort the client asks for wins, as it does for the family
		{"flash-9-high", "low", "flash-9-low"},
		{"flash-9-low", "medium", "flash-9-medium"},
		// with none asked for, the id that was named goes as it is
		{"flash-9-high", "", "flash-9-high"},
		{"flash-9-low", "", "flash-9-low"},
		// an effort the family does not take is asked at the level of its
		// own nearest, not at the one the family thinks at when told none
		{"flash-9-high", "minimal", "flash-9-low"},
		{"flash-9", "xhigh", "flash-9-high"},
		// the same for a family whose default is not its top: the id asked
		// for is the one that goes out, and an effort moves it to the level
		// it picks
		{"note-4-low", "", "note-4-low"},
		{"note-4-medium", "", "note-4-medium"},
		{"note-4-low", "high", "note-4-medium"},
		{"note-4-low", "minimal", "note-4-low"},
	} {
		if got := SentNameIn(nil, "antigravity", tc.model, tc.effort); got != tc.want {
			t.Errorf("%s at %q goes out as %q; want %q", tc.model, tc.effort, got, tc.want)
		}
		if got := AntigravitySentID(tc.model, tc.effort); got != tc.want {
			t.Errorf("%s at %q: the id itself is %q, want %q", tc.model, tc.effort, got, tc.want)
		}
	}
	// a model of no family goes as it is, whatever effort is asked for
	if got := SentNameIn(nil, "antigravity", "solo", "high"); got != "solo" {
		t.Errorf("a model of no family is asked for as %q; want solo", got)
	}
}

// The variants belong to the account and not to the provider: a model is
// asked for one of its levels only where the account behind the provider is
// the one serving that family. The two are separate questions wherever a
// provider answers to an id that is not its account's — an account provider is
// built with its account's own id today, so reading one for the other agrees
// and says nothing about the rule. So an account the user is signed in to
// under a provider of its own id is asked for the variant, and a relay added
// under a built-in account's id is that relay and is asked for the model
// magpie knows, which is what the gateway's own dispatch (codeAssistID) reads
// the same way.
func TestSentNameIsJudgedByTheAccountAndNotByTheProviderId(t *testing.T) {
	prefsHome(t)
	if err := os.MkdirAll(filepath.Dir(catalog.LivePath("antigravity")), 0o755); err != nil {
		t.Fatal(err)
	}
	live := `{"models":[{"id":"flash-9-low"},{"id":"flash-9-high"}]}`
	if err := os.WriteFile(catalog.LivePath("antigravity"), []byte(live), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	wires := map[string]string{"relay-1/*": "vendor-c/*", "antigravity/*": "vendor-d/*"}

	// the account is the one serving the family, whatever id the provider
	// it is on answers to
	if got := SentNameOnIn(wires, "relay-1", "antigravity", "flash-9", "low"); got != "vendor-c/flash-9-low" {
		t.Errorf("an Antigravity account on a provider of another id, at low: the vendor is asked for %q; want vendor-c/flash-9-low", got)
	}
	// and the provider's id is no part of the question: a relay the user
	// added under a built-in account's id is that relay, and no level of
	// the family is what a call on it goes out under
	if got := SentNameOnIn(wires, "antigravity", "", "flash-9", "low"); got != "vendor-d/flash-9" {
		t.Errorf("a relay under the account's own id, at low: the vendor is asked for %q; want vendor-d/flash-9", got)
	}
	// another account is asked for the model magpie knows as well
	if got := SentNameOnIn(wires, "relay-1", "gemini", "flash-9", "low"); got != "vendor-c/flash-9" {
		t.Errorf("another account on the same provider, at low: the vendor is asked for %q; want vendor-c/flash-9", got)
	}
	// a record keeps the id the call went out on and no account, and that id
	// is the account's own, so the ledger reads it back the same way — and a
	// record of a relay, whose id is no account's, by that id
	if got := SentNameIn(wires, "antigravity", "flash-9", "low"); got != "vendor-d/flash-9-low" {
		t.Errorf("a record of a call on the account's own provider, at low, reads back as %q; want vendor-d/flash-9-low", got)
	}
	if got := SentNameIn(wires, "relay-1", "flash-9", "low"); got != "vendor-c/flash-9" {
		t.Errorf("a record of a call on a relay, at low, reads back as %q; want vendor-c/flash-9", got)
	}
}

// A name that is only whitespace is no name at either level of the lookup,
// as it is in UpstreamNameIn: the model's own falls through to the
// provider's, and both falling through leaves the request under the id it
// was built with. A blank name read as a name would send a relay an id of
// spaces.
func TestUpstreamNameAsInIgnoresANameOfOnlyWhitespace(t *testing.T) {
	for _, tc := range []struct {
		what       string
		wires      map[string]string
		sent, want string
	}{
		{"the model's own name is blank, the provider's is not",
			map[string]string{"relay/sol": "   ", "relay/*": "vendor-c/*"}, "sol-high", "vendor-c/sol-high"},
		{"both are blank",
			map[string]string{"relay/sol": "  ", "relay/*": "\t"}, "sol-high", "sol-high"},
		{"the provider's is blank and no other model has one",
			map[string]string{"relay/sol": "   ", "relay/*": " "}, "sol-high", "sol-high"},
		{"the request goes out under the model itself",
			map[string]string{"relay/sol": "   ", "relay/*": "vendor-c/*"}, "sol", "vendor-c/sol"},
	} {
		if got := UpstreamNameAsIn(tc.wires, "relay", "sol", tc.sent); got != tc.want {
			t.Errorf("%s: asking for %q gives %q; want %q", tc.what, tc.sent, got, tc.want)
		}
	}
}

// The gateway decides where a request goes, and for an account it hands to
// the agent's own backend the model it knows is what goes out — so a wire
// name given for one of those accounts is accepted, never sent, and read at
// the ledger as every call through it a swap. asksOwnBackend refuses them
// where the name is given, and the two have to be the same set: an account
// the gateway starts serving its own way is refused a name the day it
// lands. So the set is read out of the dispatch itself rather than kept a
// second time beside it, which is the copy that goes on saying what the
// dispatch stopped saying.
func TestAsksOwnBackendAgreesWithTheGatewayDispatch(t *testing.T) {
	d := &dispatch{t: t, seen: map[string]bool{}}
	dispatched := d.accountBackends(filepath.Join("..", "gateway"))
	if len(dispatched) == 0 {
		t.Fatal("no account is read as one the gateway asks through its own backend; its dispatch is not being read")
	}
	for _, agent := range dispatched {
		if !(Provider{Account: &Account{Agent: agent}}).asksOwnBackend() {
			t.Errorf("the gateway asks a %s account through its own backend, where the model id the agent knows is what goes out, and a wire name is accepted for one of its models", agent)
		}
	}
	// and nothing beyond them: an account the gateway asks over a vendor's
	// API does send the name, so refusing one there refuses a name that
	// would have worked
	for _, agent := range accountIDs {
		if slices.Contains(dispatched, agent) {
			continue
		}
		if (Provider{Account: &Account{Agent: agent}}).asksOwnBackend() {
			t.Errorf("a %s account is asked over the vendor's own API, where a wire name is sent, and one is refused for its models", agent)
		}
	}
}

// dispatch reads an account dispatch out of the sources it is written in:
// which package a name is a constant of, and which constants are on their
// way to themselves. A module's own name is the prefix every import path in
// it has, which is how one package's constant is looked up in another.
type dispatch struct {
	t    *testing.T
	root string // the tree the module is checked out in
	mod  string // what its go.mod calls the module
	seen map[string]bool
}

// accountBackends are the agents the gateway serves a request itself for,
// where the account belongs to the agent whose backend it is: a branch that
// asks whether the provider's account is one of them and then reaches that
// backend's own serve, as against one that sends the request to a vendor.
// A test file is not read: what a test says the dispatch does is no part of
// what it does.
func (d *dispatch) accountBackends(dir string) []string {
	d.t.Helper()
	var out []string
	for _, name := range d.files(dir) {
		ast.Inspect(d.parse(name), func(n ast.Node) bool {
			ifs, ok := n.(*ast.IfStmt)
			if !ok || !d.servesItself(ifs) {
				return true
			}
			for _, agent := range d.agents(ifs.Cond, dir) {
				if !slices.Contains(out, agent) {
					out = append(out, agent)
				}
			}
			return true
		})
	}
	slices.Sort(out)
	return out
}

// servesItself says whether a branch is one where the request is answered for
// the agent that owns the account instead of sent to a vendor: it ends in
// one of the serves, each named for the backend it answers for and reached
// by every account of that agent. A branch that only reads the account — what
// a request needs from it, which model it counts tokens for — is not one,
// and neither is a call that begins with serve and serves nothing:
// servesElsewhere answers whether a provider serves something.
func (d *dispatch) servesItself(ifs *ast.IfStmt) bool {
	d.t.Helper()
	for _, stmt := range ifs.Body.List {
		own := false
		ast.Inspect(stmt, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if name := sel.Sel.Name; len(name) > len("serve") && name[len("serve")] >= 'A' && name[len("serve")] <= 'Z' {
					own = true
				}
			}
			return !own
		})
		if own {
			return true
		}
	}
	return false
}

// agents are the ones a condition compares the provider's account against,
// and nothing at all unless it asks the account is there: the same
// comparison is made elsewhere in the gateway to tell what a request needs
// rather than where it goes.
func (d *dispatch) agents(cond ast.Expr, dir string) []string {
	d.t.Helper()
	var out []string
	account := false
	ast.Inspect(cond, func(n ast.Node) bool {
		b, ok := n.(*ast.BinaryExpr)
		if !ok {
			return true
		}
		switch b.Op {
		case token.NEQ:
			if d.isField(b.X, "Account") && d.isIdent(b.Y, "nil") {
				account = true
			}
		case token.EQL:
			sel, ok := b.X.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Agent" || !d.isField(sel.X, "Account") {
				return true
			}
			agent := d.id(dir, b.Y)
			if agent == "" {
				d.t.Errorf("the dispatch compares an account's agent against %s, which is not an id this test can read: a wire name for that account would be accepted and never sent",
					types.ExprString(b.Y))
				return true
			}
			out = append(out, agent)
		}
		return true
	})
	if !account {
		return nil
	}
	return out
}

// isField says whether the expression is a field of that name, and isIdent
// whether it is a name standing for itself.
func (d *dispatch) isField(e ast.Expr, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == name
}

func (d *dispatch) isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

// id is the agent id a comparison is against, read as far as it is written:
// a string as it stands, or the constant it names — of the package being
// read, as the gateway writes its own, or of one that package imports, as
// Qoder's CN subscription is named. A constant written in terms of itself
// is no id, and says so rather than standing in for one.
func (d *dispatch) id(dir string, e ast.Expr) string {
	d.t.Helper()
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			s, err := strconv.Unquote(x.Value)
			if err != nil {
				d.t.Errorf("%s is not a string this test can read: %v", x.Value, err)
				return ""
			}
			return s
		}
	case *ast.Ident:
		return d.constant(dir, x.Name)
	case *ast.SelectorExpr:
		if pkg, ok := x.X.(*ast.Ident); ok {
			return d.constant(d.pkgDir(dir, pkg.Name), x.Sel.Name)
		}
	}
	return ""
}

// constant is what a constant of the package in dir is written as, in the
// first file that declares it, or the empty string where none does.
func (d *dispatch) constant(dir, name string) string {
	d.t.Helper()
	key := filepath.Join(dir, name)
	if d.seen[key] {
		return ""
	}
	d.seen[key] = true
	for _, file := range d.files(dir) {
		var id string
		ast.Inspect(d.parse(file), func(n ast.Node) bool {
			spec, ok := n.(*ast.ValueSpec)
			if !ok || id != "" || len(spec.Names) != 1 || spec.Names[0].Name != name || len(spec.Values) != 1 {
				return true
			}
			id = d.id(dir, spec.Values[0])
			return true
		})
		if id != "" {
			return id
		}
	}
	return ""
}

// pkgDir is where the package in dir imports `pkg` from keeps its sources:
// an import path is the module's own name and a path under it, so the path
// under it is where those sources are in the same tree.
func (d *dispatch) pkgDir(dir, pkg string) string {
	d.t.Helper()
	if d.root == "" {
		d.root, d.mod = d.module()
	}
	mod := d.mod + "/"
	for _, name := range d.files(dir) {
		for _, im := range d.parse(name).Imports {
			path, err := strconv.Unquote(im.Path.Value)
			if err != nil || !strings.HasPrefix(path, mod) {
				continue
			}
			as := filepath.Base(path)
			if im.Name != nil {
				as = im.Name.Name
			}
			if as == pkg {
				return filepath.Join(d.root, filepath.FromSlash(strings.TrimPrefix(path, mod)))
			}
		}
	}
	d.t.Fatalf("no package is imported as %q from %s; the dispatch cannot be read", pkg, dir)
	return ""
}

// module is the tree the sources are in and what its go.mod calls the
// module, found by walking up to the go.mod that names it.
func (d *dispatch) module() (string, string) {
	d.t.Helper()
	root, err := filepath.Abs(".")
	if err != nil {
		d.t.Fatalf("the tree the sources are in: %v", err)
	}
	for {
		if b, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if m, ok := strings.CutPrefix(l, "module "); ok {
					return root, strings.TrimSpace(m)
				}
			}
		}
		if up := filepath.Dir(root); up != root {
			root = up
			continue
		}
		break
	}
	d.t.Fatal("no go.mod above the sources says what the module is called; the dispatch cannot be read")
	return "", ""
}

// files are the sources of a package, its tests left out.
func (d *dispatch) files(dir string) []string {
	d.t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		d.t.Fatalf("the sources in %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if n := e.Name(); !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			out = append(out, filepath.Join(dir, n))
		}
	}
	if len(out) == 0 {
		d.t.Fatalf("no sources in %s", dir)
	}
	slices.Sort(out)
	return out
}

func (d *dispatch) parse(name string) *ast.File {
	d.t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
	if err != nil {
		d.t.Fatalf("%s: %v", name, err)
	}
	return f
}
