package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// busyVendor answers every request 429, out of quota, and counts them.
func busyVendor(t *testing.T) (string, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var asked []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"quota exceeded"}}`)
	}))
	t.Cleanup(up.Close)
	return up.URL, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), asked...) }
}

// XYenon on Discord: a routing group of embedding models made by hand
// was a 404 when its providers had chat models picked, as most do. Picks
// are of chat models, and an embedding, reranker or speech model is in no
// chat list (it is left out when the list is fetched), so each member was
// dropped as unpicked (#3911's rule). The members are the group's, and
// the first one out of quota passes the request to the next — for
// embeddings, rerank, speech and transcriptions alike. The sibling of
// 1d8658abc, which kept a drawing group's image models.
func TestMediaGroupOfProvidersWithChatPicks(t *testing.T) {
	fresh(t)
	busy, busyAsked := busyVendor(t)
	l, sp, sc := &library{}, &speaker{more: make(chan struct{})}, &scribe{}
	lib, voice, ears := httptest.NewServer(l), httptest.NewServer(sp), httptest.NewServer(sc)
	t.Cleanup(lib.Close)
	t.Cleanup(voice.Close)
	t.Cleanup(ears.Close)
	// every provider has picks, and none of them is the media model
	for _, p := range []provider.Provider{
		{ID: "busy", Name: "Busy", Chat: busy + "/v1", Key: "k", Models: []string{"chat-model"}},
		{ID: "lib", Name: "Lib", Chat: lib.URL + "/v1", Key: "key", Models: []string{"chat-model"}},
		{ID: "voice", Name: "Voice", Chat: voice.URL + "/v1", Key: "key", Models: []string{"chat-model"}},
		{ID: "ears", Name: "Ears", Chat: ears.URL + "/v1", Key: "key", Models: []string{"chat-model"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range []provider.Group{
		{ID: "emb", Name: "emb", Members: []string{"busy/embed-1", "lib/embed-1"}, Routing: "order"},
		{ID: "rr", Name: "rr", Members: []string{"busy/rerank-1", "lib/rerank-1"}, Routing: "order"},
		{ID: "tts", Name: "tts", Members: []string{"busy/tts-1", "voice/gpt-4o-mini-tts"}, Routing: "order"},
		{ID: "stt", Name: "stt", Members: []string{"busy/whisper-1", "ears/whisper-1"}, Routing: "order"},
		// a chat model the user unticked stays out (#3911): a group of it alone has none
		{ID: "unticked", Name: "unticked", Members: []string{"lib/chat-model-old"}, Routing: "order"},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	// lib's list has a chat model the user left unticked
	if err := catalog.SaveLive("lib", lib.URL+"/v1", []catalog.Model{{ID: "chat-model"}, {ID: "chat-model-old"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	if code, raw := postRetrieval(t, s, "/v1/embeddings", `{"model":"group/emb","input":"hi"}`); code != 200 || !strings.Contains(raw, `"embedding":[0.1,-0.2]`) {
		t.Fatalf("embeddings: %d %s", code, raw)
	}
	if sent := l.got("/v1/embeddings"); len(sent) != 1 || !strings.Contains(sent[0], `"model":"embed-1"`) {
		t.Fatalf("lib was sent %v", sent)
	}
	if code, raw := postRetrieval(t, s, "/v1/rerank", `{"model":"group/rr","query":"q","documents":["a","b"]}`); code != 200 || !strings.Contains(raw, `"relevance_score":0.9`) {
		t.Fatalf("rerank: %d %s", code, raw)
	}
	if code, _, raw := postSpeech(t, s, "/v1/audio/speech", `{"model":"group/tts","input":"hello","voice":"alloy"}`); code != 200 || string(raw) != string(mp3) {
		t.Fatalf("speech: %d %.200s", code, raw)
	}
	body, ctype := audioFormBody(t, "model", "group/stt")
	if code, _, raw := postAudio(t, s, "/v1/audio/transcriptions", ctype, body); code != 200 || !strings.Contains(raw, "Hello from the magpie.") {
		t.Fatalf("transcription: %d %s", code, raw)
	}
	want := []string{"POST /v1/embeddings", "POST /v1/rerank", "POST /v1/audio/speech", "POST /v1/audio/transcriptions"}
	if got := busyAsked(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the busy member was asked %v, want %v", got, want)
	}
	if code, raw := postRetrieval(t, s, "/v1/embeddings", `{"model":"group/unticked","input":"hi"}`); code != 404 || len(l.got("/v1/embeddings")) != 1 {
		t.Fatalf("a group of a chat model the user unticked was asked: %d %s", code, raw)
	}
}

// A video group made by hand of two providers with chat models picked
// makes its video, the first member that serves no such model passing it
// on: its video models are no chat model, so no pick names them.
func TestVideoGroupOfProvidersWithChatPicks(t *testing.T) {
	fresh(t)
	a, b := newOpenAIVideos(t), newOpenAIVideos(t)
	a.lacks = "sk-a"
	saveFetched(t, provider.Provider{ID: "a", Name: "A", Chat: a.URL + "/v1", Key: "sk-a", Models: []string{"gpt-5.5"}})
	saveFetched(t, provider.Provider{ID: "b", Name: "B", Chat: b.URL + "/v1", Key: "sk-b", Models: []string{"gpt-5.5"}})
	if err := provider.SaveGroup(provider.Group{ID: "vid", Name: "vid", Members: []string{"a/vid-1", "b/vid-1"}, Routing: "order"}); err != nil {
		t.Fatal(err)
	}
	code, obj, raw := asKey(t, New().Handler(), "", "POST", "/v1/videos", `{"model":"group/vid","prompt":"a cat on a fence"}`)
	if code != 200 || obj["model"] != "b/vid-1" {
		t.Fatalf("video: %d %s", code, raw)
	}
	if got := strings.Join(a.log(), ","); !strings.Contains(got, "POST /v1/videos sk-a") {
		t.Fatalf("the first member was never asked: %v", a.log())
	}
}

// The video sibling of TestMixedGroupDrawsOnItsImageMemberOnly: a group of
// a chat model and a video model starts the video on its video member
// alone, never on the chat model, which comes first in the group.
func TestMixedGroupMakesVideoOnItsVideoMemberOnly(t *testing.T) {
	fresh(t)
	a := newOpenAIVideos(t)
	saveFetched(t, provider.Provider{ID: "a", Name: "A", Chat: a.URL + "/v1", Key: "sk-a", Models: []string{"gpt-5.5"}})
	if err := provider.SaveGroup(provider.Group{ID: "mixed", Name: "mixed", Members: []string{"a/gpt-5.5", "a/vid-1"}, Routing: "order"}); err != nil {
		t.Fatal(err)
	}
	code, obj, raw := asKey(t, New().Handler(), "", "POST", "/v1/videos", `{"model":"group/mixed","prompt":"a cat on a fence"}`)
	if code != 200 || obj["model"] != "a/vid-1" {
		t.Fatalf("video: %d %s", code, raw)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, b := range a.bodies {
		if strings.Contains(b, "gpt-5.5") {
			t.Fatalf("the chat member was asked for the video: %v", a.bodies)
		}
	}
}
