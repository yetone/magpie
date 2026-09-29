package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// AIHubMix's /models leaves out gpt-image-2, which its key draws with; its
// public catalog has it, and the list kept for the provider gets it.
func TestPublicCatalogsImageModelsJoinTheList(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/models" {
			if r.Header.Get("Authorization") != "" {
				t.Errorf("the public catalog was sent the key")
			}
			w.Write([]byte(`{"success":true,"data":[
				{"model_id":"gpt-image-2","model_name":"GPT Image 2","types":"image_generation","input_modalities":"text,image","output_modalities":"image","release_date":"2026-04-21","pricing":{"input":5,"output":30}},
				{"model_id":"gemini-2.5-flash-image","model_name":"Gemini","types":"image_generation","output_modalities":"image","release_date":null},
				{"model_id":"V_2","model_name":"Ideogram","types":"image_generation","output_modalities":"image"},
				{"model_id":"gpt-5","model_name":"GPT-5","types":"llm","output_modalities":"text"}]}`))
			return
		}
		w.Write([]byte(`{"data":[{"id":"gpt-5"},{"id":"gemini-2.5-flash-image"}]}`))
	}))
	defer server.Close()
	old := catalog.PublicCatalogs
	catalog.PublicCatalogs = map[string]string{"127.0.0.1": server.URL + "/api/v1/models"}
	t.Cleanup(func() { catalog.PublicCatalogs = old })

	p := Provider{ID: "hub", Chat: server.URL + "/v1", Key: "k"}
	chat, err := p.Fetch(context.Background())
	if err != nil || len(chat) != 1 || chat[0].ID != "gpt-5" {
		t.Fatalf("chat models %+v, %v", chat, err)
	}
	var ids []string
	for _, m := range catalog.LiveDrawers("hub") {
		ids = append(ids, m.ID)
		if m.ID == "gpt-image-2" && (m.Name != "GPT Image 2" || !m.Images || m.Price != nil) {
			t.Errorf("gpt-image-2 kept as %+v", m)
		}
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"gemini-2.5-flash-image", "gpt-image-2"}) {
		t.Fatalf("drawers %v", ids)
	}
}
