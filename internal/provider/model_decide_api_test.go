package provider

import (
	"slices"
	"strings"
	"testing"
)

// axelburks on Discord: a key's provider with a System One URL beside its
// chat URL serves a decision model under a name of its own (an Ark
// endpoint id); the model's API can be set to System One, which makes it a
// decision model there: offered to routing groups, asked on System One by
// its own name, and kept out of the conversation models. Auto again gives
// it back to them.
func TestModelSetToSystemOneIsADecisionModel(t *testing.T) {
	prefillHome(t)
	if err := Save(Provider{ID: "ark", Name: "Ark", Key: "ark-k", Chat: "https://ark.example/api/v3",
		Decide: "https://ark.example/api/v3/systemone", Models: []string{"ep-chat", "ep-decide"}}); err != nil {
		t.Fatal(err)
	}
	if err := SetModelAPI("ark/ep-decide", "decide"); err != nil {
		t.Fatal(err)
	}
	p, err := Find("ark")
	if err != nil {
		t.Fatal(err)
	}
	if !p.DecidesModel("ep-decide") || p.DecidesModel("ep-chat") {
		t.Fatalf("decides: ep-decide %v, ep-chat %v", p.DecidesModel("ep-decide"), p.DecidesModel("ep-chat"))
	}
	if api, ok := p.ModelAPI("ep-decide"); !ok || api != DecideAPI {
		t.Fatalf("ModelAPI = %q %v", api, ok)
	}
	if m, ok := resolveDecideModel(*p, "ep-decide"); !ok || m != "ep-decide" {
		t.Fatalf("asked on System One as %q %v, want its own name", m, ok)
	}
	ids := func(es []Entry) []string {
		var out []string
		for _, e := range es {
			if strings.HasPrefix(e.ID, "ark/") {
				out = append(out, e.ID)
			}
		}
		return out
	}
	if got := ids(Deciders()); !slices.Contains(got, "ark/ep-decide") || slices.Contains(got, "ark/ep-chat") {
		t.Fatalf("Deciders = %v", got)
	}
	if got := ids(buildEntries()); !slices.Contains(got, "ark/ep-chat") || slices.Contains(got, "ark/ep-decide") {
		t.Fatalf("conversation models = %v", got)
	}

	// Auto: a conversation model again
	if err := SetModelAPI("ark/ep-decide", ""); err != nil {
		t.Fatal(err)
	}
	if p, _ = Find("ark"); p.DecidesModel("ep-decide") {
		t.Fatal("still a decision model after Auto")
	}
	if got := ids(buildEntries()); !slices.Contains(got, "ark/ep-decide") {
		t.Fatalf("conversation models after Auto = %v", got)
	}
}

// Only a key's provider with a System One URL beside a conversation URL
// takes it: one with no System One URL, or with that alone (every model a
// decision model already), refuses it, saying why.
func TestModelSystemOneNeedsItsURL(t *testing.T) {
	prefillHome(t)
	for _, p := range []Provider{
		{ID: "relay", Name: "Relay", Key: "k", Chat: "https://relay.example/v1", Models: []string{"m"}},
		{ID: "jev", Name: "Jev", Key: "k", Decide: "https://jev.example/v1", Models: []string{"m"}},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
		if err := SetModelAPI(p.ID+"/m", "decide"); err == nil || !strings.Contains(err.Error(), "System One") {
			t.Fatalf("%s: %v", p.ID, err)
		}
	}
	if (Provider{Decide: "https://x/v1", Chat: "https://x/v1", Account: &Account{}}).TakesDecideAPI() {
		t.Fatal("an account takes it")
	}
	if (Provider{Decide: "https://x/v1", Chat: "https://x/v1", Preset: RemoteMagpiePreset}).TakesDecideAPI() {
		t.Fatal("a remote magpie takes it")
	}
}
