package provider

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// limitsHome keeps a test off the machine's own settings, providers and
// catalogue, and forgets what an earlier test cached.
func limitsHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

// A window and a reply limit are numbers the agents keep in files of their
// own — Pi's contextWindow and maxTokens, OpenCode's limit — and read at
// start-up, so setting them has to tell them, as a name does, and to tell
// them once: the two paths save different files, and each of them already
// ends in a telling. Saving them quietly would leave every agent a limit
// behind that magpie no longer serves; telling twice rewrites every agent's
// model lists over again for nothing.
func TestLimitsReachTheAgents(t *testing.T) {
	limitsHome(t)
	if err := Save(Provider{ID: "relay", Name: "relay", Chat: "https://relay.example/v1", Key: "k", Models: []string{"sol"}}); err != nil {
		t.Fatal(err)
	}
	told := 0
	catalog.Changed = func() { told++ }
	t.Cleanup(func() { catalog.Changed = nil })

	// once, not twice: the two paths reach the agents differently — a reply
	// limit saves the settings file and tells them itself, a window saves
	// the provider and is told from there — and either way an agent's model
	// lists are rewritten by one round, not two
	if err := SetModelOutput("relay/sol", 4096); err != nil {
		t.Fatal(err)
	}
	if told != 1 {
		t.Errorf("a reply limit told the agents %d times; want once", told)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	told = 0
	if err := SetContext(*p, "sol", 262144); err != nil {
		t.Fatal(err)
	}
	if told != 1 {
		t.Errorf("a window told the agents %d times; want once", told)
	}

	// and a limit changed again is told just as loudly, and just once
	told = 0
	if err := SetModelOutput("relay/sol", 8192); err != nil {
		t.Fatal(err)
	}
	if told != 1 {
		t.Errorf("a changed reply limit told the agents %d times; want once", told)
	}

	// the limits reached the entry the gateway serves
	e, ok := EntryOf("relay/sol")
	if !ok {
		t.Fatal("relay/sol is not in the catalog")
	}
	if e.Context != 262144 || e.Output != 8192 {
		t.Errorf("entry: context %d, output %d; want 262144 and 8192", e.Context, e.Output)
	}
}

// A limit keyed by a model the provider does not serve would never be looked
// up again, so it is refused the same way a name for one is: quietly keeping
// it is a value that reads as set and is not.
func TestLimitsRefuseAModelTheProviderHasNot(t *testing.T) {
	limitsHome(t)
	if err := Save(Provider{ID: "relay", Name: "relay", Chat: "https://relay.example/v1", Key: "k", Models: []string{"sol"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if err := SetModelOutput("relay/typo", 4096); err == nil {
		t.Error("a reply limit for a model relay does not serve was accepted")
	}
	if err := SetContext(*p, "typo", 262144); err == nil {
		t.Error("a window for a model relay does not serve was accepted")
	}
	if n := settings.Load().ModelOutputs["relay/typo"]; n != 0 {
		t.Errorf("a refused reply limit was kept anyway: %d", n)
	}
	stored, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if n := stored.Contexts["typo"]; n != 0 {
		t.Errorf("a refused window was kept anyway: %d", n)
	}

	// "*" names every model of the provider, not one of them, and a key
	// that is not a model's at all is refused before either
	if err := SetModelOutput("relay/*", 4096); err != nil {
		t.Errorf("the provider's own every-model limit was refused: %v", err)
	}
	if err := SetModelOutput("not a model", 4096); err == nil {
		t.Error("a key that names no provider and model was accepted")
	}
}

// A model taken off a provider — magpie provider set relay models=sol —
// leaves a window and a reply limit keyed by an id the provider no longer
// serves, and both have to be clearable: refusing a --reset for it is a
// number the user can only take off by hand, with nothing to say why.
func TestLimitsComeOffAModelThatHasGone(t *testing.T) {
	limitsHome(t)
	if err := Save(Provider{ID: "relay", Name: "relay", Chat: "https://relay.example/v1", Key: "k",
		Models: []string{"sol", "mystery-x"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if err := SetContext(*p, "mystery-x", 262144); err != nil {
		t.Fatal(err)
	}
	if err := SetModelOutput("relay/mystery-x", 131072); err != nil {
		t.Fatal(err)
	}

	// magpie provider set relay models=sol: the model is off its list
	p, err = Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	p.Models = []string{"sol"}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	if err := SetModelOutput("relay/mystery-x", 0); err != nil {
		t.Fatalf("--reset of a reply limit: %v", err)
	}
	if n := settings.Load().ModelOutputs["relay/mystery-x"]; n != 0 {
		t.Errorf("the reply limit of a model that has gone is kept: %d", n)
	}
	if err := SetContext(*p, "mystery-x", 0); err != nil {
		t.Fatalf("--reset of a window: %v", err)
	}
	stored, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if n := stored.Contexts["mystery-x"]; n != 0 {
		t.Errorf("the window of a model that has gone is kept: %d", n)
	}

	// setting one is still refused: it would answer no request, and
	// nothing would ever say the number is not in force
	if err := SetModelOutput("relay/mystery-x", 4096); err == nil {
		t.Error("a reply limit for a model that has gone was accepted")
	}
	if err := SetContext(*stored, "mystery-x", 262144); err == nil {
		t.Error("a window for a model that has gone was accepted")
	}
}

// A reply limit outlives the provider it was set for, and that provider can
// be deleted: the entry is then left in the settings under an id nothing
// serves any more. A removal that resolved the provider first would leave it
// with no way off but the file, so a removal is done under the key the entry
// is stored at — which is all that is left of the id to go by. With no
// provider to resolve, the two checks that keep a --reset off a key that was
// never one are all that stand between it and the wrong entry.
func TestOutputComesOffAProviderThatIsGone(t *testing.T) {
	limitsHome(t)
	if err := Save(Provider{ID: "relay", Name: "relay", Chat: "https://relay.example/v1", Key: "k",
		Models: []string{"sol", "mystery-x"}}); err != nil {
		t.Fatal(err)
	}
	// the spellings a caller has: the id itself, the provider's own
	// every-model one, and the "magpie/" the agents put in front of the
	// gateway's ids. Each is a key of the one map.
	ids := []string{"relay/mystery-x", "relay/*", "magpie/relay/sol"}
	for _, id := range ids {
		if err := SetModelOutput(id, 131072); err != nil {
			t.Fatal(err)
		}
	}
	// the provider is deleted, not switched off: nothing is left of it to
	// resolve a ref against, and the entries stay in the settings
	if err := Delete("relay"); err != nil {
		t.Fatal(err)
	}
	if got := len(settings.Load().ModelOutputs); got != len(ids) {
		t.Fatalf("%d limits are left in the settings; want %d", got, len(ids))
	}
	for _, id := range ids[:2] {
		key, dropped, err := DropModelOutput(id)
		if err != nil {
			t.Errorf("%s --reset: %v; a limit of a provider that is gone cannot be taken off", id, err)
			continue
		}
		if !dropped {
			t.Errorf("%s --reset: took a limit off that was not there", id)
		}
		if want := strings.TrimPrefix(id, "magpie/"); key != want {
			t.Errorf("%s --reset: cleared %q; want %q, the key it is stored at", id, key, want)
		}
	}
	// and the same removal through the setter every other caller reaches for
	if err := SetModelOutput(ids[2], 0); err != nil {
		t.Errorf("%s --reset through SetModelOutput: %v", ids[2], err)
	}
	if left := settings.Load().ModelOutputs; len(left) != 0 {
		t.Errorf("the settings still hold %v; want none", left)
	}
	// taking away what is not there is not a failure: it is the state the
	// entry is left in either way, and the caller is told there was none to
	// take so it does not report one that was
	if _, dropped, err := DropModelOutput("relay/mystery-x"); err != nil {
		t.Errorf("--reset of a limit that is not there: %v", err)
	} else if dropped {
		t.Error("--reset of a limit that is not there took one off")
	}
	if _, _, err := DropModelOutput("group/auto"); err == nil {
		t.Error("a routing group's was taken as a model's limit")
	}
	if _, _, err := DropModelOutput("not a model"); err == nil {
		t.Error("a key that names no provider and model was accepted")
	}
	// setting one is still refused: there is no provider whose model it
	// would be, so it would answer no request and nothing would say so
	if err := SetModelOutput("relay/mystery-x", 4096); err == nil {
		t.Error("a reply limit for a provider that is gone was accepted")
	}
	if n := settings.Load().ModelOutputs["relay/mystery-x"]; n != 0 {
		t.Errorf("a refused reply limit was kept anyway: %d", n)
	}
}

// A reply limit outlives the provider it was set for, and nothing rewrites
// its key when that provider is deleted — so "b/sol" is still where one is
// held while a provider of another id has since been given the display name
// "b". A --reset of the spelling the entry is stored under has to clear
// that entry: resolved as a name first it reaches the second provider's key
// instead, where a limit of its own is stored, so the user is told the
// entry they asked for has gone, and the entry they meant stays in the
// settings to go on answering with.
func TestOutputResetTakesTheEntryTheProviderLeft(t *testing.T) {
	limitsHome(t)
	if err := Save(Provider{ID: "b", Name: "b", Chat: "https://relay.example/v1", Key: "k",
		Models: []string{"sol", "other"}}); err != nil {
		t.Fatal(err)
	}
	// the two keys a reply limit is kept under: the model's own, and the
	// provider's every-model one, which is the fallback for every model it
	// serves and so the entry left behind that goes on answering
	for _, id := range []string{"b/sol", "b/*"} {
		if err := SetModelOutput(id, 4096); err != nil {
			t.Fatal(err)
		}
	}
	if err := Delete("b"); err != nil {
		t.Fatal(err)
	}
	// and a provider of another id given the name b is what those entries
	// have to survive: its own keys are the ones that name resolves to
	if err := Save(Provider{ID: "c", Name: "b", Chat: "https://relay.example/v1", Key: "k",
		Models: []string{"sol", "other"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"c/sol", "c/*"} {
		if err := SetModelOutput(id, 8192); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"b/sol", "b/*"} {
		key, dropped, err := DropModelOutput(id)
		if err != nil {
			t.Errorf("%s --reset: %v; a limit of a provider that is gone cannot be taken off", id, err)
			continue
		}
		if !dropped {
			t.Errorf("%s --reset: took a limit off that was not there", id)
		}
		if key != id {
			t.Errorf("%s --reset: cleared %q; want %q, the key it is stored at", id, key, id)
		}
		if n := settings.Load().ModelOutputs[id]; n != 0 {
			t.Errorf("%s --reset: still answers with at most %d tokens", id, n)
		}
	}
	// and the other provider's own limits are none of this command's
	// business: they are what a limit of b's provider would be cleared
	// under instead, and the user told it had been
	outs := settings.Load().ModelOutputs
	if len(outs) != 2 || outs["c/sol"] != 8192 || outs["c/*"] != 8192 {
		t.Errorf("the settings hold %v; want the other provider's two limits of 8192 and none of b's", outs)
	}
	if e, ok := EntryOf("c/sol"); !ok || e.Output != 8192 {
		t.Errorf("c/sol answers with at most %d; want its own 8192", e.Output)
	}
}

// A limit set through a display name is stored under the provider's own id,
// which is the only one outputOf and the gateway read it by, so a --reset
// spelled the same way has to resolve the name to reach it — and then clear
// the entry the provider is really answering with, not leave it standing
// under a key nothing is read by.
func TestOutputResetByADisplayName(t *testing.T) {
	limitsHome(t)
	if err := Save(Provider{ID: "c", Name: "b", Chat: "https://relay.example/v1", Key: "k",
		Models: []string{"sol", "other"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"b/sol", "b/*"} {
		if err := SetModelOutput(id, 4096); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		// nothing is stored under the name: the entry is under the id the
		// provider is read by
		if n, ok := settings.Load().ModelOutputs[id]; ok {
			t.Errorf("%q is stored at %d; want the provider's own id, which is what answers", id, n)
		}
		key, dropped, err := DropModelOutput(id)
		if err != nil {
			t.Errorf("%s --reset: %v", id, err)
			continue
		}
		if !dropped {
			t.Errorf("%s --reset: took a limit off that was not there", id)
		}
		if want := "c" + strings.TrimPrefix(id, "b"); key != want {
			t.Errorf("%s --reset: cleared %q; want %q, the key it is stored at", id, key, want)
		}
		if left := settings.Load().ModelOutputs; len(left) != 0 {
			t.Errorf("%s --reset: the settings still hold %v; want none", id, left)
		}
	}
}

// A --reset of an id no provider answers to is not a failure: there is
// nothing stored under it to take away, which is the state the entry is
// left in either way. What it must not do is clear a limit of a provider it
// does resolve to, and report the user's own as gone.
func TestOutputResetOfAnIdNoProviderAnswersTo(t *testing.T) {
	limitsHome(t)
	if err := Save(Provider{ID: "c", Name: "b", Chat: "https://relay.example/v1", Key: "k",
		Models: []string{"sol"}}); err != nil {
		t.Fatal(err)
	}
	if err := SetModelOutput("c/sol", 8192); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ id, key string }{
		{"typo/sol", "typo/sol"}, // no provider answers to any part of it
		{"b/typo", "c/typo"},     // a name that resolves, for a model nothing is stored under
	} {
		key, dropped, err := DropModelOutput(c.id)
		if err != nil {
			t.Errorf("%s --reset: %v; a limit that is not there is nothing to take away", c.id, err)
			continue
		}
		if dropped {
			t.Errorf("%s --reset: took a limit off that was not there", c.id)
		}
		if key != c.key {
			t.Errorf("%s --reset: cleared %q; want %q", c.id, key, c.key)
		}
		if n := settings.Load().ModelOutputs["c/sol"]; n != 8192 {
			t.Fatalf("%s --reset: c/sol answers with at most %d; want its own 8192", c.id, n)
		}
	}
	// and a limit cannot be set on one either: with no provider to resolve,
	// the checks that keep a key from being a model stand on the key itself
	if err := SetModelOutput("typo/sol", 4096); err == nil {
		t.Error("a reply limit for a model no provider serves was accepted")
	}
	if n, ok := settings.Load().ModelOutputs["typo/sol"]; ok {
		t.Errorf("a refused reply limit was kept anyway: %d", n)
	}
}

// A removal that took a limit off is a change of the catalog the agents have
// to hear of: the number they keep in files of their own goes back to the
// vendor's list only if they are told. One over a limit that was not there
// changes nothing, and is told to no one — a save and a redraw of every
// agent's model lists over a limit that did not move.
func TestLimitsTellTheAgentsOnlyOfALimitThatMoved(t *testing.T) {
	limitsHome(t)
	if err := Save(Provider{ID: "relay", Name: "relay", Chat: "https://relay.example/v1", Key: "k",
		Models: []string{"sol"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if err := SetContext(*p, "sol", 262144); err != nil {
		t.Fatal(err)
	}
	if err := SetModelOutput("relay/sol", 131072); err != nil {
		t.Fatal(err)
	}
	told := 0
	catalog.Changed = func() { told++ }
	t.Cleanup(func() { catalog.Changed = nil })

	key, dropped, err := DropModelOutput("relay/sol")
	if err != nil {
		t.Fatal(err)
	}
	if !dropped {
		t.Fatal("a reply limit that was set was not taken off")
	}
	if key != "relay/sol" {
		t.Errorf("cleared %q; want the key the limit was stored at", key)
	}
	if _, ok := settings.Load().ModelOutputs[key]; ok {
		t.Error("the key the limit was stored at is still in the settings")
	}
	if told != 1 {
		t.Errorf("a reply limit taken off told the agents %d times; want once", told)
	}

	p, err = Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	told = 0
	dropped, err = DropContext(*p, "sol")
	if err != nil {
		t.Fatal(err)
	}
	if !dropped {
		t.Error("a window that was set was not taken off")
	}
	if told != 1 {
		t.Errorf("a window taken off told the agents %d times; want once", told)
	}
	stored, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if n := stored.Contexts["sol"]; n != 0 {
		t.Errorf("relay/sol still takes %d tokens; want none", n)
	}

	// and a second removal of either is over a limit that is not there:
	// nothing is written, and no agent's files are rewritten
	told = 0
	if _, dropped, err := DropModelOutput("relay/sol"); err != nil {
		t.Errorf("--reset of a reply limit that is not there: %v", err)
	} else if dropped {
		t.Error("--reset of a reply limit that is not there took one off")
	}
	if dropped, err := DropContext(*stored, "sol"); err != nil {
		t.Errorf("--reset of a window that is not there: %v", err)
	} else if dropped {
		t.Error("--reset of a window that is not there took one off")
	}
	// and through the setter every other caller goes by, which is a save
	// away from a provider rewritten for nothing
	if err := SetContext(*stored, "sol", 0); err != nil {
		t.Errorf("--reset of a window that is not there through SetContext: %v", err)
	}
	if told != 0 {
		t.Errorf("a removal over a limit that was not there told the agents %d times; want none", told)
	}
}

// A limit may be set on any model a provider serves, so a query has to find
// what the setter took: a provider kept unlisted is still served through a
// routing group, a provider switched off is served again as soon as it is
// on, and both keep the window and the reply limit the user gave them.
// Answering "is not a model this provider serves" about an id the next
// command stores is the CLI contradicting itself.
func TestServedEntryOfFindsWhatALimitMayBeSetOn(t *testing.T) {
	limitsHome(t)
	for _, c := range []struct {
		id            string
		unlisted, off bool
	}{
		{"relay", true, false}, // unlisted: still served, through a group
		{"dark", false, true},  // switched off: still takes what it is given
	} {
		if err := Save(Provider{ID: c.id, Name: c.id, Chat: "https://relay.example/v1", Key: "k",
			Models: []string{"sol"}, Unlisted: c.unlisted, Off: c.off}); err != nil {
			t.Fatal(err)
		}
		p, err := Find(c.id)
		if err != nil {
			t.Fatal(err)
		}
		if err := SetContext(*p, "sol", 262144); err != nil {
			t.Fatal(err)
		}
		if err := SetModelOutput(c.id+"/sol", 131072); err != nil {
			t.Fatal(err)
		}
		id := c.id + "/sol"
		e, ok := ServedEntryOf(id)
		if !ok {
			t.Fatalf("%s is not a model %s serves, though a window and a reply limit on it are kept", id, c.id)
		}
		if e.Context != 262144 || e.Output != 131072 {
			t.Errorf("%s: context %d, output %d; want 262144 and 131072", id, e.Context, e.Output)
		}
		// the catalog is what these two have no entry in, which is the
		// whole difference the query is answering
		if _, ok := EntryOf(id); ok {
			t.Errorf("%s is in the catalog; this says nothing about the rest", id)
		}
	}
	// and an id no limit can be set on is not found either: the two refuse
	// the same models
	for _, id := range []string{"relay/typo", "nosuch/sol", "relay"} {
		if e, ok := ServedEntryOf(id); ok {
			t.Errorf("%s found %s, a model no limit can be set on", id, e.ID)
		}
	}
}

// A model-specific limit wins over its provider's, and a provider's "every
// model" entry covers a model that has none of its own.
func TestOutputPrecedence(t *testing.T) {
	limitsHome(t)
	if err := Save(Provider{ID: "relay", Name: "relay", Chat: "https://relay.example/v1", Key: "k",
		Models: []string{"sol", "other"}}); err != nil {
		t.Fatal(err)
	}
	if err := SetModelOutput("relay/*", 4096); err != nil {
		t.Fatal(err)
	}
	if e, _ := EntryOf("relay/sol"); e.Output != 4096 {
		t.Errorf("the provider's own output: %d; want 4096", e.Output)
	}
	if err := SetModelOutput("relay/sol", 8192); err != nil {
		t.Fatal(err)
	}
	if e, _ := EntryOf("relay/sol"); e.Output != 8192 {
		t.Errorf("the model's own output: %d; want 8192", e.Output)
	}
	if e, _ := EntryOf("relay/other"); e.Output != 4096 {
		t.Errorf("a model with none of its own: %d; want the provider's 4096", e.Output)
	}
	// taking the model's away leaves the provider's in force, not the
	// catalogue's
	if err := SetModelOutput("relay/sol", 0); err != nil {
		t.Fatal(err)
	}
	if e, _ := EntryOf("relay/sol"); e.Output != 4096 {
		t.Errorf("after --reset: %d; want the provider's 4096 still in force", e.Output)
	}
	// a negative limit is refused rather than read as a reset
	if err := SetModelOutput("relay/other", -1); err == nil {
		t.Error("a negative reply limit was accepted")
	}
}

// A provider renamed from old to new is still found by the id it had (Was),
// so a reply limit can be set by one — an agent's own config is full of the
// ids its provider had when it was written. Every read asks for the id the
// provider has now (outputOf, entryFor, the gateway's /models), so a key
// under the old one is a number that sits in the settings reading as set and
// is in force for nothing. The key is the provider's id as it is now,
// whatever the caller spelled.
func TestOutputKeysByTheProvidersIdNow(t *testing.T) {
	limitsHome(t)
	if err := Save(Provider{ID: "old", Name: "old", Chat: "https://relay.example/v1", Key: "k",
		Models: []string{"sol"}}); err != nil {
		t.Fatal(err)
	}
	if err := Rename("old", "new"); err != nil {
		t.Fatal(err)
	}
	if _, err := Find("old"); err != nil {
		t.Fatalf("the id the provider had is not one it is found by: %v", err)
	}
	// both spellings are the same model, and either leaves one key behind
	for _, id := range []string{"old/sol", "new/sol"} {
		if err := SetModelOutput(id, 4096); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if n := settings.Load().ModelOutputs["new/sol"]; n != 4096 {
			t.Errorf("%s: the key the provider is read by holds %d; want 4096", id, n)
		}
		if n, ok := settings.Load().ModelOutputs["old/sol"]; ok {
			t.Errorf("%s: \"old/sol\" is kept at %d, a key no lookup will ever match", id, n)
		}
		if e, ok := EntryOf("new/sol"); !ok || e.Output != 4096 {
			t.Errorf("%s: the entry the gateway serves answers with at most %d; want 4096", id, e.Output)
		}
	}
}

// The other half of the same key: a removal has to reach the entry that is
// in force. Cleared by an id the provider had, it took an entry nobody reads
// and left the real one standing, so the model kept answering with a limit
// the user had taken off it.
func TestOutputResetKeysByTheProvidersIdNow(t *testing.T) {
	limitsHome(t)
	if err := Save(Provider{ID: "old", Name: "old", Chat: "https://relay.example/v1", Key: "k",
		Models: []string{"sol"}}); err != nil {
		t.Fatal(err)
	}
	if err := SetModelOutput("old/sol", 4096); err != nil {
		t.Fatal(err)
	}
	if err := Rename("old", "new"); err != nil {
		t.Fatal(err)
	}
	// the limit taken before the rename came along with it
	if n := settings.Load().ModelOutputs["new/sol"]; n != 4096 {
		t.Fatalf("after the rename the entry the provider is read by holds %d; want 4096", n)
	}
	for _, id := range []string{"old/sol", "new/sol"} {
		if err := SetModelOutput(id, 4096); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if err := SetModelOutput(id, 0); err != nil {
			t.Fatalf("%s --reset: %v", id, err)
		}
		if left := settings.Load().ModelOutputs; len(left) != 0 {
			t.Errorf("%s --reset: the settings still hold %v; want none", id, left)
		}
		if e, ok := EntryOf("new/sol"); !ok || e.Output == 4096 {
			t.Errorf("%s --reset: the entry the gateway serves still answers with at most %d", id, e.Output)
		}
	}
}

// The provider's own every-model entry is keyed the same way, and a stale
// key under the id it had is the one most likely to be left behind: it is
// the fallback for every model the provider serves, so --reset spelled by
// the old id would clear nothing at all.
func TestEveryModelOutputKeysByTheProvidersIdNow(t *testing.T) {
	limitsHome(t)
	if err := Save(Provider{ID: "old", Name: "old", Chat: "https://relay.example/v1", Key: "k",
		Models: []string{"sol", "other"}}); err != nil {
		t.Fatal(err)
	}
	if err := Rename("old", "new"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"old/*", "new/*"} {
		if err := SetModelOutput(id, 2048); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if n := settings.Load().ModelOutputs["new/*"]; n != 2048 {
			t.Errorf("%s: the key the provider is read by holds %d; want 2048", id, n)
		}
		if n, ok := settings.Load().ModelOutputs["old/*"]; ok {
			t.Errorf("%s: \"old/*\" is kept at %d, a key no lookup will ever match", id, n)
		}
		for _, m := range []string{"new/sol", "new/other"} {
			if e, ok := EntryOf(m); !ok || e.Output != 2048 {
				t.Errorf("%s: %s answers with at most %d; want 2048", id, m, e.Output)
			}
		}
		if err := SetModelOutput(id, 0); err != nil {
			t.Fatalf("%s --reset: %v", id, err)
		}
		if left := settings.Load().ModelOutputs; len(left) != 0 {
			t.Errorf("%s --reset: the settings still hold %v; want none", id, left)
		}
	}
}

// What is checked is what is stored. splitRef takes the spellings a caller
// has — the provider's name, the "magpie/" the agents put in front of the
// gateway's ids — and hands back the provider's own id, and that is the
// only one the maps are read by.
func TestOutputChecksTheKeyItStores(t *testing.T) {
	limitsHome(t)
	if err := Save(Provider{ID: "my-relay", Name: "My Relay", Chat: "https://relay.example/v1",
		Key: "k", Models: []string{"sol"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"magpie/my-relay/sol", "My Relay/sol"} {
		if err := SetModelOutput(id, 4096); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if n := settings.Load().ModelOutputs["my-relay/sol"]; n != 4096 {
			t.Errorf("%s: the key the provider is read by holds %d; want 4096", id, n)
		}
		if e, ok := EntryOf("my-relay/sol"); !ok || e.Output != 4096 {
			t.Errorf("%s: the entry the gateway serves answers with at most %d; want 4096", id, e.Output)
		}
	}
	// a key that names no provider and no model is still refused: there is
	// no provider id to check it against
	if err := SetModelOutput("not a model", 4096); err == nil {
		t.Error("a key that names no provider and model was accepted")
	}
}
