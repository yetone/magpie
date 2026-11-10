package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// Vercel AI Gateway is a preset (Archie627 on X): Chat Completions and
// Responses at /v1, Anthropic's Messages at the root, models.dev's
// "vercel" catalog, and an entry imported at its base URL is the preset.
func TestVercelPreset(t *testing.T) {
	p, err := FromPreset("vercel")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Vercel AI Gateway" || p.Icon != "vercel" || p.Catalog != "vercel" || p.KeysURL == "" {
		t.Fatalf("%+v", p)
	}
	if p.Chat != "https://ai-gateway.vercel.sh/v1" || p.Responses != "https://ai-gateway.vercel.sh/v1" || p.Anthropic != "https://ai-gateway.vercel.sh" {
		t.Fatalf("endpoints: %q %q %q", p.Chat, p.Responses, p.Anthropic)
	}
	if pr := Preset("vercel"); pr.Kind != KindRelay || pr.NoKey {
		t.Fatalf("%+v", pr)
	}
	im, _ := imported("x", "k", endpoints{chat: "https://ai-gateway.vercel.sh/v1/"}, nil)
	if im.Preset != "vercel" || im.Icon != "vercel" {
		t.Fatalf("imported: %+v", im)
	}
	// the decision preset at /typesafe stays its own
	if Preset("vercel-jev").ID != "vercel-jev" {
		t.Fatal("vercel-jev gone")
	}
}

// Vercel's /v1/models, real rows of it (testdata/vercel-models.json):
// each row's "type" says what it is. Its decision and realtime models and
// its batch-only ids answer no chat; its voices, transcribers, embedders,
// rerankers, image and video models go where they serve. Only at Vercel's
// host: the same rows elsewhere are read as before.
func TestVercelLiveList(t *testing.T) {
	body, err := os.ReadFile("testdata/vercel-models.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer k" {
			rw.WriteHeader(http.StatusUnauthorized)
			return
		}
		rw.Write(body)
	}))
	defer srv.Close()
	old := http.DefaultClient.Transport
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	http.DefaultClient.Transport = rewrite{srv}

	ms, _, err := catalog.FetchAt(context.Background(), "https://ai-gateway.vercel.sh/v1", "k", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]catalog.Model{}
	for _, m := range ms {
		by[m.ID] = m
	}
	chat := catalog.Chat(ms)
	if len(chat) != 1 || chat[0].ID != "anthropic/claude-sonnet-4.6" || chat[0].Context != 1000000 {
		t.Fatalf("chat: %+v", chat)
	}
	for _, id := range []string{"anthropic/claude-sonnet-4.6-batch", "liquid/d1", "spacexai/grok-voice-3.0"} {
		if _, ok := by[id]; ok {
			t.Fatalf("%s kept: %+v", id, by[id])
		}
	}
	for id, serves := range map[string]string{
		"elevenlabs/eleven-v3":          catalog.ServesSpeech,
		"elevenlabs/scribe-v2":          catalog.ServesTranscription,
		"openai/text-embedding-3-small": catalog.ServesEmbedding,
		"cohere/rerank-v3.5":            catalog.ServesRerank,
	} {
		if m, ok := by[id]; !ok || m.Serves != serves {
			t.Fatalf("%s: %+v (want %s)", id, m, serves)
		}
	}
	if m := by["bfl/flux-2-flex"]; !m.Draws || m.Films {
		t.Fatalf("image: %+v", m)
	}
	if m := by["alibaba/wan-v2.6-i2v"]; !m.Films || m.Draws {
		t.Fatalf("video: %+v", m)
	}

	// another host's "type" is not Vercel's: the decision model stays
	http.DefaultClient.Transport = old
	ms, _, err = catalog.FetchAt(context.Background(), srv.URL+"/v1", "k", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	kept := false
	for _, m := range catalog.Chat(ms) {
		kept = kept || m.ID == "liquid/d1"
	}
	if !kept {
		t.Fatalf("elsewhere: %+v", ms)
	}
}
