package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestGeminiBase(t *testing.T) {
	for in, want := range map[string]string{
		"":                                  "",
		"generativelanguage.googleapis.com": "https://generativelanguage.googleapis.com/v1beta",
		"https://generativelanguage.googleapis.com/":                                                           "https://generativelanguage.googleapis.com/v1beta",
		"https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse": "https://generativelanguage.googleapis.com/v1beta",
		"https://relay.example/proxy/v1beta/models":                                                            "https://relay.example/proxy/v1beta",
		"https://relay.example/proxy/v1":                                                                       "https://relay.example/proxy/v1",
	} {
		if got := GeminiBase(in); got != want {
			t.Errorf("GeminiBase(%q) = %q, want %q", in, got, want)
		}
	}
	if got := GeminiPath("models/gemini-2.5-pro", true); got != "/models/gemini-2.5-pro:streamGenerateContent?alt=sse" {
		t.Errorf("stream path %q", got)
	}
	if got := GeminiPath("gemini-2.5-flash", false); got != "/models/gemini-2.5-flash:generateContent" {
		t.Errorf("path %q", got)
	}
}

// A custom provider with a Gemini URL alone speaks Gemini upstream, is
// signed with x-goog-api-key, and lists the models its API serves for
// generateContent, a page at a time, under a base with a path prefix
// (#1346, NagaseMinato).
func TestCustomGeminiProvider(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var keys, paths []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("x-goog-api-key")+"|"+r.Header.Get("Authorization"))
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		if r.URL.Path != "/proxy/v1beta/models" {
			http.NotFound(w, r)
			return
		}
		page := map[string]any{"models": []map[string]any{
			{"name": "models/gemini-2.5-pro", "displayName": "Gemini 2.5 Pro", "supportedGenerationMethods": []string{"generateContent", "countTokens"}, "inputTokenLimit": 1048576, "outputTokenLimit": 65536},
			{"name": "models/text-embedding-004", "supportedGenerationMethods": []string{"embedContent"}},
		}, "nextPageToken": "p2"}
		if r.URL.Query().Get("pageToken") == "p2" {
			page = map[string]any{"models": []map[string]any{
				{"name": "models/gemini-2.5-flash", "supportedGenerationMethods": []string{"generateContent"}},
			}}
		}
		json.NewEncoder(w).Encode(page)
	}))
	defer up.Close()

	p := Provider{ID: "gem", Name: "Gem", Key: "AIza-test", Gemini: up.URL + "/proxy/v1beta/models/gemini-2.5-pro:generateContent"}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Find("gem")
	if err != nil {
		t.Fatal(err)
	}
	p = *got
	if p.Gemini != up.URL+"/proxy/v1beta" {
		t.Errorf("base kept as %q", p.Gemini)
	}
	if got := p.Speaks(); !slices.Equal(got, []Protocol{Gemini}) || p.Base(Gemini) != p.Gemini {
		t.Errorf("speaks %v at %q", got, p.Base(Gemini))
	}
	if h := AuthHeaders(p, Gemini); len(h) != 1 || h["x-goog-api-key"] != "AIza-test" {
		t.Errorf("auth %v", h)
	}
	if p.ModelTest() != "" {
		t.Errorf("not testable: %q", p.ModelTest())
	}
	ms, err := p.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	if !slices.Equal(ids, []string{"gemini-2.5-pro", "gemini-2.5-flash"}) || ms[0].Name != "Gemini 2.5 Pro" || ms[0].Context != 1048576 || ms[0].Output != 65536 {
		t.Errorf("listed %v: %+v", ids, ms)
	}
	if len(paths) != 2 || paths[0] != "/proxy/v1beta/models?pageSize=1000" || paths[1] != "/proxy/v1beta/models?pageSize=1000&pageToken=p2" {
		t.Errorf("asked %v", paths)
	}
	for _, k := range keys {
		if k != "AIza-test|" {
			t.Errorf("signed %q", k)
		}
	}
}

// Factory's sign-in keeps its own generate route and its Bearer token.
func TestFactoryGeminiUnchanged(t *testing.T) {
	p := Provider{ID: "factory", Key: "tok", Account: &Account{}}
	if !p.FactoryGemini() || p.Base(Gemini) != factoryAPI+"/api/llm/g/v1" {
		t.Errorf("factory base %q", p.Base(Gemini))
	}
	if h := AuthHeaders(p, Gemini); h["Authorization"] != "Bearer tok" || h["x-goog-api-key"] != "" {
		t.Errorf("factory auth %v", h)
	}
}
