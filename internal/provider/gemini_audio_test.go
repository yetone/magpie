package provider

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// geminiKindsCatalog is models.dev's rows for the models below, as
// models.dev gives them (the bundled copy, 2026-11-15): Vertex AI's Gemini
// 3.5 Flash with its audio, video and pdf, one relay that lists it with
// text and image alone, and Vertex AI's text-only GPT OSS.
const geminiKindsCatalog = `{
"google-vertex":{"id":"google-vertex","name":"Vertex","models":{
 "gemini-3.5-flash":{"id":"gemini-3.5-flash","name":"Gemini 3.5 Flash","attachment":true,"reasoning":true,"tool_call":true,"release_date":"2026-05-19","modalities":{"input":["text","image","video","audio","pdf"],"output":["text"]},"limit":{"context":1048576,"output":65536}},
 "openai/gpt-oss-120b-maas":{"id":"openai/gpt-oss-120b-maas","name":"GPT OSS 120B","attachment":false,"reasoning":true,"tool_call":true,"release_date":"2025-08-05","modalities":{"input":["text"],"output":["text"]},"limit":{"context":131072,"output":131072}}}},
"google":{"id":"google","name":"Google","models":{
 "gemini-3.5-flash":{"id":"gemini-3.5-flash","name":"Gemini 3.5 Flash","attachment":true,"reasoning":true,"tool_call":true,"release_date":"2026-05-19","modalities":{"input":["text","image","video","audio","pdf"],"output":["text"]},"limit":{"context":1048576,"output":65536}}}},
"somerelay":{"id":"somerelay","name":"Some relay","models":{
 "google/gemini-3.5-flash":{"id":"google/gemini-3.5-flash","name":"Gemini 3.5 Flash","attachment":true,"tool_call":true,"release_date":"2026-05-19","modalities":{"input":["text","image"],"output":["text"]},"limit":{"context":1048576,"output":65536}}}}
}`

func geminiKinds(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(geminiKindsCatalog), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

// A Gemini model a list gives bare — Vertex AI's, which magpie names
// itself, or a relay's — is said to take what models.dev says it takes
// wherever it is served: audio, video and pdf beside text and image. It
// was said to take text and image alone, as agents guess for a model they
// are told nothing of (Parlevent_ on X, Gemini on Vertex AI). A text-only
// model stays text-only, and one whose list said what it takes keeps that.
func TestGeminiTakesAudioWhereverServed(t *testing.T) {
	vertexHome(t)
	geminiKinds(t)
	if err := Save(Provider{ID: "vx", Name: "vx", Preset: VertexPreset, Vertex: &Vertex{Project: "acme"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "relay", Name: "Relay", Gemini: "https://relay.example/v1beta", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("relay", "https://relay.example/v1beta", []catalog.Model{
		{ID: "gemini-3.5-flash", Name: "gemini-3.5-flash"},
		{ID: "said", InputModalities: []string{"text", "image"}, ImageInput: ptrBool(true)},
	}); err != nil {
		t.Fatal(err)
	}
	s := settings.Load()
	entry := func(pid, model string) Entry {
		t.Helper()
		p, err := Find(pid)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range p.Exposed() {
			if m.ID == model {
				return entryFor(*p, m, s)
			}
		}
		t.Fatalf("%s doesn't list %s", pid, model)
		return Entry{}
	}
	all := []string{"text", "image", "audio", "video", "pdf"}
	for _, c := range []struct {
		pid, model string
		in         []string
	}{
		{"vx", "gemini-3.5-flash", all},
		{"vx", "openai/gpt-oss-120b-maas", []string{"text"}},
		{"relay", "gemini-3.5-flash", all},
		{"relay", "said", []string{"text", "image"}},
	} {
		e := entry(c.pid, c.model)
		if in, _ := e.Modalities(); !slices.Equal(in, c.in) {
			t.Errorf("%s/%s takes %v, want %v", c.pid, c.model, in, c.in)
		}
	}
	// the user's word on images still decides image, and the rest stays
	if err := SetModelImage("vx/gemini-3.5-flash", ptrBool(false)); err != nil {
		t.Fatal(err)
	}
	s = settings.Load()
	if in, _ := entry("vx", "gemini-3.5-flash").Modalities(); !slices.Equal(in, []string{"text", "audio", "video", "pdf"}) {
		t.Errorf("images turned off: %v", in)
	}
}

func ptrBool(v bool) *bool { return &v }
