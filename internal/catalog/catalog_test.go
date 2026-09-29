package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCatalog puts a valid models.dev-shaped file where load() will find it;
// load() falls back to the machine's own caches, so a malformed file would
// silently read the developer's real catalog.
func writeCatalog(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "magpie"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(CachePath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	Reset()
	t.Cleanup(Reset)
}

func TestProviderReadsTemperatureCapability(t *testing.T) {
	writeCatalog(t, `{
	  "openai": {
	    "models": {
	      "gpt-5.5": {"id":"gpt-5.5","name":"GPT-5.5","temperature":false,
	                  "reasoning_options":[{"type":"effort","values":["low","high"]}]},
	      "gpt-4.1": {"id":"gpt-4.1","name":"GPT-4.1","temperature":true}
	    }
	  }
	}`)

	ms := Provider("openai")
	if len(ms) != 2 {
		t.Fatalf("models: %+v", ms)
	}
	byID := map[string]Model{}
	for _, m := range ms {
		byID[m.ID] = m
	}
	if p := byID["gpt-5.5"].Temperature; p == nil || *p {
		t.Fatalf("gpt-5.5 temperature: %v", p)
	}
	if p := byID["gpt-4.1"].Temperature; p == nil || !*p {
		t.Fatalf("gpt-4.1 temperature: %v", p)
	}
	if got := byID["gpt-5.5"].Efforts; len(got) != 2 || got[0] != "low" {
		t.Fatalf("efforts: %v", got)
	}
}

func TestDecorateCarriesTemperature(t *testing.T) {
	no, yes := false, true
	live := []Model{{ID: "new-model"}, {ID: "gpt-5.5"}}
	known := []Model{
		{ID: "gpt-5.5", Name: "GPT-5.5", Temperature: &no, Released: "2026-04-23"},
		{ID: "old", Temperature: &yes},
	}
	out := Decorate(live, known)
	if out[0].Temperature != nil {
		t.Fatalf("an unknown model gained a capability: %v", *out[0].Temperature)
	}
	if out[1].Temperature == nil || *out[1].Temperature || out[1].Name != "GPT-5.5" {
		t.Fatalf("decorated: %+v", out[1])
	}
}

// A model takes images where models.dev says so; one listed by several
// providers takes them as most of those say, whatever a vendor prefixes.
func TestImages(t *testing.T) {
	writeCatalog(t, `{
	  "a": {"models": {
	    "vision": {"id":"vision","name":"V","modalities":{"input":["text","image"],"output":["text"]}},
	    "text":   {"id":"text","name":"T","modalities":{"input":["text"],"output":["text"]}}
	  }},
	  "b": {"models": {"text": {"id":"text","name":"T","modalities":{"input":["text"],"output":["text"]}}}},
	  "c": {"models": {"org/text": {"id":"org/text","name":"T","modalities":{"input":["text","image"],"output":["text"]}}}}
	}`)
	for _, m := range Provider("a") {
		if m.Images != (m.ID == "vision") {
			t.Errorf("%s: images %v", m.ID, m.Images)
		}
	}
	if !SeesImages("z-ai/Vision") || SeesImages("text") || SeesImages("unknown") {
		t.Error("SeesImages")
	}
}

// A model's window is models.dev's input limit where it gives one (what a
// prompt may hold), else its context; looked up by bare id, it is the size
// most providers give, so a relay's model finds it too.
func TestContextOf(t *testing.T) {
	writeCatalog(t, `{
	  "openai": {"models": {
	    "gpt-5.5": {"id":"gpt-5.5","limit":{"context":1050000,"input":922000,"output":128000}},
	    "gpt-4.1": {"id":"gpt-4.1","limit":{"context":1047576,"output":32768}}
	  }},
	  "zai": {"models": {"glm-4.6": {"id":"glm-4.6","limit":{"context":204800}}}},
	  "a": {"models": {"zai/glm-4.6": {"id":"zai/glm-4.6","limit":{"context":204800}}}},
	  "b": {"models": {"glm-4.6": {"id":"glm-4.6","limit":{"context":128000}}}}
	}`)
	for id, want := range map[string]int{
		"gpt-5.5":        922000,
		"openai/gpt-5.5": 922000,
		"gpt-5.5(high)":  922000,
		"gpt-4.1":        1047576,
		"GLM-4.6":        204800,
		"glm-4.6:free":   204800,
		"unknown-model":  0,
	} {
		if got := ContextOf(id); got != want {
			t.Errorf("ContextOf(%q) = %d, want %d", id, got, want)
		}
	}
	for _, m := range Provider("openai") {
		if m.ID == "gpt-5.5" && m.Context != 922000 {
			t.Errorf("Provider: %+v", m)
		}
	}
	if out := Decorate([]Model{{ID: "gpt-5.5"}}, Provider("openai")); out[0].Context != 922000 {
		t.Errorf("Decorate: %+v", out[0])
	}
}

// A Bedrock inference profile of a geography models.dev doesn't list the
// model in (apac.) takes the model id's window, output and levels.
func TestBedrockProfiles(t *testing.T) {
	writeCatalog(t, `{
	  "amazon-bedrock": {"models": {
	    "anthropic.claude-opus-5-5": {"id":"anthropic.claude-opus-5-5","limit":{"context":1000000,"output":128000},"reasoning_options":[{"type":"effort","values":["low","high"]}]},
	    "global.anthropic.claude-opus-5-5": {"id":"global.anthropic.claude-opus-5-5","limit":{"context":1000000,"output":128000}}
	  }}
	}`)
	for _, id := range []string{"apac.anthropic.claude-opus-5-5", "us-gov.anthropic.claude-opus-5-5", "global.anthropic.claude-opus-5-5"} {
		if ContextOf(id) != 1000000 || OutputOf(id) != 128000 {
			t.Errorf("%s: %d %d", id, ContextOf(id), OutputOf(id))
		}
	}
	if got := strings.Join(EffortsOf("apac.anthropic.claude-opus-5-5"), ","); got != "low,high" {
		t.Errorf("efforts: %q", got)
	}
	// not a profile: a model named with a dot
	if ContextOf("us.") != 0 || ContextOf("eu.unknown") != 0 {
		t.Error("a bare geography taken for a profile")
	}
}

// A vendor models.dev doesn't list takes the levels the providers serving
// the model give; one that gives none (a toggle, nothing) doesn't vote.
func TestEffortsOf(t *testing.T) {
	writeCatalog(t, `{
	  "zai": {"models": {"glm-5.3-flash": {"id":"glm-5.3-flash","reasoning_options":[{"type":"effort","values":["low","high","max"]}]}}},
	  "a": {"models": {"z-ai/glm-5.3-flash": {"id":"z-ai/glm-5.3-flash","reasoning_options":[{"type":"effort","values":["low","high","max"]}]}}},
	  "b": {"models": {"glm-5.3-flash": {"id":"glm-5.3-flash","reasoning_options":[{"type":"effort","values":["none","low","medium","high"]}]},
	                   "glm-5": {"id":"glm-5","reasoning_options":[{"type":"toggle"}]},
	                   "deepseek-chat": {"id":"deepseek-chat"}}},
	  "c": {"models": {"glm-5.3-flash": {"id":"glm-5.3-flash","reasoning_options":[{"type":"toggle"}]}}}
	}`)
	for id, want := range map[string]string{
		"glm-5.3-flash":       "low,high,max",
		"GLM-5.3-Flash":       "low,high,max",
		"volc/glm-5.3-flash":  "low,high,max",
		"glm-5.3-flash:free":  "low,high,max",
		"glm-5.3-flash(high)": "low,high,max",
		"glm-5":               "",
		"deepseek-chat":       "",
		"unknown-model":       "",
	} {
		if got := strings.Join(EffortsOf(id), ","); got != want {
			t.Errorf("EffortsOf(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestFetchedImageCapabilityOverridesCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[
			{"id":"vision","modalities":{"input":["text","image"]}},
			{"id":"text","modalities":{"input":["text"]}},
			{"id":"unknown"}
		]}`))
	}))
	defer server.Close()
	live, err := Fetch(context.Background(), server.URL, "", false, nil)
	if err != nil || len(live) != 3 {
		t.Fatalf("fetched models: %+v, %v", live, err)
	}
	known := []Model{
		{ID: "vision", Images: true, ImageInput: imageInput([]string{"text", "image"})},
		{ID: "text", Images: true, ImageInput: imageInput([]string{"text", "image"})},
		{ID: "unknown", Images: true, ImageInput: imageInput([]string{"text", "image"})},
	}
	got := Decorate(live, known)
	if got[0].ImageInput == nil || !*got[0].ImageInput || !got[0].Images ||
		got[1].ImageInput == nil || *got[1].ImageInput || got[1].Images ||
		got[2].ImageInput == nil || !*got[2].ImageInput || !got[2].Images {
		t.Fatalf("source precedence: %+v", got)
	}
}
