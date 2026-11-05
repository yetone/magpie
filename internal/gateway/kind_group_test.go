package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// kindVendor is an OpenAI-style vendor whose list has a chat model and one
// model of each other API, and that serves each of those APIs; busy
// answers every one of them 429, out of quota. It counts what it is asked.
type kindVendor struct {
	*httptest.Server
	busy  bool
	mu    sync.Mutex
	asked []string
}

func newKindVendor(t *testing.T, busy bool) *kindVendor {
	t.Helper()
	v := &kindVendor{busy: busy}
	v.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		v.mu.Lock()
		v.asked = append(v.asked, r.Method+" "+r.URL.Path)
		v.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" && r.URL.Path == "/v1/models" {
			io.WriteString(w, `{"object":"list","data":[`+
				`{"id":"chat-model","object":"model"},`+
				`{"id":"embed-1","object":"model"},`+
				`{"id":"bge-reranker-1","object":"model"},`+
				`{"id":"tts-1","object":"model"},`+
				`{"id":"whisper-1","object":"model"},`+
				`{"id":"omni-moderation-latest","object":"model"}]}`)
			return
		}
		if v.busy {
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"quota exceeded"}}`)
			return
		}
		switch r.URL.Path {
		case "/v1/embeddings":
			io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.5,0.25]}],"model":"embed-1","usage":{"prompt_tokens":1,"total_tokens":1}}`)
		case "/v1/rerank":
			io.WriteString(w, `{"results":[{"index":1,"relevance_score":0.75}]}`)
		case "/v1/audio/speech":
			w.Header().Set("Content-Type", "audio/mpeg")
			w.Write([]byte("ID3 kind speech"))
		case "/v1/audio/transcriptions":
			io.WriteString(w, `{"text":"heard by kind"}`)
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"message":"no route `+r.URL.Path+` `+string(b)+`"}}`)
		}
	}))
	t.Cleanup(v.Close)
	return v
}

func (v *kindVendor) log() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.asked...)
}

// #5339 (XYenon on Discord): a model of an API other than chat that two
// providers serve under one id is a group, found as a chat model's is,
// asked for by its id on its API's route or by the group's own id: the
// first provider out of quota passes the request to the next. The models
// come from the providers' fetched lists, which kept them apart from the
// chat models (an embedding model was dropped from the list before, so
// "embed-1" was a 404). None of it is offered to agents: /v1/models and
// the groups list have neither the models nor their groups. A group is of
// its own API: an embedding model's id on /v1/rerank is no reranker.
func TestKindGroupsServeEachAPIByRoute(t *testing.T) {
	fresh(t)
	busy, free := newKindVendor(t, true), newKindVendor(t, false)
	// both have their chat model picked, as most providers do
	saveFetched(t, provider.Provider{ID: "busy", Name: "Busy", Chat: busy.URL + "/v1", Key: "k", Models: []string{"chat-model"}})
	saveFetched(t, provider.Provider{ID: "free", Name: "Free", Chat: free.URL + "/v1", Key: "k", Models: []string{"chat-model"}})
	s := New()

	if code, raw := postRetrieval(t, s, "/v1/embeddings", `{"model":"embed-1","input":"hi"}`); code != 200 || !strings.Contains(raw, `[0.5,0.25]`) {
		t.Fatalf("embeddings by the model's id: %d %s", code, raw)
	}
	if code, raw := postRetrieval(t, s, "/v1/embeddings", `{"model":"group/auto-embedding:embed-1","input":"hi"}`); code != 200 || !strings.Contains(raw, `[0.5,0.25]`) {
		t.Fatalf("embeddings by the group's id: %d %s", code, raw)
	}
	if code, raw := postRetrieval(t, s, "/v1/rerank", `{"model":"bge-reranker-1","query":"q","documents":["a","b"]}`); code != 200 || !strings.Contains(raw, `"relevance_score":0.75`) {
		t.Fatalf("rerank: %d %s", code, raw)
	}
	if code, _, raw := postSpeech(t, s, "/v1/audio/speech", `{"model":"tts-1","input":"hello","voice":"alloy"}`); code != 200 || string(raw) != "ID3 kind speech" {
		t.Fatalf("speech: %d %.200s", code, raw)
	}
	body, ctype := audioFormBody(t, "model", "whisper-1")
	if code, _, raw := postAudio(t, s, "/v1/audio/transcriptions", ctype, body); code != 200 || !strings.Contains(raw, "heard by kind") {
		t.Fatalf("transcription: %d %s", code, raw)
	}
	want := "GET /v1/models,POST /v1/embeddings,POST /v1/embeddings,POST /v1/rerank,POST /v1/audio/speech,POST /v1/audio/transcriptions"
	if got := strings.Join(busy.log(), ","); got != want {
		t.Fatalf("the busy provider was asked %s, want %s", got, want)
	}

	// a group is of its own API's route alone
	for _, c := range []struct{ path, body string }{
		{"/v1/rerank", `{"model":"embed-1","query":"q","documents":["a"]}`},
		{"/v1/rerank", `{"model":"group/auto-embedding:embed-1","query":"q","documents":["a"]}`},
		{"/v1/embeddings", `{"model":"tts-1","input":"hi"}`},
	} {
		if code, raw := postRetrieval(t, s, c.path, c.body); code != 404 {
			t.Fatalf("%s %s: %d %s, want 404", c.path, c.body, code, raw)
		}
	}

	// none of it is offered as a model to talk to
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	for _, id := range []string{"embed-1", "bge-reranker-1", "tts-1", "whisper-1", "auto-embedding", "moderation"} {
		if strings.Contains(rec.Body.String(), id) {
			t.Fatalf("/v1/models lists %s: %s", id, rec.Body.String())
		}
	}
	for _, g := range provider.Groups() {
		if strings.Contains(g.ID, ":") || strings.Contains(g.ID, "embed") || strings.Contains(g.ID, "tts") {
			t.Fatalf("the groups list has %s", g.ID)
		}
	}
	for _, e := range provider.Catalog() {
		for _, id := range []string{"embed", "rerank", "tts", "whisper", "moderation"} {
			if strings.Contains(e.ID, id) || strings.Contains(e.Model, id) {
				t.Fatalf("agents are offered %s (%s)", e.ID, e.Model)
			}
		}
	}
}

// A video model two providers list under one id is a group by that id on
// /v1/videos: the first that can't make it passes it to the next. Before,
// the bare id went to the first provider listing it and ended there.
func TestVideoKindGroupByTheModelsID(t *testing.T) {
	fresh(t)
	a, b := newOpenAIVideos(t), newOpenAIVideos(t)
	a.lacks = "sk-a"
	saveFetched(t, provider.Provider{ID: "a", Name: "A", Chat: a.URL + "/v1", Key: "sk-a", Models: []string{"gpt-5.5"}})
	saveFetched(t, provider.Provider{ID: "b", Name: "B", Chat: b.URL + "/v1", Key: "sk-b", Models: []string{"gpt-5.5"}})
	code, obj, raw := asKey(t, New().Handler(), "", "POST", "/v1/videos", `{"model":"vid-1","prompt":"a cat on a fence"}`)
	if code != 200 || obj["model"] != "b/vid-1" {
		t.Fatalf("video: %d %s", code, raw)
	}
	if got := strings.Join(a.log(), ","); !strings.Contains(got, "POST /v1/videos sk-a") {
		t.Fatalf("the first provider was never asked: %v", a.log())
	}
}
