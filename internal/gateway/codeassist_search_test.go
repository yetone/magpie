package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// Gemini on a Google sign-in searches by itself, googleSearch, for a
// request with no function tools; Antigravity refuses googleSearch beside
// them, so one with tools is given magpie's search instead (#757).
func TestCodeAssistSearchesWithoutTools(t *testing.T) {
	ag := provider.Provider{ID: "antigravity", Account: &provider.Account{Agent: "antigravity", User: "u"}}
	plain := &Request{WebSearch: true, Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "news?"}}}}}
	tools := &Request{WebSearch: true, Messages: plain.Messages, Tools: []Tool{{Name: "bash"}}}
	if !searchesFor(ag, provider.CodeAssist, "gemini-3.8-flash", plain) {
		t.Error("Gemini without tools doesn't search by itself")
	}
	if searchesFor(ag, provider.CodeAssist, "gemini-3.8-flash", tools) {
		t.Error("Gemini with tools is said to search by itself")
	}
	if searchesFor(ag, provider.CodeAssist, "claude-sonnet-4-6", plain) || searchesFor(ag, provider.CodeAssist, "gemini-3.1-flash-image", plain) {
		t.Error("Claude or an image model is said to search by googleSearch")
	}
	key := provider.Provider{ID: "g", Chat: "https://example.invalid/v1", Key: "k"}
	if searchesFor(key, provider.CodeAssist, "gemini-3.8-flash", plain) {
		t.Error("a provider without a Google sign-in searches by Code Assist")
	}

	var env struct {
		Request map[string]json.RawMessage `json:"request"`
	}
	json.Unmarshal(buildCodeAssist(plain, "gemini-3.8-flash", "antigravity"), &env)
	if got := string(env.Request["tools"]); got != `[{"googleSearch":{}}]` {
		t.Errorf("tools = %s", got)
	}
	env.Request = nil
	json.Unmarshal(buildCodeAssist(tools, "gemini-3.8-flash", "antigravity"), &env)
	if got := string(env.Request["tools"]); strings.Contains(got, "googleSearch") || !strings.Contains(got, "functionDeclarations") {
		t.Errorf("tools with function tools = %s", got)
	}
}

// What googleSearch found comes back as a search the client is told of,
// once, with its pages, before the stop.
func TestCodeAssistDecodesGrounding(t *testing.T) {
	var got []Event
	d := &codeAssistDecoder{}
	for _, line := range []string{
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Go 1.27.1 is the latest."}]}}]}}`,
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP",
			"groundingMetadata":{"webSearchQueries":["latest go release"],"groundingChunks":[{"web":{"uri":"https://vertexaisearch.cloud.google.com/grounding-api-redirect/a","title":"go.dev"}},{"web":{"uri":"https://vertexaisearch.cloud.google.com/grounding-api-redirect/b"}}]}}],
			"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}}`,
		`{"response":{"candidates":[{"content":{"role":"model","parts":[]},"groundingMetadata":{"webSearchQueries":["latest go release"]}}]}}`,
	} {
		d.decode(line, func(ev Event) { got = append(got, ev) })
	}
	var kinds []EventKind
	var search Event
	for _, ev := range got {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == KSearch {
			search = ev
		}
	}
	want := []EventKind{KStart, KText, KSearch, KStop, KUsage}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds = %v, want %v", kinds, want)
		}
	}
	if search.Text != "latest go release" || len(search.Hits) != 2 || search.Hits[0].Title != "go.dev" ||
		search.Hits[1].Title != search.Hits[1].URL || !strings.HasSuffix(search.Hits[0].URL, "/a") {
		t.Errorf("search = %+v", search)
	}
}

// An image model on Code Assist is asked for images beside text, or it
// answers in words only.
func TestCodeAssistImageModelAsksForImages(t *testing.T) {
	r := &Request{Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "draw a cat"}}}}}
	if b := string(buildCodeAssist(r, "gemini-3.1-flash-image", "antigravity")); !strings.Contains(b, `"responseModalities":["TEXT","IMAGE"]`) {
		t.Errorf("image model: %s", b)
	}
	if b := string(buildCodeAssist(r, "gemini-3.8-flash", "antigravity")); strings.Contains(b, "responseModalities") {
		t.Errorf("text model: %s", b)
	}
}

// "gemini" has a "mini" in it: a Gemini Pro listed first was taken for a
// small model and searched with (#757).
func TestSmallModelOfGemini(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	testenv.SetHome(t, t.TempDir())
	p := provider.Provider{ID: "g", Name: "G", Chat: "https://example.invalid/v1", Key: "k",
		Models: []string{"gemini-pro-agent", "gemini-3.1-pro-low", "gemini-3-flash", "gemini-3.1-flash-lite"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	var live []catalog.Model
	for _, id := range p.Models {
		live = append(live, catalog.Model{ID: id})
	}
	if err := catalog.SaveLive(p.ID, p.Chat, live); err != nil {
		t.Fatal(err)
	}
	if got := smallModel(p, nil); got != "gemini-3-flash" {
		t.Errorf("small model = %q", got)
	}
}
