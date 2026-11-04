package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
)

// Agnes AI's own /v1/models (apihub.agnes-ai.com, 2026-11-04), as new-api
// sends it: no kind, domain or modalities, only supported_endpoint_types
// "openai" on every row. Its two video models are video models by their
// id, out of the chat list (glorious_rabbit_30894 on Discord: Agnes's
// models in an agent's picker), its image models draw, and its chat
// models stay.
func TestFetchAgnesMarksVideoModelsByID(t *testing.T) {
	body, err := os.ReadFile("testdata/agnes-models-2026-11-04.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	defer srv.Close()
	ms, _, err := FetchAt(context.Background(), srv.URL+"/v1", "k", false, nil)
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
	if want := []string{"agnes-video-2.5", "agnes-video-2.5-flash"}; !slices.Equal(films, want) {
		t.Errorf("video models %v, want %v", films, want)
	}
	if want := []string{"agnes-image-2.0-flash", "agnes-image-2.1-flash", "agnes-image-2.5-flash"}; !slices.Equal(draws, want) {
		t.Errorf("image models %v, want %v", draws, want)
	}
	if want := []string{"agnes-2.5-flash", "agnes-2.5-pro-alpha", "agnes-2.5-pro-beta", "agnes-3.0-flash"}; !slices.Equal(chat, want) {
		t.Errorf("chat models %v, want %v", chat, want)
	}
}

// A list saved before FilmsID kept Agnes's video models among its chat
// models; they leave the chat list from the first read after the update,
// not only once the list is fetched again.
func TestSavedListVideoModelsLeaveChat(t *testing.T) {
	saved := []Model{
		{ID: "agnes-2.5-flash"},
		{ID: "agnes-video-2.5"},
		{ID: "agnes-video-2.5-flash"},
		// said to answer in text by its list: a chat model whatever its name
		{ID: "video-chat-1", OutputModalities: []string{"text"}},
	}
	if err := SaveLive("agnes-saved", "https://apihub.agnes-ai.com/v1", saved); err != nil {
		t.Fatal(err)
	}
	ms, _, _ := Live("agnes-saved")
	var chat []string
	for _, m := range ms {
		chat = append(chat, m.ID)
	}
	if want := []string{"agnes-2.5-flash", "video-chat-1"}; !slices.Equal(chat, want) {
		t.Errorf("chat models %v, want %v", chat, want)
	}
	var films []string
	for _, m := range LiveVideomakers("agnes-saved") {
		films = append(films, m.ID)
	}
	if want := []string{"agnes-video-2.5", "agnes-video-2.5-flash"}; !slices.Equal(films, want) {
		t.Errorf("video models %v, want %v", films, want)
	}
}

// FilmsID knows the video models of models.dev's lists by name, and none
// of the chat models whose names hold one of its words inside another.
func TestFilmsID(t *testing.T) {
	for _, id := range []string{"agnes-video-2.5", "openai/sora-2", "google/veo-3.1", "google/veo3.1-fast", "kling-v2-6", "klingai/kling-v3.0-t2v",
		"bytedance/seedance-2.0", "happyhorse-1.1-i2v", "alibaba/wan-v2.6-r2v", "wan2-2-t2v-a14b", "runwayml/runway-gen-4-turbo", "MiniMax-Hailuo-02", "vidu-q2"} {
		if !FilmsID(id) {
			t.Errorf("FilmsID(%q) = false", id)
		}
	}
	for _, id := range []string{"agnes-2.5-flash", "thinkingmachines/inkling", "inkling-small", "gpt-5.5", "claude-sonnet-5", "qwen3-vl-plus", "doubao-seed-2-1-pro-260915", "deepseek-v4", "glm-5.2", "grok-4-1-fast", "kimi-k3"} {
		if FilmsID(id) {
			t.Errorf("FilmsID(%q) = true", id)
		}
	}
}
