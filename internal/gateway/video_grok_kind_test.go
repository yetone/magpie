package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

func modelIDs(ms []catalog.Model) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

// A Grok channel set up with a base URL lists grok-imagine-video among
// the models to talk to, where right-click → Makes… → Video marks it, not
// under Image models, where nothing could (iceloon on X): its /models
// marks no kind, and the id's "imagine" read it as an image model. It is
// no video model on its name alone (#1399).
func TestGrokChannelVideoIsMarkable(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"id":"grok-4.3"},{"id":"grok-imagine-image"},{"id":"grok-imagine-video"},{"id":"grok-imagine-video-1.5"}]}`)
	}))
	t.Cleanup(up.Close)
	p := saveFetched(t, provider.Provider{ID: "relay", Name: "Relay", Chat: up.URL + "/v1", Key: "sk-relay"})
	if got := modelIDs(Drawers(p)); !slices.Equal(got, []string{"grok-imagine-image"}) {
		t.Errorf("drawers %v", got)
	}
	if got := modelIDs(Videomakers(p)); len(got) != 0 {
		t.Errorf("videomakers %v on the name alone", got)
	}
	if chat := modelIDs(p.Available()); !slices.Contains(chat, "grok-imagine-video") || !slices.Contains(chat, "grok-imagine-video-1.5") {
		t.Errorf("grok-imagine-video not where it can be marked: %v", chat)
	}
	if err := provider.SetModelKind("relay/grok-imagine-video", "video"); err != nil {
		t.Fatal(err)
	}
	p = mustFind(t, "relay")
	if got := modelIDs(Videomakers(p)); !slices.Equal(got, []string{"grok-imagine-video"}) {
		t.Errorf("videomakers %v after marking", got)
	}
	if got := modelIDs(Drawers(p)); !slices.Equal(got, []string{"grok-imagine-image"}) {
		t.Errorf("drawers %v after marking", got)
	}
}

// An xAI API key's grok-imagine-video is made at xAI's own videos API
// (POST /v1/videos/generations, then GET /v1/videos/<request_id>), which
// has no OpenAI-shaped /v1/videos: listed as a video model, it would have
// been asked there and turned away. Its list is xAI's own, so its
// grok-imagine-video is a video model without a mark.
func TestXAIKeyMakesGrokVideo(t *testing.T) {
	fresh(t)
	up := newGrokMedia(t)
	p := provider.Provider{ID: "xai", Name: "xAI", Chat: "http://api.x.ai/v1", Responses: "http://api.x.ai/v1", Key: "xai-key"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	// a list saved before the fix, which drew it
	if err := catalog.SaveLive("xai", "http://api.x.ai/v1", []catalog.Model{{ID: "grok-4.3"}, {ID: "grok-imagine-image", Draws: true}, {ID: "grok-imagine-video", Draws: true}}); err != nil {
		t.Fatal(err)
	}
	p = mustFind(t, "xai")
	if got := modelIDs(Videomakers(p)); !slices.Equal(got, []string{"grok-imagine-video"}) {
		t.Fatalf("videomakers %v", got)
	}
	if got := modelIDs(Drawers(p)); !slices.Equal(got, []string{"grok-imagine-image"}) {
		t.Fatalf("drawers %v", got)
	}
	s := New()
	addr := strings.TrimPrefix(up.URL, "http://")
	s.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}}
	code, obj, raw, _ := request(t, s, "POST", "/v1/videos", "application/json", `{"model":"xai/grok-imagine-video","prompt":"a kite sways","seconds":"6"}`)
	if code != 200 || obj["status"] != "queued" || obj["model"] != "xai/grok-imagine-video" {
		t.Fatalf("start: %d %s", code, raw)
	}
	var sent map[string]any
	json.Unmarshal([]byte(up.body("/v1/videos/generations", 0)), &sent)
	if sent["model"] != "grok-imagine-video" || sent["prompt"] != "a kite sways" {
		t.Fatalf("asked %v (paths %v)", sent, up.paths)
	}
	id, _ := obj["id"].(string)
	for i := 0; i < 3; i++ {
		code, obj, raw, _ = request(t, s, "GET", "/v1/videos/"+id, "", "")
		if code != 200 {
			t.Fatalf("poll %d: %d %s", i, code, raw)
		}
	}
	if obj["status"] != "completed" {
		t.Fatalf("done: %s", raw)
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if !slices.Contains(up.paths, "GET /v1/videos/req-1") || slices.Contains(up.paths, "POST /v1/videos") {
		t.Fatalf("paths %v", up.paths)
	}
}
