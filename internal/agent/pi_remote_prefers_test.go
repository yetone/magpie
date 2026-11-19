package agent

import (
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// OoopsHh on X: with another magpie as the provider, Pi's models.json had
// only codex/* on openai-responses (a ChatGPT sign-in's models are listed
// on /v1/responses alone); an OpenAI key's openai/* and a routing group's
// auto/*, best asked on Responses there too, went on openai-completions,
// where the vendor turned away effort max. The remote's list now says the
// API it asks each model on best (magpie_prefers), and every agent that
// asks each model on its own API (Pi, omp, droid, dsh, through
// catalog.Model.APIs) is wired to it. A model best on Chat there, and one
// from a magpie from before that says nothing, stay on Chat.
func TestPiAsksARemoteMagpiesModelWhereItPrefers(t *testing.T) {
	syncHome(t)
	ids := []string{"openai/gpt-6.2-astra", "group/auto", "kimi/kimi-k3", "old/gpt-6.2-sol"}
	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Chat: "http://127.0.0.1:2", Key: "k", Models: ids}); err != nil {
		t.Fatal(err)
	}
	office, err := provider.Find("office")
	if err != nil {
		t.Fatal(err)
	}
	efforts := []string{"low", "medium", "high", "xhigh", "max"}
	if err := catalog.SaveLive("office", office.Chat, []catalog.Model{
		{ID: "openai/gpt-6.2-astra", Efforts: efforts, APIs: []string{"chat", "responses"}, Prefers: "responses"},
		{ID: "group/auto", Efforts: efforts, Prefers: "responses"},
		{ID: "kimi/kimi-k3", APIs: []string{"chat"}, Prefers: "chat"},
		{ID: "old/gpt-6.2-sol", Efforts: efforts, APIs: []string{"chat", "responses"}},
	}); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"office/openai/gpt-6.2-astra": "openai-responses", "office/group/auto": "openai-responses", "office/kimi/kimi-k3": nil, "office/old/gpt-6.2-sol": nil}
	seen := 0
	for _, m := range magpieModels("pi") {
		w, ok := want[m.ID]
		if !ok {
			continue
		}
		seen++
		e := piModelJSON(piAsked("pi", m), "http://127.0.0.1:3425", true)
		if e["api"] != w {
			t.Errorf("%s: api %v, want %v (%v)", m.ID, e["api"], w, e)
		}
		if w == "openai-responses" {
			if l, _ := e["thinkingLevelMap"].(map[string]any); l["max"] != "max" {
				t.Errorf("%s: levels %v", m.ID, e["thinkingLevelMap"])
			}
		}
	}
	if seen != len(want) {
		t.Fatalf("listed %d of %v", seen, want)
	}
}
