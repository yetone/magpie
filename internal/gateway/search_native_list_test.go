package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A Remote magpie's list says "native" for every model it would itself
// search with by the model alone, so a magpie that has it as its Remote
// magpie may pick that model to search with (#7475, Oranguice): a Claude
// subscription's models (Claude Code's own WebSearch) and an AI Studio
// key's Gemini (googleSearch) were listed as "magpie", and the other
// magpie's searcher picker had Codex's models alone.
func TestRemoteListSaysNativeForEverySearcher(t *testing.T) {
	claude := provider.Provider{ID: "claude", Name: "Claude", Account: &provider.Account{Agent: "claude", User: "u@example.com"}}
	studio := provider.Provider{ID: "google", Name: "Google Gemini", Chat: provider.AIStudioChat, Gemini: provider.AIStudioGemini, Key: "k"}
	vertex := provider.Provider{ID: "google-vertex", Name: "Google Vertex AI", Preset: provider.VertexPreset, Key: "k",
		Vertex: &provider.Vertex{Location: "global"}}
	plain := provider.Provider{ID: "plain", Name: "Plain", Chat: "https://plain.example/v1", Key: "k"}
	for _, c := range []struct {
		p     provider.Provider
		model string
		want  string
	}{
		{claude, "claude-haiku-5-5", searchNative},
		{claude, "claude-opus-5-5", searchNative},
		{studio, "gemini-4-carbon", searchNative},
		// googleSearch is Gemini's: an image model on the key doesn't
		// search, and is searched for by this magpie
		{studio, "gemini-3-pro-image", searchMagpie},
		// Vertex AI is sent no googleSearch nor web_search by magpie
		// (searchesFor), so its models don't search by themselves here
		{vertex, "gemini-4-carbon", searchMagpie},
		{vertex, "claude-sonnet-5-5", searchMagpie},
		{plain, "m1", searchMagpie},
	} {
		if got := webSearchOf(provider.Entry{Provider: &c.p, Model: c.model}, true); got != c.want {
			t.Errorf("%s/%s: web_search %q, want %q", c.p.ID, c.model, got, c.want)
		}
	}
	// a routing group is one this magpie searches for, whatever its
	// members do
	if got := webSearchOf(provider.Entry{Provider: &claude, Model: "claude-haiku-5-5", Group: "auto/fast"}, true); got != searchMagpie {
		t.Errorf("a group of Claude's: web_search %q, want %q", got, searchMagpie)
	}
}
