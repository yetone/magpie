package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// An agent plugin's connect is told, of each model in models and of
// model, what /v1/models says of it by the same names: native_endpoints,
// the APIs a request goes on untranslated, and pricing, OpenRouter's USD
// a token (cleary5779 on Discord, a self-built agent picking the API that
// passes straight through and showing what a call costs). A model with no
// known price has no pricing at all, never a 0.
func TestPluggedConnectSeesEndpointsAndPricing(t *testing.T) {
	home := pluggedHome(t, `
export const agent = { id: "facts", name: "Facts", config: "~/.facts.json", model: "model" };
export function connect({ gateway, model, models }) {
  return { "seen": JSON.stringify({ model, models }) };
}
`)
	if err := provider.SetModelPrice("deepseek/pro", &catalog.Price{Input: 0.3, Output: 1.2, CacheRead: 0.03}); err != nil {
		t.Fatal(err)
	}
	a, err := Find("facts")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".facts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct{ Seen string }
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatalf("%v:\n%s", err, b)
	}
	type entry struct {
		ID        string            `json:"id"`
		Native    []string          `json:"native_endpoints"`
		Pricing   map[string]string `json:"pricing"`
		HasPrice  bool              `json:"-"`
		HasNative bool              `json:"-"`
	}
	var seen struct {
		Model  map[string]json.RawMessage   `json:"model"`
		Models []map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal([]byte(file.Seen), &seen); err != nil {
		t.Fatalf("%v:\n%s", err, file.Seen)
	}
	read := func(raw map[string]json.RawMessage) entry {
		var e entry
		b, _ := json.Marshal(raw)
		json.Unmarshal(b, &e)
		_, e.HasPrice = raw["pricing"]
		_, e.HasNative = raw["native_endpoints"]
		return e
	}
	byID := map[string]entry{}
	for _, m := range seen.Models {
		e := read(m)
		byID[e.ID] = e
	}
	pro, flash, model := byID["deepseek/pro"], byID["deepseek/flash"], read(seen.Model)
	want := map[string]string{"prompt": "0.0000003", "completion": "0.0000012", "input_cache_read": "0.00000003"}
	for name, e := range map[string]entry{"models' pro": pro, "model": model} {
		if len(e.Native) != 1 || e.Native[0] != "/v1/chat/completions" {
			t.Errorf("%s native_endpoints = %v, want [/v1/chat/completions]:\n%s", name, e.Native, file.Seen)
		}
		if len(e.Pricing) != len(want) {
			t.Errorf("%s pricing = %v, want %v", name, e.Pricing, want)
		}
		for k, v := range want {
			if e.Pricing[k] != v {
				t.Errorf("%s pricing[%s] = %q, want %q", name, k, e.Pricing[k], v)
			}
		}
	}
	if flash.ID == "" || !flash.HasNative {
		t.Errorf("flash missing or without native_endpoints:\n%s", file.Seen)
	}
	if flash.HasPrice {
		t.Errorf("unpriced flash has pricing %v, want none", flash.Pricing)
	}
	// what connect was told before stays as it was
	for _, k := range []string{"id", "name", "context", "output", "images", "efforts"} {
		if _, ok := seen.Model[k]; !ok {
			t.Errorf("model lost %q:\n%s", k, file.Seen)
		}
	}
}

// An agent plugin's connect is given magpie's decision models as
// decisionModels, apart from models (baranwang on X: a plugin wiring
// magpie's judging models into its agent had no way to learn them):
// each with its id, its provider and the gateway paths it is asked on,
// a routing group of them too, and none of them in models, which agents
// offer for a conversation.
func TestPluggedConnectSeesDecisionModels(t *testing.T) {
	home := pluggedHome(t, `
export const agent = { id: "judged", name: "Judged", config: "~/.judged.json", model: "model" };
export function connect({ models, decisionModels }) {
  return { "seen": JSON.stringify({ models, decisionModels }) };
}
`)
	// no request reaches it: connect is only told of it
	if err := provider.Save(provider.Provider{ID: "mixed", Name: "Mixed", Key: "k", Chat: "http://127.0.0.1:9/v1", Decide: "http://127.0.0.1:9/v1", Models: []string{"deepseek-v4.1-flash", "typesafe/jev"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "judges", Name: "Judges", Members: []string{"mixed/typesafe/jev"}}); err != nil {
		t.Fatal(err)
	}
	a, err := Find("judged")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".judged.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct{ Seen string }
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatalf("%v:\n%s", err, b)
	}
	type decider struct {
		ID        string   `json:"id"`
		Name      string   `json:"name"`
		Provider  string   `json:"provider"`
		Group     bool     `json:"group"`
		Endpoints []string `json:"endpoints"`
	}
	var seen struct {
		Models   []struct{ ID string } `json:"models"`
		Deciders []decider             `json:"decisionModels"`
	}
	if err := json.Unmarshal([]byte(file.Seen), &seen); err != nil {
		t.Fatalf("%v:\n%s", err, file.Seen)
	}
	byID := map[string]decider{}
	for _, d := range seen.Deciders {
		byID[d.ID] = d
	}
	want := []string{"/v1/systemone", "/v1/decisions"}
	jev, ok := byID["mixed/typesafe/jev"]
	if !ok || jev.Provider != "mixed" || jev.Group || jev.Name == "" || !slices.Equal(jev.Endpoints, want) {
		t.Errorf("decisionModels' mixed/typesafe/jev = %+v (found %v), want provider mixed, a name, endpoints %v:\n%s", jev, ok, want, file.Seen)
	}
	g, ok := byID[provider.GroupPrefix+"judges"]
	if !ok || !g.Group || g.Name != "Judges" || !slices.Equal(g.Endpoints, want) {
		t.Errorf("decisionModels' group = %+v (found %v), want group Judges on %v:\n%s", g, ok, want, file.Seen)
	}
	chat := false
	for _, m := range seen.Models {
		if m.ID == "mixed/typesafe/jev" || m.ID == provider.GroupPrefix+"judges" {
			t.Errorf("models has decision model %s:\n%s", m.ID, file.Seen)
		}
		chat = chat || m.ID == "mixed/deepseek-v4.1-flash"
	}
	if !chat {
		t.Errorf("models lost mixed's chat model:\n%s", file.Seen)
	}
	for _, d := range seen.Deciders {
		if d.ID == "deepseek/pro" || d.ID == "mixed/deepseek-v4.1-flash" {
			t.Errorf("decisionModels has chat model %s", d.ID)
		}
	}
}
