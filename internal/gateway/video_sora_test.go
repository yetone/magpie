package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// soraVideos is an upstream serving OpenAI's Videos API under /v1 whose
// model list names sora-2 and sora-2-pro and marks no kind, as OpenAI's
// does: a video is made in one poll.
type soraVideos struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
}

func newSoraVideos(t *testing.T) *soraVideos {
	t.Helper()
	u := &soraVideos{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		defer u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/models":
			io.WriteString(w, `{"object":"list","data":[{"id":"gpt-5.5","object":"model"},{"id":"sora-2","object":"model"},{"id":"sora-2-pro","object":"model"}]}`)
		case r.Method == "POST" && r.URL.Path == "/v1/videos":
			u.bodies = append(u.bodies, string(b))
			io.WriteString(w, `{"id":"video_1","object":"video","created_at":1790000000,"status":"queued","progress":0,"model":"sora-2","seconds":"4","size":"1280x720"}`)
		case r.Method == "GET" && r.URL.Path == "/v1/videos/video_1":
			io.WriteString(w, `{"id":"video_1","object":"video","status":"completed","progress":100,"model":"sora-2"}`)
		case r.Method == "GET" && r.URL.Path == "/v1/videos/video_1/content":
			w.Header().Set("Content-Type", "video/mp4")
			io.WriteString(w, "\x00\x00\x00\x18ftypmp42fake-mp4")
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"message":"no route `+r.Method+" "+r.URL.Path+`","type":"invalid_request_error"}}`)
		}
	}))
	t.Cleanup(u.Close)
	return u
}

// An OpenAI API key's Sora models make videos through /v1/videos (#4954):
// a sora id is a video model its list needs no kind mark for, asked at the
// OpenAI-shaped videos API the gateway already speaks, and it is no chat
// model.
func TestOpenAIKeySoraMakesVideos(t *testing.T) {
	fresh(t)
	up := newSoraVideos(t)
	p := saveFetched(t, provider.Provider{ID: "studio", Name: "Studio", Chat: up.URL + "/v1", Key: "sk-studio"})
	ids := func(ms []catalog.Model) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.ID)
		}
		return out
	}
	if got := ids(Videomakers(p)); !slices.Equal(got, []string{"sora-2", "sora-2-pro"}) {
		t.Fatalf("videomakers %v", got)
	}
	var chat []string
	for _, m := range p.Available() {
		chat = append(chat, m.ID)
	}
	if slices.Contains(chat, "sora-2") || slices.Contains(chat, "sora-2-pro") {
		t.Fatalf("the sora models among the chat models: %v", chat)
	}
	h := New().Handler()
	code, obj, raw := asKey(t, h, "", "POST", "/v1/videos", `{"model":"studio/sora-2","prompt":"a cat on a fence","seconds":4,"size":"1280x720"}`)
	id, _ := obj["id"].(string)
	if code != 200 || obj["model"] != "studio/sora-2" || obj["status"] != "queued" {
		t.Fatalf("start: %d %s", code, raw)
	}
	var sent map[string]any
	json.Unmarshal([]byte(up.bodies[0]), &sent)
	if sent["model"] != "sora-2" || sent["prompt"] != "a cat on a fence" || sent["seconds"] != "4" || sent["size"] != "1280x720" {
		t.Fatalf("sent %v", sent)
	}
	if code, obj, raw = asKey(t, h, "", "GET", "/v1/videos/"+id, ""); code != 200 || obj["status"] != "completed" || obj["model"] != "studio/sora-2" {
		t.Fatalf("poll: %d %s", code, raw)
	}
	if got := ids(Videomakers(p)); strings.Join(got, ",") != "sora-2,sora-2-pro" {
		t.Fatalf("videomakers changed: %v", got)
	}
}
