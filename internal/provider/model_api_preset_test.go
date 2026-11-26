package provider

import (
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// OpenCode Zen serves TypeSafe's Jev on System One alone, at
// /zen/v1/systemone (its docs' model table, zen.mdx): Jev routes groups
// there, as on TypeSafe's own API, and is never an agent's chat model —
// a Zen provider saved before the preset said so included.
func TestOpenCodeZenJevDecides(t *testing.T) {
	detectHome(t)
	pr, err := FromPreset("opencode-zen")
	if err != nil {
		t.Fatal(err)
	}
	if pr.Decide != "https://opencode.ai/zen/v1" {
		t.Fatalf("preset decide %q", pr.Decide)
	}
	old := pr
	old.Decide = "" // saved before the preset had it
	if err := Save(old); err != nil {
		t.Fatal(err)
	}
	found, err := Find(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	p := *found
	if p.Decide != pr.Decide || p.DecideOnly() {
		t.Fatalf("saved Zen decide %q", p.Decide)
	}
	live := []catalog.Model{{ID: "deepseek-v4.1-flash"}, {ID: "jev-1.13"}, {ID: "jev-1.13-free"}, {ID: "mimo-v2.6-flash-free"}}
	if err := catalog.SaveLive(p.ID, p.Chat, live); err != nil {
		t.Fatal(err)
	}
	if !p.DecidesModel("jev-1.13-free") || p.DecidesModel("deepseek-v4.1-flash") {
		t.Fatal("Jev isn't Zen's decision model")
	}
	var ids []string
	for _, e := range providerEntries() {
		if e.Provider.ID == p.ID {
			ids = append(ids, e.Model)
		}
	}
	if slices.ContainsFunc(ids, func(id string) bool { return strings.HasPrefix(id, "jev") }) || !slices.Contains(ids, "deepseek-v4.1-flash") {
		t.Fatalf("agents' models %v", ids)
	}
	// asked with no key of the user's, the free Jev; with one, Zen's first
	if j := p.Jev(); j != "jev-1.13-free" {
		t.Fatalf("keyless Jev %q", j)
	}
	if u, _ := p.DecideURL(t.Context()); u != "https://opencode.ai/zen/v1/systemone" {
		t.Fatalf("asked at %q", u)
	}
	paid := p
	paid.Key = "sk-own"
	if j := paid.Jev(); j != "jev-1.13" {
		t.Fatalf("keyed Jev %q", j)
	}
}

// OpenCode Go serves Jev 1.14, Gemini Decision 1 and GPT-6 Luna Decision
// on System One under its own key. Its model list marks them, so they
// route groups without being offered to agents as chat models.
func TestOpenCodeGoDecisions(t *testing.T) {
	detectHome(t)
	p, err := FromPreset("opencode-go")
	if err != nil {
		t.Fatal(err)
	}
	const decide = "https://opencode.ai/inference/go/systemone/v1"
	if p.Decide != decide {
		t.Fatalf("preset decide %q", p.Decide)
	}
	p.Key = "sk-go"
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	old := p
	old.Decide = ""
	if err := Save(old); err != nil {
		t.Fatal(err)
	}
	found, err := Find(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Decide != decide {
		t.Fatalf("saved Go decide %q", found.Decide)
	}
	live := []catalog.Model{
		{ID: "deepseek-v4.1-flash"},
		{ID: "jev-1.14", Name: "Jev 1.14", Decides: true, Context: 64_000},
		{ID: "gemini-decision-1", Name: "Gemini Decision 1", Decides: true, Context: 262_144, Images: true},
		{ID: "gpt-6-luna-decision", Name: "GPT-6 Luna Decision", Decides: true, Context: 1_050_000, Images: true},
	}
	if err := catalog.SaveLive(p.ID, p.Chat, live); err != nil {
		t.Fatal(err)
	}
	found, err = Find(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	p = *found
	for _, id := range []string{"jev-1.14", "gemini-decision-1", "gpt-6-luna-decision"} {
		if !p.DecidesModel(id) {
			t.Errorf("%s isn't a decision model", id)
		}
	}
	if p.DecidesModel("deepseek-v4.1-flash") {
		t.Error("a chat model became a decision model")
	}
	if p.Jev() != "jev-1.14" {
		t.Errorf("default decision model %q", p.Jev())
	}
	// where Go's System One answers (a key that isn't Go's gets 401 there,
	// and 404 one path off, on 11-26)
	if u, err := p.DecideURL(t.Context()); err != nil || u != decide+"/systemone" {
		t.Errorf("asked at %q, %v", u, err)
	}
	if ms := p.DecisionModels(); len(ms) != 3 {
		t.Errorf("decision models %+v", ms)
	}
	for _, e := range providerEntries() {
		if e.Provider.ID == p.ID && p.DecidesModel(e.Model) {
			t.Errorf("%s/%s was offered to agents", p.ID, e.Model)
		}
	}
	var offered []string
	for _, e := range OfferedDeciders(nil) {
		if e.Provider.ID == p.ID {
			offered = append(offered, e.Model)
		}
	}
	slices.Sort(offered)
	if !slices.Equal(offered, []string{"gemini-decision-1", "gpt-6-luna-decision", "jev-1.14"}) {
		t.Errorf("offered decisions %v", offered)
	}
}

// A preset's model (OpenCode Go's deepseek-v4.1-flash, which answers on
// Responses as well as chat) and a subscription's can be asked on one API
// the user picks, as a custom provider's can (01huadalang on Discord); Auto
// is what the vendor's list says.
func TestPresetAndSubscriptionModelAPI(t *testing.T) {
	detectHome(t)
	p, err := FromPreset("opencode-go")
	if err != nil {
		t.Fatal(err)
	}
	p.Key, p.Models = "k", []string{"deepseek-v4.1-flash", "minimax-m3"}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	if err := SetModelAPI("opencode-go/deepseek-v4.1-flash", "responses"); err != nil {
		t.Fatal(err)
	}
	got, _ := Find("opencode-go")
	p = *got
	if apis := p.APIs("deepseek-v4.1-flash"); !slices.Equal(apis, []Protocol{Responses}) {
		t.Fatalf("apis %v", apis)
	}
	if n := p.Native("deepseek-v4.1-flash"); n != Responses {
		t.Fatalf("native %s", n)
	}
	if apis := p.ListedAPIs("deepseek-v4.1-flash"); slices.Equal(apis, []Protocol{Responses}) {
		t.Fatalf("the user's pick taken for the vendor's: %v", apis)
	}
	// a subscription with more than one API: the pick is honoured too
	sub := Provider{ID: "copilot", Name: "Copilot", Account: &Account{Agent: "copilot", User: "u"},
		Chat: "https://api.example.com", Responses: "https://api.example.com", Anthropic: "https://api.example.com"}
	s := settings.Load()
	s.ModelAPIs = map[string]string{"copilot/gpt-9": "chat"}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if got, ok := sub.ModelAPI("gpt-9"); !ok || got != Chat {
		t.Fatalf("subscription ModelAPI %v %v", got, ok)
	}
	if apis := sub.APIs("gpt-9"); !slices.Equal(apis, []Protocol{Chat}) {
		t.Fatalf("subscription apis %v", apis)
	}
	// one with a single API has nothing to pick
	one := Provider{ID: "codex", Account: &Account{Agent: "codex"}, Responses: "https://chatgpt.example.com"}
	s.ModelAPIs = map[string]string{"codex/gpt-9": "chat"}
	settings.Save(s)
	if _, ok := one.ModelAPI("gpt-9"); ok {
		t.Fatal("a single-API subscription took a pick")
	}
}

// Zen's list marks its decision models as Go's does (type "decision"), so
// Gemini Decision 1 and GPT-6 Luna Decision, which Zen's System One serves
// (it asked for a key, and turned an unknown model away, on 11-26), route
// groups beside its Jevs, and none of them is offered to agents.
func TestOpenCodeZenListedDecisions(t *testing.T) {
	detectHome(t)
	p, err := FromPreset("opencode-zen")
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	live := []catalog.Model{{ID: "deepseek-v4.1-flash"}, {ID: "jev-1.13", Decides: true}, {ID: "jev-1.13-free", Decides: true},
		{ID: "gemini-decision-1", Decides: true}, {ID: "gpt-6-luna-decision", Decides: true}, {ID: "jev-1.14", Decides: true}}
	if err := catalog.SaveLive(p.ID, p.Chat, live); err != nil {
		t.Fatal(err)
	}
	found, err := Find(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	p = *found
	for _, e := range providerEntries() {
		if e.Provider.ID == p.ID && e.Model != "deepseek-v4.1-flash" {
			t.Errorf("%s/%s was offered to agents", p.ID, e.Model)
		}
	}
	var offered []string
	for _, e := range OfferedDeciders(nil) {
		if e.Provider.ID == p.ID {
			offered = append(offered, e.Model)
		}
	}
	slices.Sort(offered)
	if want := []string{"gemini-decision-1", "gpt-6-luna-decision", "jev-1.13", "jev-1.13-free", "jev-1.14"}; !slices.Equal(offered, want) {
		t.Errorf("offered decisions %v, want %v", offered, want)
	}
	if j := p.Jev(); j != "jev-1.13-free" {
		t.Errorf("keyless Jev %q", j)
	}
}
