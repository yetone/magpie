package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
)

// A System One provider at OpenRouter (ARNO on Discord): its /models lists
// chat models, Jev's router among them, and its decision models are at
// /models?output_modalities=decisions, the URL the user gave. Every model
// in that list is fetched, and a model typed in by hand is tested with a
// System One question of its own.
func TestDecideModelsURLAndModelTest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var mu sync.Mutex
	var asked []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			if r.URL.Query().Get("output_modalities") == "decisions" && r.URL.Query().Get("limit") == "500" {
				w.Write([]byte(`{"data":[{"id":"perplexity/pplx-decider-v1-27b","architecture":{"output_modalities":["decisions"]}},{"id":"liquid/d1"},{"id":"~typesafe/jev-latest"},{"id":"typesafe/jev-1.13"}],"total_count":4}`))
				return
			}
			w.Write([]byte(`{"data":[{"id":"openai/gpt-5"},{"id":"typesafe/jev-router"}]}`))
		case "/api/v1/systemone":
			var q struct{ Model string }
			json.NewDecoder(r.Body).Decode(&q)
			mu.Lock()
			asked = append(asked, q.Model)
			mu.Unlock()
			if q.Model == "nobody/none" {
				http.Error(w, `{"error":{"message":"No such model"}}`, 404)
				return
			}
			w.Write([]byte(`{"answers":{"ok":{"answer":true}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	p := Provider{ID: "openrouter-s1", Name: "OpenRouter", Key: "k", Decide: up.URL + "/api/v1",
		ModelsURL: up.URL + "/api/v1/models?limit=500&output_modalities=decisions", Models: []string{"respan/span-01"}}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	if !slices.Equal(ids, []string{"perplexity/pplx-decider-v1-27b", "liquid/d1", "~typesafe/jev-latest", "typesafe/jev-1.13"}) {
		t.Fatalf("decision models: %v", ids)
	}
	if why := p.ModelTest(); why != "" {
		t.Fatalf("models not testable: %s", why)
	}
	r := p.TestModels(context.Background(), []string{"respan/span-01", "nobody/none"})
	if !r[0].OK || r[0].Protocol != "decide" || r[0].Model != "respan/span-01" || r[1].OK || r[1].Error == "" {
		t.Fatalf("model tests: %+v", r)
	}
	if !slices.Equal(asked, []string{"respan/span-01", "nobody/none"}) && !slices.Equal(asked, []string{"nobody/none", "respan/span-01"}) {
		t.Fatalf("asked: %v", asked)
	}
}
