package gateway

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// argonBudgetRefused is Gemini 4 Argon's answer to a thinkingBudget, as
// #2083 quotes it.
const argonBudgetRefused = `{"error":{"code":400,"message":"Thinking budget is not supported for this model. Use thinking level instead. See https://ai.google.dev/gemini-api/docs/thinking for details.","status":"INVALID_ARGUMENT"}}`

// #2083: a provider the user added that speaks Gemini's API, its models
// from models.dev's google-vertex, as the reporter set it up. Gemini 4
// Argon is told a thinking level, and medium as medium, the levels it has:
// it turns every budget away with a 400. Gemini 3.8 Flash is told its level
// as before, Gemini 2.5 still a budget, and a Gemini 3 Pro medium as high.
func TestGeminiLaterGenerationsToldALevel(t *testing.T) {
	fresh(t)
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"google-vertex":{"models":{
	"gemini-4-neon":{"id":"gemini-4-neon","reasoning_options":[{"type":"effort","values":["low","medium","high","xhigh"]}]},
	"gemini-4-argon":{"id":"gemini-4-argon","reasoning_options":[{"type":"effort","values":["low","medium","high"]}]},
	"gemini-3.8-flash":{"id":"gemini-3.8-flash","reasoning_options":[{"type":"effort","values":["low","medium","high"]}]},
	"gemini-2.5-flash":{"id":"gemini-2.5-flash","reasoning_options":[{"type":"effort","values":["low","medium","high"]}]}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	var mu sync.Mutex
	asked := map[string]map[string]any{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(b, &body)
		model := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1beta/models/"), ":streamGenerateContent")
		gen, _ := body["generationConfig"].(map[string]any)
		tc, _ := gen["thinkingConfig"].(map[string]any)
		mu.Lock()
		asked[model] = tc
		mu.Unlock()
		if _, budget := tc["thinkingBudget"]; budget && strings.HasPrefix(model, "gemini-4") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, argonBudgetRefused)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, vertexPong)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "gv", Name: "Google", Key: "k", Catalog: "google-vertex",
		Gemini: up.URL + "/v1beta", Models: []string{"gemini-4-neon", "gemini-4-argon", "gemini-3.8-flash", "gemini-2.5-flash", "gemini-3-pro-preview"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	level := func(l string) map[string]any { return map[string]any{"thinkingLevel": l, "includeThoughts": true} }
	budget := func(n float64) map[string]any { return map[string]any{"thinkingBudget": n, "includeThoughts": true} }
	for _, c := range []struct {
		path, model, effort string
		want                map[string]any
	}{
		{"/v1/chat/completions", "gemini-4-neon", "low", level("low")},
		{"/v1/chat/completions", "gemini-4-neon", "medium", level("medium")},
		{"/v1/chat/completions", "gemini-4-neon", "high", level("high")},
		// Gemini 4 Neon has xhigh of its own: it goes up as xhigh, not held
		// at high (the reporter's case). Argon, without xhigh, keeps high.
		{"/v1/chat/completions", "gemini-4-neon", "xhigh", level("xhigh")},
		{"/v1/responses", "gemini-4-neon", "xhigh", level("xhigh")},
		{"/v1/chat/completions", "gemini-4-argon", "xhigh", level("high")},
		{"/v1/chat/completions", "gemini-4-argon", "low", level("low")},
		{"/v1/chat/completions", "gemini-4-argon", "medium", level("medium")},
		{"/v1/chat/completions", "gemini-4-argon", "high", level("high")},
		{"/v1/responses", "gemini-4-argon", "high", level("high")},
		{"/v1/chat/completions", "gemini-3.8-flash", "low", level("low")},
		{"/v1/chat/completions", "gemini-3.8-flash", "medium", level("medium")},
		{"/v1/chat/completions", "gemini-3.8-flash", "high", level("high")},
		{"/v1/chat/completions", "gemini-3-pro-preview", "medium", level("high")},
		{"/v1/chat/completions", "gemini-2.5-flash", "high", budget(24000)},
	} {
		body := `{"model":"gv/` + c.model + `","stream":true,"reasoning_effort":"` + c.effort + `","messages":[{"role":"user","content":"hi"}]}`
		if c.path == "/v1/responses" {
			body = `{"model":"gv/` + c.model + `","stream":true,"reasoning":{"effort":"` + c.effort + `"},"input":"hi"}`
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", c.path, strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Errorf("%s %s at %s: status %d: %s", c.path, c.model, c.effort, rec.Code, rec.Body.String())
			continue
		}
		mu.Lock()
		got := asked[c.model]
		mu.Unlock()
		if !maps.Equal(got, c.want) {
			t.Errorf("%s %s at %s: thinkingConfig %v, want %v", c.path, c.model, c.effort, got, c.want)
		}
	}
}

// geminiGeneration reads the generation from the id however it is
// written; an id that names none, an alias among them, is 0 and keeps its
// budget.
func TestGeminiGeneration(t *testing.T) {
	for id, want := range map[string]int{
		"gemini-4-argon": 4, "models/gemini-4-argon": 4, "gemini-3.8-flash": 3, "gemini-2.5-pro": 2,
		"gemini-10-pro": 10, "gemini-flash-latest": 0, "argon-gemini-3": 0, "gemma-3-27b-it": 0, "claude-sonnet-4-5": 0,
	} {
		if got := geminiGeneration(id); got != want {
			t.Errorf("%s: %d, want %d", id, got, want)
		}
	}
}
