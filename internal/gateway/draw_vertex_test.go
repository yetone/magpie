package gateway

import (
	"encoding/base64"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// vertexDrawn is a Gemini image model's generateContent answer on Vertex
// AI, in the shape Google documents for it (candidates[].content.parts[],
// the picture as inlineData beside a line of text): #7263 gave no answer
// of its own, since it never reached this endpoint.
func vertexDrawn() string {
	return `{"candidates":[{"content":{"role":"model","parts":[{"text":"Here is a magpie."},{"inlineData":{"mimeType":"image/png","data":"` +
		base64.StdEncoding.EncodeToString(pngBytes) + `"}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":6,"candidatesTokenCount":1290,"totalTokenCount":1296,"trafficType":"ON_DEMAND"},"modelVersion":"gemini-3.1-flash-image"}`
}

// A Gemini image model on Google Vertex AI draws on Vertex AI's own
// generateContent at the project, signed with the Google token, not on
// the OpenAI-compatible chat completions its partner models are served
// on, which answered #7263's gemini-3.1-flash-image with no picture (502
// "drew nothing"). The id as a relay writes it (google/…) is asked
// without its google/. An express-mode key draws on the global endpoint,
// the key in x-goog-api-key, and lists its Gemini image models.
func TestVertexGeminiImageDrawsOnGenerateContent(t *testing.T) {
	v := &vertexUp{reply: func(int, map[string]any) (int, string) { return http.StatusOK, vertexDrawn() }}
	s := vertexServe(t, v, `{}`)
	for _, model := range []string{"gemini-3.1-flash-image", "google/gemini-3.1-flash-image", "gemini-nano-banana-2.1"} {
		code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"google-vertex/`+model+`","prompt":"a magpie","size":"1536x1024"}`)
		if code != 200 || len(a.Data) != 1 {
			t.Fatalf("%s: %d %s", model, code, raw)
		}
		if b, _ := base64.StdEncoding.DecodeString(a.Data[0].B64); string(b) != string(pngBytes) {
			t.Fatalf("%s: image %q", model, b)
		}
		got := v.last(t)
		bare := strings.TrimPrefix(model, "google/")
		if want := "/v1/projects/proj-1/locations/global/publishers/google/models/" + bare + ":generateContent"; got.path != want || got.query != "" {
			t.Fatalf("%s was asked at %s?%s, want %s", model, got.path, got.query, want)
		}
		if got.auth != "Bearer ya29.vertex-test" || got.key != "" {
			t.Fatalf("%s was signed %q, key %q", model, got.auth, got.key)
		}
		gen, _ := got.body["generationConfig"].(map[string]any)
		if mods, _ := gen["responseModalities"].([]any); !slices.Equal(mods, []any{"TEXT", "IMAGE"}) {
			t.Fatalf("%s was sent %v", model, got.body)
		}
		if ic, _ := gen["imageConfig"].(map[string]any); ic["aspectRatio"] != "3:2" {
			t.Fatalf("%s: imageConfig %v", model, gen["imageConfig"])
		}
	}

	// express mode: a key and no project
	if err := provider.Save(provider.Provider{ID: "google-vertex", Name: "Google Vertex AI", Preset: provider.VertexPreset, Key: "AQ.Ab8RN6-gw",
		Models: []string{"gemini-3.1-flash-image", "gemini-3.8-flash"}}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("google-vertex")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range Drawers(*p) {
		ids = append(ids, m.ID)
	}
	if !slices.Contains(ids, "gemini-3.1-flash-image") || slices.ContainsFunc(ids, func(id string) bool { return !vertexDrawsGemini(*p, id) }) {
		t.Fatalf("express drawers %v", ids)
	}
	s = New()
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"google-vertex/gemini-3.1-flash-image","prompt":"a magpie"}`)
	if code != 200 || len(a.Data) != 1 {
		t.Fatalf("express: %d %s", code, raw)
	}
	got := v.last(t)
	if got.path != "/v1/publishers/google/models/gemini-3.1-flash-image:generateContent" || got.key != "AQ.Ab8RN6-gw" || got.auth != "" || strings.Contains(got.query, "AQ.") {
		t.Fatalf("express was asked %+v", got)
	}
	for _, a := range v.asked {
		if strings.Contains(a.path, "/endpoints/openapi/") {
			t.Fatalf("a drawing went to chat completions: %s", a.path)
		}
	}
}

// Only Vertex AI's Gemini image models are drawn so: AI Studio's key keeps
// its own generateContent, a relay's gemini-3.1-flash-image (named with
// google/ or not) stays on chat completions, and at Vertex AI a partner
// model, Claude and an Imagen are no Gemini drawers.
func TestVertexGeminiDrawOnlyForVertex(t *testing.T) {
	studio := provider.Provider{ID: "gemini", Chat: "https://generativelanguage.googleapis.com/v1beta/openai", Key: "k"}
	relay := provider.Provider{ID: "relay", Chat: "https://relay.example/v1", Key: "k"}
	openrouter := provider.Provider{ID: "openrouter", Chat: "https://openrouter.ai/api/v1", Key: "k"}
	vertex := provider.Provider{ID: "google-vertex", Preset: provider.VertexPreset, Vertex: &provider.Vertex{Project: "proj-1", Location: "global"}}
	for _, c := range []struct {
		p     provider.Provider
		model string
		via   drawVia
		vtx   bool
	}{
		{studio, "gemini-3.1-flash-image", viaGemini, false},
		{relay, "gemini-3.1-flash-image", viaChat, false},
		{relay, "google/gemini-3.1-flash-image", viaChat, false},
		{openrouter, "google/gemini-3.1-flash-image", viaChat, false},
		{vertex, "gemini-3.1-flash-image", viaGemini, true},
		{vertex, "google/gemini-3.1-flash-image", viaGemini, true},
		{vertex, "models/gemini-2.5-flash-image", viaGemini, true},
		{vertex, "deepseek-ai/deepseek-v4.1-flash-maas", viaChat, false},
		{vertex, "claude-opus-5-5", viaChat, false},
		{vertex, "imagen-4.0-generate-001", viaImages, false},
	} {
		if got := viaFor(c.p, c.model); got != c.via {
			t.Errorf("%s %s: drawn on %s, want %s", c.p.ID, c.model, got, c.via)
		}
		if got := vertexDrawsGemini(c.p, c.model); got != c.vtx {
			t.Errorf("%s %s: Vertex Gemini drawer %v, want %v", c.p.ID, c.model, got, c.vtx)
		}
	}
}
