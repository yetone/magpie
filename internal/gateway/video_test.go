package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// grokMedia is the backend a Grok subscription's images and videos are
// asked at: what each path was sent is kept, a video takes two asks to be
// made, and a request id says how it ends.
type grokMedia struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
	sent  map[string][]string
	asked map[string]int
}

func newGrokMedia(t *testing.T) *grokMedia {
	t.Helper()
	g := &grokMedia{sent: map[string][]string{}, asked: map[string]int{}}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		g.mu.Lock()
		defer g.mu.Unlock()
		g.paths = append(g.paths, r.Method+" "+r.URL.Path)
		g.sent[r.URL.Path] = append(g.sent[r.URL.Path], string(b))
		w.Header().Set("Content-Type", "application/json")
		say := func(code int, body string) { w.WriteHeader(code); io.WriteString(w, body) }
		png := base64.StdEncoding.EncodeToString(pngBytes)
		switch {
		case r.URL.Path == "/v1/images/generations" && strings.Contains(string(b), "RATELIMITED"):
			say(429, `{"code":"resource-exhausted","error":"You have reached your image generation limit for this period."}`)
		case r.URL.Path == "/v1/images/generations" && strings.Contains(string(b), "REFUSED"):
			say(400, `{"code":"invalid-argument","error":"Generated image rejected by content moderation."}`)
		case r.URL.Path == "/v1/images/generations" && strings.Contains(string(b), "BROKEN"):
			say(500, `upstream exploded`)
		case r.URL.Path == "/v1/images/generations" && strings.Contains(string(b), `"n":2`):
			say(200, `{"data":[{"b64_json":"`+png+`"},{"b64_json":"`+png+`"}]}`)
		case r.URL.Path == "/v1/images/generations" || r.URL.Path == "/v1/images/edits":
			say(200, `{"data":[{"b64_json":"`+png+`"}]}`)
		case r.URL.Path == "/v1/videos/generations" && strings.Contains(string(b), "TOOLONG"):
			say(400, `{"code":"invalid-argument","error":"Duration must be between 1 and 15 seconds"}`)
		case r.URL.Path == "/v1/videos/generations" && strings.Contains(string(b), "FAILING"):
			say(200, `{"request_id":"req-fail"}`)
		case r.URL.Path == "/v1/videos/generations" && strings.Contains(string(b), "EXPIRING"):
			say(200, `{"request_id":"req-expired"}`)
		case r.URL.Path == "/v1/videos/generations" && strings.Contains(string(b), "NOID"):
			say(200, `{}`)
		case r.URL.Path == "/v1/videos/generations":
			say(200, `{"request_id":"req-1"}`)
		case r.URL.Path == "/v1/videos/req-1":
			g.asked["req-1"]++
			switch g.asked["req-1"] {
			case 1:
				say(202, `{"status":"pending","progress":0}`)
			case 2:
				say(202, `{"status":"pending","progress":40}`)
			default:
				say(200, `{"status":"done","progress":100,"model":"grok-imagine-video","video":{"url":"`+g.URL+`/bucket/req-1.mp4","duration":6,"respect_moderation":true},"usage":{"cost_in_usd_ticks":3000000000}}`)
			}
		case r.URL.Path == "/v1/videos/req-fail":
			say(200, `{"status":"failed","error":{"code":"invalid_argument","message":"image_url must either be a base64-encoded image or a URL."}}`)
		case r.URL.Path == "/v1/videos/req-expired":
			say(200, `{"status":"expired"}`)
		case r.URL.Path == "/bucket/req-1.mp4":
			w.Header().Set("Content-Type", "binary/octet-stream")
			io.WriteString(w, "fake-mp4-bytes")
		default:
			say(404, `{"code":"not-found","error":"no such path `+r.URL.Path+`"}`)
		}
	}))
	t.Cleanup(g.Close)
	was := provider.GrokBase
	provider.GrokBase = g.URL + "/v1"
	t.Cleanup(func() { provider.GrokBase = was })
	return g
}

func (g *grokMedia) body(path string, i int) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if i >= len(g.sent[path]) {
		return ""
	}
	return g.sent[path][i]
}

func request(t *testing.T, s *Server, method, path, ct, body string) (int, map[string]any, []byte, http.Header) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	s.Handler().ServeHTTP(rec, req)
	var obj map[string]any
	json.Unmarshal(rec.Body.Bytes(), &obj)
	return rec.Code, obj, rec.Body.Bytes(), rec.Result().Header
}

func errorOf(obj map[string]any) string {
	if e, ok := obj["error"].(map[string]any); ok {
		m, _ := e["message"].(string)
		return m
	}
	return ""
}

func TestVideomakersAreAGrokSubscriptions(t *testing.T) {
	grokSignedIn(t)
	newGrokMedia(t)
	p, err := provider.Find("grok")
	if err != nil {
		t.Fatal(err)
	}
	ms := Videomakers(*p)
	if len(ms) != 2 || ms[0].ID != "grok-imagine-video" || ms[1].ID != "grok-imagine-video-1.5" || ms[0].Provider != "grok" {
		t.Fatalf("videomakers %v", ms)
	}
	if m := AutoVideomaker(); m != "grok/grok-imagine-video" {
		t.Fatalf("AutoVideomaker = %q", m)
	}
	if ms := Videomakers(provider.Provider{ID: "x", Name: "X", Chat: "http://x/v1"}); len(ms) != 0 {
		t.Fatalf("an API key vendor makes videos: %v", ms)
	}
}

func TestVideoIDsRoundTrip(t *testing.T) {
	id := videoID(provider.Provider{ID: "grok"}, "0ab-12", time.Unix(1790000000, 0))
	if id != "video_grok.0ab-12.1790000000" {
		t.Fatalf("id %q", id)
	}
	if pid, vid, at, ok := parseVideoID(id); !ok || pid != "grok" || vid != "0ab-12" || at.Unix() != 1790000000 {
		t.Fatalf("parsed %q %q %v %v", pid, vid, at, ok)
	}
	for _, bad := range []string{"", "video_", "video_grok", "video_grok.x", "video_grok.x.y", "video_.x.1", "video_grok..1", "grok.x.1", "video_grok.x.1.2"} {
		if _, _, _, ok := parseVideoID(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestVideoResolutionAndAspect(t *testing.T) {
	for size, want := range map[string]string{"1280x720": "720p", "720x1280": "720p", "1920x1080": "1080p", "854x480": "480p", "640x640": "480p", "1024x1024": "720p", "16:9": "", "": "", "junk": "", "0x5": ""} {
		if got := videoResolution(size); got != want {
			t.Errorf("videoResolution(%q) = %q, want %q", size, got, want)
		}
	}
	for size, want := range map[string]string{"1280x720": "16:9", "720x1280": "9:16", "1024x1024": "1:1", "1600x1200": "4:3", "9:16": "9:16"} {
		if got := aspectAmong(size, grokVideoAspects); got != want {
			t.Errorf("aspect(%q) = %q, want %q", size, got, want)
		}
	}
}

func TestVideoIsStartedPolledAndFetched(t *testing.T) {
	grokSignedIn(t)
	up := newGrokMedia(t)
	s := New()
	code, obj, raw, _ := request(t, s, "POST", "/v1/videos", "application/json",
		`{"model":"grok/grok-imagine-video","prompt":"a kite sways","seconds":"6","size":"1280x720"}`)
	if code != 200 || obj["object"] != "video" || obj["status"] != "queued" || obj["model"] != "grok/grok-imagine-video" || obj["seconds"] != "6" || obj["prompt"] != "a kite sways" {
		t.Fatalf("%d %s", code, raw)
	}
	id, _ := obj["id"].(string)
	if pid, vid, _, ok := parseVideoID(id); !ok || pid != "grok" || vid != "req-1" {
		t.Fatalf("id %q", id)
	}
	var sent map[string]any
	json.Unmarshal([]byte(up.body("/v1/videos/generations", 0)), &sent)
	if sent["model"] != "grok-imagine-video" || sent["duration"] != float64(6) || sent["aspect_ratio"] != "16:9" || sent["resolution"] != "720p" || sent["prompt"] != "a kite sways" {
		t.Fatalf("asked %v", sent)
	}
	if _, ok := sent["image"]; ok {
		t.Fatalf("an image went along: %v", sent)
	}
	// pending, then pending with progress, then done
	for i, want := range []struct {
		status   string
		progress float64
	}{{"queued", 0}, {"in_progress", 40}, {"completed", 100}} {
		code, obj, raw, _ = request(t, s, "GET", "/v1/videos/"+id, "", "")
		if code != 200 || obj["status"] != want.status || obj["progress"] != want.progress || obj["id"] != id {
			t.Fatalf("poll %d: %d %s", i, code, raw)
		}
	}
	if obj["seconds"] != "6" || obj["model"] != "grok/grok-imagine-video" {
		t.Fatalf("done: %v", obj)
	}
	code, _, raw, head := request(t, s, "GET", "/v1/videos/"+id+"/content", "", "")
	if code != 200 || string(raw) != "fake-mp4-bytes" || head.Get("Content-Type") != "video/mp4" || head.Get("Content-Length") != "14" {
		t.Fatalf("content %d %q %v", code, raw, head)
	}
}

func TestVideoContentWaitsForTheVideo(t *testing.T) {
	grokSignedIn(t)
	newGrokMedia(t)
	s := New()
	_, obj, _, _ := request(t, s, "POST", "/v1/videos", "application/json", `{"prompt":"a kite"}`)
	code, _, raw, _ := request(t, s, "GET", "/v1/videos/"+obj["id"].(string)+"/content", "", "")
	if code != 409 || !strings.Contains(string(raw), "isn't ready") || !strings.Contains(string(raw), "queued") {
		t.Fatalf("%d %s", code, raw)
	}
}

func TestVideoThatFailsOrExpiresSaysWhy(t *testing.T) {
	grokSignedIn(t)
	newGrokMedia(t)
	s := New()
	for prompt, want := range map[string][2]string{
		"FAILING":  {"invalid_argument", "image_url must either be a base64-encoded image or a URL."},
		"EXPIRING": {"expired", "no longer keeps"},
	} {
		_, obj, _, _ := request(t, s, "POST", "/v1/videos", "application/json", `{"prompt":"`+prompt+`"}`)
		id := obj["id"].(string)
		code, obj, raw, _ := request(t, s, "GET", "/v1/videos/"+id, "", "")
		e, _ := obj["error"].(map[string]any)
		if code != 200 || obj["status"] != "failed" || e["code"] != want[0] || !strings.Contains(e["message"].(string), want[1]) {
			t.Fatalf("%s: %d %s", prompt, code, raw)
		}
		if code, _, raw, _ = request(t, s, "GET", "/v1/videos/"+id+"/content", "", ""); code != 409 || !strings.Contains(string(raw), "failed") {
			t.Fatalf("%s content: %d %s", prompt, code, raw)
		}
	}
}

func TestVideoStartsFromAnImage(t *testing.T) {
	grokSignedIn(t)
	up := newGrokMedia(t)
	s := New()
	img := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	code, _, raw, _ := request(t, s, "POST", "/v1/videos", "application/json",
		`{"prompt":"it moves","input_reference":{"image_url":"`+img+`"},"reference_images":["https://example.com/a.png","`+img+`"]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	var sent struct {
		Image map[string]string   `json:"image"`
		Refs  []map[string]string `json:"reference_images"`
	}
	json.Unmarshal([]byte(up.body("/v1/videos/generations", 0)), &sent)
	if sent.Image["url"] != img || len(sent.Refs) != 2 || sent.Refs[0]["url"] != "https://example.com/a.png" || sent.Refs[1]["url"] != img {
		t.Fatalf("asked %s", up.body("/v1/videos/generations", 0))
	}
	// OpenAI's own shape: the image is a file in the multipart form
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("model", "grok/grok-imagine-video-1.5")
	mw.WriteField("prompt", "it moves")
	mw.WriteField("seconds", "8")
	part, _ := mw.CreateFormFile("input_reference", "first.png")
	part.Write(pngBytes)
	mw.Close()
	code, obj, raw, _ := request(t, s, "POST", "/v1/videos", mw.FormDataContentType(), buf.String())
	if code != 200 || obj["model"] != "grok/grok-imagine-video-1.5" {
		t.Fatalf("multipart %d %s", code, raw)
	}
	last := up.body("/v1/videos/generations", 1)
	if !strings.Contains(last, `"model":"grok-imagine-video-1.5"`) || !strings.Contains(last, `"duration":8`) || !strings.Contains(last, `"image":{"url":"data:`) {
		t.Fatalf("multipart asked %s", last)
	}
}

func TestVideoRequestsAreTurnedAway(t *testing.T) {
	grokSignedIn(t)
	newGrokMedia(t)
	s := New()
	for _, c := range []struct {
		name, method, path, body string
		code                     int
		want                     string
	}{
		{"no prompt", "POST", "/v1/videos", `{"model":"grok/grok-imagine-video"}`, 400, "say what to film"},
		{"not JSON", "POST", "/v1/videos", `nope`, 400, "isn't JSON"},
		{"seconds not a number", "POST", "/v1/videos", `{"prompt":"x","seconds":"a few"}`, 400, "whole number"},
		{"a chat model", "POST", "/v1/videos", `{"prompt":"x","model":"grok/grok-4.7"}`, 400, "can't make videos"},
		{"no such model", "POST", "/v1/videos", `{"prompt":"x","model":"nobody/nothing"}`, 404, "knows no model"},
		{"the vendor's limit is said", "POST", "/v1/videos", `{"prompt":"TOOLONG","seconds":"99"}`, 400, "Duration must be between 1 and 15 seconds"},
		{"the vendor sent no id", "POST", "/v1/videos", `{"prompt":"NOID"}`, 502, "no request_id"},
		{"an id that isn't one", "GET", "/v1/videos/nonsense", ``, 404, "isn't the id of a video"},
		{"a provider that makes none", "GET", "/v1/videos/video_nobody.x.1", ``, 404, "no provider"},
		{"content of an id that isn't one", "GET", "/v1/videos/nonsense/content", ``, 404, "isn't the id of a video"},
	} {
		code, obj, raw, _ := request(t, s, c.method, c.path, "application/json", c.body)
		if code != c.code || !strings.Contains(errorOf(obj), c.want) {
			t.Errorf("%s: %d %s", c.name, code, raw)
		}
	}
}

func TestVideoNeedsAMaker(t *testing.T) {
	codexSignedIn(t) // a ChatGPT account makes no videos, and there is no Grok one
	s := New()
	code, obj, raw, _ := request(t, s, "POST", "/v1/videos", "application/json", `{"prompt":"a kite"}`)
	if code != 400 || !strings.Contains(errorOf(obj), "no model to make videos with") {
		t.Fatalf("%d %s", code, raw)
	}
}
