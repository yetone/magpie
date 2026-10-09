package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Remote media uses the remote's images and videos APIs, including edits,
// polls and content downloads, with local video ids surviving every poll.
func TestRemoteMagpieMedia(t *testing.T) {
	grokSignedIn(t)
	up := newGrokMedia(t)
	remote := httptest.NewServer(New().Handler())
	t.Cleanup(remote.Close)
	if err := provider.Save(provider.Provider{ID: "office", Preset: provider.RemoteMagpiePreset, Chat: remote.URL, Key: "remote-key"}); err != nil {
		t.Fatal(err)
	}
	p, _ := provider.Find("office")
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(Drawers(*p)) == 0 || len(Videomakers(*p)) == 0 {
		t.Fatal("remote image or video models weren't discovered")
	}
	s := New()
	code, _, raw, _ := request(t, s, "POST", "/v1/images/generations", "application/json", `{"model":"office/grok/grok-imagine-image","prompt":"a kite"}`)
	if code != 200 || !bytes.Contains(raw, []byte(`"b64_json"`)) {
		t.Fatalf("image: %d %s", code, raw)
	}
	var edit bytes.Buffer
	form := multipart.NewWriter(&edit)
	form.WriteField("model", "office/grok/grok-imagine-image")
	form.WriteField("prompt", "add a kite")
	part, err := form.CreateFormFile("image", "input.png")
	if err != nil {
		t.Fatal(err)
	}
	part.Write(pngBytes)
	form.Close()
	code, _, raw, _ = request(t, s, "POST", "/v1/images/edits", form.FormDataContentType(), edit.String())
	if code != 200 || !bytes.Contains(raw, []byte(`"b64_json"`)) {
		t.Fatalf("edit: %d %s", code, raw)
	}
	if b := up.body("/v1/images/edits", 0); !strings.Contains(b, "data:image/png;base64,") || !strings.Contains(b, "add a kite") {
		t.Fatalf("edit lost image or prompt: %s", b)
	}
	code, obj, raw, _ := request(t, s, "POST", "/v1/videos", "application/json", `{"model":"office/grok/grok-imagine-video","prompt":"a kite sways","seconds":"6","size":"1280x720"}`)
	id, _ := obj["id"].(string)
	if code != 200 || id == "" || obj["model"] != "office/grok/grok-imagine-video" || obj["status"] != "queued" {
		t.Fatalf("video: %d %s", code, raw)
	}
	for i, want := range []string{"queued", "in_progress", "completed"} {
		code, obj, raw, _ = request(t, s, "GET", "/v1/videos/"+id, "", "")
		if code != 200 || obj["id"] != id || obj["status"] != want || obj["model"] != "office/grok/grok-imagine-video" {
			t.Fatalf("poll %d: %d %s", i, code, raw)
		}
	}
	code, _, raw, headers := request(t, s, "GET", "/v1/videos/"+id+"/content", "", "")
	if code != 200 || string(raw) != "fake-mp4-bytes" || headers.Get("Content-Type") != "video/mp4" {
		t.Fatalf("content: %d %s %v", code, raw, headers)
	}
	var sent map[string]any
	json.Unmarshal([]byte(up.body("/v1/videos/generations", 0)), &sent)
	if sent["model"] != "grok-imagine-video" || sent["duration"] != float64(6) || sent["aspect_ratio"] != "16:9" || sent["resolution"] != "720p" {
		t.Errorf("video options: %v", sent)
	}
	// Caller metadata is accepted by the remote for every media path.
	arrived := make(chan http.Header, 1)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"b64_json":"aGVsbG8="}]}`)
	}))
	t.Cleanup(peer.Close)
	q := *p
	q.Chat = peer.URL
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("User-Agent", "pi/1.0")
	r.Header.Set(SessionHeader, "media-session")
	withCaller(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, err := s.send(r.Context(), q, peer.URL+"/images/generations", "application/json", []byte(`{"model":"m","prompt":"kite"}`), true)
		if err != nil {
			t.Fatal(err)
		}
	})).ServeHTTP(httptest.NewRecorder(), r)
	head := <-arrived
	if head.Get(AgentHeader) != "pi" || head.Get(SessionHeader) != "media-session" || !strings.HasPrefix(head.Get("User-Agent"), "magpie/") {
		t.Errorf("media caller: %v", head)
	}
}

func TestRemoteMagpieVideoContentCaller(t *testing.T) {
	fresh(t)
	arrived := make(chan caller, 1)
	remote := httptest.NewServer(withCaller(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- callerOf(r)
		w.Header().Set("Content-Type", "video/mp4")
		io.WriteString(w, "fake-mp4-bytes")
	})))
	t.Cleanup(remote.Close)
	if err := provider.Save(provider.Provider{ID: "office", Preset: provider.RemoteMagpiePreset, Chat: remote.URL, Key: "remote-key"}); err != nil {
		t.Fatal(err)
	}
	p, _ := provider.Find("office")
	req := httptest.NewRequest("GET", "/v1/videos/"+videoID(*p, "task", time.Now())+"/content", nil)
	req.Header.Set("User-Agent", "pi/1.0")
	req.Header.Set(SessionHeader, "video-session")
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.String() != "fake-mp4-bytes" {
		t.Fatalf("video content: %d %s", rec.Code, rec.Body.String())
	}
	// Content downloads have no usage record of their own. Check the
	// caller accepted by the remote's real receiving middleware instead.
	if c := <-arrived; c.agent != "pi" || c.via != hostName() || c.session != "video-session" {
		t.Errorf("remote video content lost caller: %+v", c)
	}
}
