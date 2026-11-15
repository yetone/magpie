package provider

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// localIDHome is two relays of the user's own (zeewell on X): relay-a lists
// deepseek-v4.1-flash-1017 and both grok-4.6 and grok-4.6@500k, relay-b
// deepseek-v4.1-flash.
func localIDHome(t *testing.T) {
	t.Helper()
	azureHome(t)
	for _, r := range []struct {
		id  string
		ids []string
	}{
		{"relay-a", []string{"deepseek-v4.1-flash-1017", "grok-4.6", "grok-4.6@500k"}},
		{"relay-b", []string{"deepseek-v4.1-flash"}},
	} {
		base := "https://" + r.id + ".example.invalid/v1"
		if err := Save(Provider{ID: r.id, Name: r.id, Key: "sk-" + r.id, Chat: base}); err != nil {
			t.Fatal(err)
		}
		var ms []catalog.Model
		for _, id := range r.ids {
			ms = append(ms, catalog.Model{ID: id})
		}
		if err := catalog.SaveLive(r.id, base, ms); err != nil {
			t.Fatal(err)
		}
	}
}

func findProvider(t *testing.T, id string) Provider {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return *p
}

func modelIDList(ms []catalog.Model) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func setID(t *testing.T, ref, id string) string {
	t.Helper()
	replaced, changed, err := SetModelID(ref, id)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatalf("SetModelID(%q, %q) changed nothing", ref, id)
	}
	return replaced
}

// A model given an id is listed, resolved, found in a group with the
// other relay's model of that id, and asked for by its own id.
func TestModelIDListedAndAskedForByOwn(t *testing.T) {
	localIDHome(t)
	setID(t, "relay-a/deepseek-v4.1-flash-1017", "deepseek-v4.1-flash")
	setID(t, "relay-a/grok-4.6@500k", "grok-4.6")

	a := findProvider(t, "relay-a")
	// grok-4.6 of relay-a's own is not listed while @500k is listed by it
	if got, want := modelIDList(a.Available()), []string{"deepseek-v4.1-flash", "grok-4.6"}; !slices.Equal(got, want) {
		t.Fatalf("relay-a lists %v, want %v", got, want)
	}
	for local, own := range map[string]string{"deepseek-v4.1-flash": "deepseek-v4.1-flash-1017", "grok-4.6": "grok-4.6@500k"} {
		if got := a.OwnModelID(local); got != own {
			t.Errorf("OwnModelID(%s) = %s, want %s", local, got, own)
		}
		if got := UpstreamName(a, local); got != own {
			t.Errorf("UpstreamName(%s) = %s, want %s: the vendor is asked for its own id", local, got, own)
		}
		p, model, ok := Resolve("relay-a/" + local)
		if !ok || p.ID != "relay-a" || model != local {
			t.Errorf("Resolve(relay-a/%s) = %s %s %v", local, p.ID, model, ok)
		}
	}
	// an agent's old ref to the own id still resolves, asked for as is
	if p, model, ok := Resolve("relay-a/deepseek-v4.1-flash-1017"); !ok || p.ID != "relay-a" || UpstreamName(p, model) != "deepseek-v4.1-flash-1017" {
		t.Errorf("old ref: %s %s %v", p.ID, model, ok)
	}
	// relay-b's model of its own id is unchanged
	if got := UpstreamName(findProvider(t, "relay-b"), "deepseek-v4.1-flash"); got != "deepseek-v4.1-flash" {
		t.Errorf("relay-b asked for %s", got)
	}

	// the found group has both relays under the one id
	gid, ok := GroupFor("deepseek-v4.1-flash")
	if !ok {
		t.Fatal("no found group for deepseek-v4.1-flash")
	}
	_, ms, ok := FindGroup(gid)
	if !ok {
		t.Fatalf("FindGroup(%s)", gid)
	}
	var members []string
	for _, m := range ms {
		members = append(members, m.Provider.ID+"/"+m.Model)
	}
	slices.Sort(members)
	if want := []string{"relay-a/deepseek-v4.1-flash", "relay-b/deepseek-v4.1-flash"}; !slices.Equal(members, want) {
		t.Fatalf("%s has %v, want %v", gid, members, want)
	}

	// reset: listed by its own id again, nothing left in the settings
	if _, changed, err := SetModelID("relay-a/deepseek-v4.1-flash", ""); err != nil || !changed {
		t.Fatalf("reset: %v %v", changed, err)
	}
	if _, changed, err := SetModelID("relay-a/grok-4.6", "--reset"); err == nil || changed {
		t.Fatalf("a flag taken as an id: %v %v", changed, err)
	}
	setID(t, "relay-a/grok-4.6", "grok-4.6@500k")
	if got := modelIDList(findProvider(t, "relay-a").Available()); !slices.Equal(got, []string{"deepseek-v4.1-flash-1017", "grok-4.6", "grok-4.6@500k"}) {
		t.Fatalf("after reset relay-a lists %v", got)
	}
	if ids := settings.Load().ModelIDs; len(ids) != 0 {
		t.Fatalf("after reset ModelIDs = %v", ids)
	}
}

// One id is one model of a provider: the latest given it has it, and the
// one before is listed by its own id again.
func TestModelIDLatestWins(t *testing.T) {
	localIDHome(t)
	setID(t, "relay-a/grok-4.6@500k", "grok")
	if replaced := setID(t, "relay-a/deepseek-v4.1-flash-1017", "grok"); replaced != "grok-4.6@500k" {
		t.Fatalf("replaced %q, want grok-4.6@500k", replaced)
	}
	a := findProvider(t, "relay-a")
	if got, want := modelIDList(a.Available()), []string{"grok", "grok-4.6", "grok-4.6@500k"}; !slices.Equal(got, want) {
		t.Fatalf("relay-a lists %v, want %v", got, want)
	}
	if got := a.OwnModelID("grok"); got != "deepseek-v4.1-flash-1017" {
		t.Fatalf("grok is %s", got)
	}

	// two rows of one save can't take one id
	if err := SetModelPrefs("relay-a", map[string]ModelPref{
		"grok-4.6":      {ID: refOf("x")},
		"grok-4.6@500k": {ID: refOf("x")},
	}); err == nil || !strings.Contains(err.Error(), "can't both be listed as x") {
		t.Fatalf("SetModelPrefs with one id twice: %v", err)
	}
}

func refOf[T any](v T) *T { return &v }

// What the user picked, grouped and set of the model goes to its new id,
// but a value they already have under that id stays theirs.
func TestModelIDMovesTheUsersRefs(t *testing.T) {
	localIDHome(t)
	a := findProvider(t, "relay-a")
	a.Models = []string{"deepseek-v4.1-flash-1017", "grok-4.6"}
	a.Contexts = map[string]int{"deepseek-v4.1-flash-1017": 64000}
	if err := Save(a); err != nil {
		t.Fatal(err)
	}
	b := findProvider(t, "relay-b")
	b.Fallback = []string{"relay-a/deepseek-v4.1-flash-1017"}
	if err := Save(b); err != nil {
		t.Fatal(err)
	}
	if err := SaveGroup(Group{ID: "flash", Name: "Flash", Members: []string{"relay-b/deepseek-v4.1-flash", "relay-a/deepseek-v4.1-flash-1017"}}); err != nil {
		t.Fatal(err)
	}
	s := settings.Load()
	s.HiddenModels = map[string][]string{"codex": {"relay-a/deepseek-v4.1-flash-1017"}}
	s.ModelOutputs = map[string]int{"relay-a/deepseek-v4.1-flash-1017": 8000}
	s.ModelNames = map[string]string{"relay-a/deepseek-v4.1-flash-1017": "old", "relay-a/deepseek-v4.1-flash": "Mine"}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	var moved [][2]string
	old := ModelMoved
	ModelMoved = func(from, to string) error { moved = append(moved, [2]string{from, to}); return nil }
	t.Cleanup(func() { ModelMoved = old })

	setID(t, "relay-a/deepseek-v4.1-flash-1017", "deepseek-v4.1-flash")

	a = findProvider(t, "relay-a")
	if !slices.Equal(a.Models, []string{"deepseek-v4.1-flash", "grok-4.6"}) {
		t.Errorf("picks %v", a.Models)
	}
	if a.Contexts["deepseek-v4.1-flash"] != 64000 || len(a.Contexts) != 1 {
		t.Errorf("contexts %v", a.Contexts)
	}
	if b := findProvider(t, "relay-b"); !slices.Equal(b.Fallback, []string{"relay-a/deepseek-v4.1-flash"}) {
		t.Errorf("fallback %v", b.Fallback)
	}
	g, _, ok := FindGroup(GroupPrefix + "flash")
	if !ok || !slices.Equal(g.Members, []string{"relay-b/deepseek-v4.1-flash", "relay-a/deepseek-v4.1-flash"}) {
		t.Errorf("group %v %v", g.Members, ok)
	}
	s = settings.Load()
	if got := s.HiddenModels["codex"]; !slices.Equal(got, []string{"relay-a/deepseek-v4.1-flash"}) {
		t.Errorf("hidden %v", got)
	}
	if s.ModelOutputs["relay-a/deepseek-v4.1-flash"] != 8000 {
		t.Errorf("outputs %v", s.ModelOutputs)
	}
	// the user's own value under the new id is never written over
	if s.ModelNames["relay-a/deepseek-v4.1-flash"] != "Mine" {
		t.Errorf("names %v", s.ModelNames)
	}
	if !slices.Equal(moved, [][2]string{{"relay-a/deepseek-v4.1-flash-1017", "relay-a/deepseek-v4.1-flash"}}) {
		t.Errorf("ModelMoved got %v", moved)
	}
}

// Only a provider of the user's own takes an id, and only one a model
// can have.
func TestModelIDRefused(t *testing.T) {
	localIDHome(t)
	for _, id := range []string{"a b", "x*", "-x", "group/flash"} {
		if _, changed, err := SetModelID("relay-a/grok-4.6", id); err == nil || changed {
			t.Errorf("id %q taken: %v", id, changed)
		}
	}
	if _, _, err := SetModelID("relay-a/nope", "x"); err == nil {
		t.Error("an unknown model took an id")
	}
	if !(Provider{Account: &Account{}}).TakesModelIDs() {
		t.Error("an account takes no ids")
	}
	if (Provider{Decide: "https://decide.example.invalid"}).TakesModelIDs() {
		t.Error("a provider that only decides takes ids")
	}
	if len(settings.Load().ModelIDs) != 0 {
		t.Fatalf("ModelIDs = %v", settings.Load().ModelIDs)
	}
}

// The picks of a provider's list (liveIDs) and its keys' models
// (Serving) are read by the id the model is listed by.
func TestModelIDServingAndPrice(t *testing.T) {
	localIDHome(t)
	setID(t, "relay-a/deepseek-v4.1-flash-1017", "deepseek-v4.1-flash")
	ids := liveIDs("relay-a")
	if !ids["deepseek-v4.1-flash"] || ids["deepseek-v4.1-flash-1017"] {
		t.Fatalf("liveIDs %v", ids)
	}
	a := findProvider(t, "relay-a")
	if ms := a.liveByID("deepseek-v4.1-flash"); len(ms) != 1 || ms[0].ID != "deepseek-v4.1-flash" {
		t.Fatalf("liveByID %v", ms)
	}
}

// An account's model listed by an id of the user's own (zeewell on X:
// SuperGrok's grok-4.6@500k as grok-4.6) is read by the vendor's id where
// the account's own say of it is: the account's picks kept from before,
// and the variant an Antigravity family goes out as at an effort.
func TestModelIDAtAnAccount(t *testing.T) {
	t.Cleanup(AntigravityBuiltinForTest()) // the retired built-in route
	prefsHome(t)
	if err := os.MkdirAll(filepath.Dir(catalog.LivePath("antigravity")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.LivePath("antigravity"), []byte(`{"models":[{"id":"flash-9-low"},{"id":"flash-9-high"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	s := settings.Load()
	s.ModelIDs = map[string]string{"antigravity/my-flash": "flash-9", "grok/grok-4.6": "grok-4.6@500k"}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}

	g := Provider{ID: "grok", Account: &Account{Agent: "grok"}, AccountModels: map[string][]string{"a@b.c": {"grok-4.6@500k"}}}
	if own := g.OwnModelID("grok-4.6"); own != "grok-4.6@500k" {
		t.Fatalf("the account is asked for grok-4.6 by %q", own)
	}
	if !g.AccountServes("a@b.c", "grok-4.6") {
		t.Error("the account's pick of grok-4.6@500k doesn't serve it listed as grok-4.6")
	}
	if g.AccountServes("a@b.c", "grok-4.7") {
		t.Error("the account's picks serve a model it didn't pick")
	}

	wires := settings.Load().Wires()
	if got := SentNameOnIn(wires, "antigravity", "antigravity", "my-flash", "low"); got != "flash-9-low" {
		t.Errorf("flash-9 listed as my-flash, at low, goes out as %q; want flash-9-low", got)
	}
	if got, want := SentNameOnIn(wires, "antigravity", "antigravity", "my-flash", ""), AntigravitySentID("flash-9", ""); got != want {
		t.Errorf("flash-9 listed as my-flash, at no effort, goes out as %q; want the family's default %s", got, want)
	}
}
