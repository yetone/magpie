package catalog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// A model whose id names videos is no image model, whatever else its id
// says: grok-imagine-video says "imagine", and a Grok channel listed it
// among its Image models, where nothing could mark it Video (iceloon on
// X). The ids are ones models.dev lists with a video output; cogvideox and
// the image models stay as they were.
func TestVideoNamedIsNoImageModel(t *testing.T) {
	for _, id := range []string{"grok-imagine-video", "grok-imagine-video-1.5", "spacexai/grok-imagine-video-1.5-lite",
		"wanx2.1-t2v-turbo", "wan2.5-i2v-preview", "alibaba/wan-v3.0-video", "bfl/flux-3-video", "Grok-Imagine-Video"} {
		if DrawsID(id) || ImagesAPI(id) {
			t.Errorf("%s: DrawsID %v ImagesAPI %v, a video model drawn as an image", id, DrawsID(id), ImagesAPI(id))
		}
	}
	for _, id := range []string{"grok-imagine-image", "grok-imagine-image-pro", "wanx2.1-t2i-turbo", "flux-2-pro", "cogview-4"} {
		if !DrawsID(id) || !ImagesAPI(id) {
			t.Errorf("%s: DrawsID %v ImagesAPI %v, want an image model", id, DrawsID(id), ImagesAPI(id))
		}
	}
	for _, id := range []string{"cogvideox", "videoseal", "glm-4.6v"} {
		if namesVideo(id) {
			t.Errorf("%s: namesVideo true", id)
		}
	}
}

func kindsOf(ms []Model) map[string]string {
	kind := map[string]string{}
	for _, m := range ms {
		switch {
		case m.Films && m.Draws:
			kind[m.ID] = "both"
		case m.Films:
			kind[m.ID] = "video"
		case m.Draws:
			kind[m.ID] = "image"
		default:
			kind[m.ID] = "chat"
		}
	}
	return kind
}

// A relay's /models that marks no kind lists Grok's video models where the
// user can mark them Video (#1399: magpie doesn't send a relay's model to
// a videos API on its name alone), never under Image models.
func TestFetchRelayGrokVideoIsNoDrawer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[
			{"id":"grok-imagine-video"},
			{"id":"grok-imagine-video-1.5"},
			{"id":"grok-imagine-image"},
			{"id":"grok-4.3"}]}`)
	}))
	defer srv.Close()
	ms, err := FetchURL(context.Background(), srv.URL+"/v1/models", "", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	kind := kindsOf(ms)
	want := map[string]string{"grok-imagine-video": "chat", "grok-imagine-video-1.5": "chat", "grok-imagine-image": "image", "grok-4.3": "chat"}
	for id, w := range want {
		if kind[id] != w {
			t.Errorf("%s: %q, want %q", id, kind[id], w)
		}
	}
}

// xAI's own list is the one whose grok-imagine-video is a video model:
// api.x.ai makes it at its videos API.
func TestXAIFilms(t *testing.T) {
	for _, c := range []struct {
		base, id string
		want     bool
	}{
		{"https://api.x.ai/v1", "grok-imagine-video", true},
		{"https://API.X.AI/v1/models", "grok-imagine-video-1.5", true},
		{"https://api.x.ai/v1", "grok-imagine-image", false},
		{"https://api.x.ai/v1", "grok-4.3", false},
		{"https://relay.example/v1", "grok-imagine-video", false},
		{"https://api.x.ai.evil.example/v1", "grok-imagine-video", false},
	} {
		if got := xaiFilms(c.base, c.id); got != c.want {
			t.Errorf("xaiFilms(%q, %q) = %v", c.base, c.id, got)
		}
	}
}

// A list saved before the fix kept grok-imagine-video as an image model;
// its first read gives it as xAI's video model, or a relay's model to talk
// to, without a fetch.
func TestSavedGrokVideoMended(t *testing.T) {
	for _, c := range []struct {
		base, want string
	}{{"https://api.x.ai/v1", "video"}, {"https://relay.example/v1", "chat"}} {
		p := "films-mend-" + c.want
		if err := SaveLive(p, c.base, []Model{
			{ID: "grok-4.3"},
			{ID: "grok-imagine-video", Draws: true},
			{ID: "grok-imagine-image", Draws: true},
		}); err != nil {
			t.Fatal(err)
		}
		ms, ok := LiveAll(p)
		os.Remove(LivePath(p))
		if !ok {
			t.Fatal("no list")
		}
		kind := kindsOf(ms)
		if kind["grok-imagine-video"] != c.want || kind["grok-imagine-image"] != "image" || kind["grok-4.3"] != "chat" {
			t.Errorf("%s: %v, want grok-imagine-video %s", c.base, kind, c.want)
		}
	}
}
