package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// Together AI's /v1/models is a bare JSON array (together-python's
// models.list reads the answer as a list), each row with a "type" that
// says what the model is for. The array is read, and the type puts each
// model where it serves: its embedding and rerank models, whose ids
// (togethercomputer/m2-bert-80M-32k-retrieval, Salesforce/Llama-Rank-V1)
// don't say so, on /v1/embeddings and /v1/rerank (@rfiacne on X), its
// image model with the image models, its voice and transcriber on theirs,
// its moderation model nowhere. Only the chat model is offered to chat.
func TestTogetherLiveList(t *testing.T) {
	body, err := os.ReadFile("testdata/together-models.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer k" {
			rw.WriteHeader(http.StatusUnauthorized)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.Write(body)
	}))
	defer srv.Close()
	old := http.DefaultClient.Transport
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	http.DefaultClient.Transport = rewrite{srv}

	ms, _, err := catalog.FetchAt(context.Background(), "https://api.together.xyz/v1", "k", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]catalog.Model{}
	for _, m := range ms {
		by[m.ID] = m
	}
	chat := catalog.Chat(ms)
	if len(chat) != 1 || chat[0].ID != "meta-llama/Llama-3.3-70B-Instruct-Turbo" || chat[0].Context != 131072 {
		t.Fatalf("chat: %+v", chat)
	}
	for id, serves := range map[string]string{
		"togethercomputer/m2-bert-80M-32k-retrieval": catalog.ServesEmbedding,
		"intfloat/multilingual-e5-large-instruct":    catalog.ServesEmbedding,
		"Salesforce/Llama-Rank-V1":                   catalog.ServesRerank,
		"cartesia/sonic-2":                           catalog.ServesSpeech,
		"openai/whisper-large-v3":                    catalog.ServesTranscription,
	} {
		if m, ok := by[id]; !ok || m.Serves != serves {
			t.Errorf("%s: %+v (want %s)", id, m, serves)
		}
	}
	if m := by["black-forest-labs/FLUX.1-schnell"]; !m.Draws || m.Serves != "" {
		t.Errorf("image: %+v", m)
	}
	if m, ok := by["meta-llama/Llama-Guard-4-12B"]; ok {
		t.Errorf("moderation kept: %+v", m)
	}
}
