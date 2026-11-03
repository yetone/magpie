package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
)

// Volcengine Ark's own /api/v3/models, rows of a pay-as-you-go key's real
// reply (2026-11-03): it marks no "kind", but says each model's domain and
// its output_modalities. A Seedance (domain VideoGeneration, output video)
// and a wan2-1 (no domain, output video) are video models, out of the chat
// list and in LiveVideomakers (axelburks on Discord: Ark's Seedance sat
// among the chat models, and the editor had no Video models to tick);
// Seedream draws, the one listed with no modalities too; a 3D model
// (output three_d) is no chat model; the text models stay.
func TestFetchArkMarksVideoModels(t *testing.T) {
	body, err := os.ReadFile("testdata/volcengine-ark-models-2026-11-03.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()
	ms, _, err := FetchAt(context.Background(), srv.URL+"/api/v3", "k", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	var films, draws, chat []string
	for _, m := range ms {
		switch {
		case m.Films:
			films = append(films, m.ID)
		case m.Draws:
			draws = append(draws, m.ID)
		default:
			chat = append(chat, m.ID)
		}
	}
	slices.Sort(films)
	slices.Sort(draws)
	slices.Sort(chat)
	if want := []string{"doubao-seedance-1-0-pro-250528", "doubao-seedance-1-0-pro-fast-251015", "doubao-seedance-2-0-260128", "wan2-1-14b-t2v-250225"}; !slices.Equal(films, want) {
		t.Errorf("video models %v, want %v", films, want)
	}
	if want := []string{"doubao-seedream-5-0-flash-260915", "doubao-seedream-5-0-pro-260628"}; !slices.Equal(draws, want) {
		t.Errorf("image models %v, want %v", draws, want)
	}
	if want := []string{"doubao-seed-2-1-pro-260915", "doubao-seed-translation-250915", "kimi-k3-1-pro-261024"}; !slices.Equal(chat, want) {
		t.Errorf("chat models %v, want %v", chat, want)
	}
}
