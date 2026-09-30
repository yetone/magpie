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
	"github.com/yetone/magpie/internal/settings"
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
	if id != "video_grok.0ab-12.1790000000.0" {
		t.Fatalf("id %q", id)
	}
	if pid, vid, at, by, ok := parseVideoID(id); !ok || pid != "grok" || vid != "0ab-12" || at.Unix() != 1790000000 || by != "0" {
		t.Fatalf("parsed %q %q %v %q %v", pid, vid, at, by, ok)
	}
	for _, bad := range []string{"", "video_", "video_grok", "video_grok.x", "video_grok.x.y", "video_grok.x.1", "video_.x.1.0", "video_grok..1.0", "grok.x.1.0", "video_grok.x.1.2.3",
		"video_grok.x?a=1.1.0", "video_grok.x/y.1.0", "video_grok.x y.1.0", "video_grok.%2e%2e.1.0", "video_grok.x#.1.0", "video_gr/ok.x.1.0"} {
		if _, _, _, _, ok := parseVideoID(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestVideoResolutionAndAspect(t *testing.T) {
	for size, want := range map[string]string{"1280x720": "720p", "720x1280": "720p", "1920x1080": "1080p", "854x480": "480p", "640x640": "480p", "1024x1024": "720p", "16:9": "", "": "", "junk": "", "0x5": ""} {
		if got := videoResolution("grok-imagine-video-1.5", size); got != want {
			t.Errorf("videoResolution(1.5, %q) = %q, want %q", size, got, want)
		}
	}
	// grok-imagine-video stops at 720p: the vendor turns 1080p away for it
	for size, want := range map[string]string{"1920x1080": "720p", "1080x1920": "720p", "1280x720": "720p", "854x480": "480p", "16:9": ""} {
		if got := videoResolution("grok-imagine-video", size); got != want {
			t.Errorf("videoResolution(base, %q) = %q, want %q", size, got, want)
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
	if pid, vid, _, by, ok := parseVideoID(id); !ok || pid != "grok" || vid != "req-1" || by == "0" || by != signer(mustFind(t, "grok")) {
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
	// the vendor doesn't say when it finished, so nothing that changes from one poll to the next is made up
	if _, ok := obj["completed_at"]; ok {
		t.Fatalf("completed_at %v", obj["completed_at"])
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
		{"seconds below one", "POST", "/v1/videos", `{"prompt":"x","seconds":0}`, 400, "at least 1"},
		{"a chat model", "POST", "/v1/videos", `{"prompt":"x","model":"grok/grok-4.7"}`, 400, "can't make videos"},
		{"no such model", "POST", "/v1/videos", `{"prompt":"x","model":"nobody/nothing"}`, 404, "knows no model"},
		{"the vendor's limit is said", "POST", "/v1/videos", `{"prompt":"TOOLONG","seconds":"99"}`, 400, "Duration must be between 1 and 15 seconds"},
		{"the vendor sent no id", "POST", "/v1/videos", `{"prompt":"NOID"}`, 502, "no request_id"},
		{"an id that isn't one", "GET", "/v1/videos/nonsense", ``, 404, "isn't the id of a video"},
		{"a provider that makes none", "GET", "/v1/videos/video_nobody.x.1.0", ``, 404, "no provider"},
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

func mustFind(t *testing.T, id string) provider.Provider {
	t.Helper()
	p, err := provider.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return *p
}

// An id is put in the vendor's URL with the account's token: what would
// change the path is turned away before anything is sent.
func TestVideoIDCannotChangeTheUpstreamPath(t *testing.T) {
	grokSignedIn(t)
	up := newGrokMedia(t)
	s := New()
	by := signer(mustFind(t, "grok"))
	for _, vendor := range []string{"x%3Fa=1", "x%2Fy", "..%2F..%2Fother", "x%23y", "x%20y", "%2e%2e"} {
		for _, suffix := range []string{"", "/content"} {
			code, obj, raw, _ := request(t, s, "GET", "/v1/videos/video_grok."+vendor+".1."+by+suffix, "", "")
			if code != 404 || !strings.Contains(errorOf(obj), "isn't the id of a video") {
				t.Errorf("%s%s: %d %s", vendor, suffix, code, raw)
			}
		}
	}
	if len(up.paths) != 0 {
		t.Fatalf("the vendor was asked: %v", up.paths)
	}
}

// A video is told from those of the account signed in before a switch.
func TestVideoOfAnotherAccountSaysSo(t *testing.T) {
	grokSignedIn(t)
	up := newGrokMedia(t)
	s := New()
	other := mustFind(t, "grok")
	acct := *other.Account
	acct.User = "someone-else@x.ai"
	other.Account = &acct
	id := videoID(other, "req-1", time.Now())
	for _, suffix := range []string{"", "/content"} {
		code, obj, raw, _ := request(t, s, "GET", "/v1/videos/"+id+suffix, "", "")
		if code != 404 || !strings.Contains(errorOf(obj), "another") || !strings.Contains(errorOf(obj), "account") {
			t.Errorf("%q: %d %s", suffix, code, raw)
		}
	}
	if len(up.paths) != 0 {
		t.Fatalf("the vendor was asked: %v", up.paths)
	}
}

// Turning image generation off in the Settings turns the video a request
// that names no model gets off with it; one that names its model is its own.
func TestVideoFollowsImageGenOff(t *testing.T) {
	grokSignedIn(t)
	up := newGrokMedia(t)
	s := New()
	st := settings.Load()
	st.ImageGen = "off"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	if m, ok := videomaker(); ok || m != "" {
		t.Fatalf("videomaker = %q %v", m, ok)
	}
	code, obj, raw, _ := request(t, s, "POST", "/v1/videos", "application/json", `{"prompt":"a kite"}`)
	if code != 400 || !strings.Contains(errorOf(obj), "no model to make videos with") {
		t.Fatalf("%d %s", code, raw)
	}
	if len(up.paths) != 0 {
		t.Fatalf("the vendor was asked: %v", up.paths)
	}
	code, _, raw, _ = request(t, s, "POST", "/v1/videos", "application/json", `{"prompt":"a kite","model":"grok/grok-imagine-video"}`)
	if code != 200 {
		t.Fatalf("a named model: %d %s", code, raw)
	}
	// on again, or set to an image model of another vendor, video keeps its own default
	for _, v := range []string{"", "art/painter-image-preview"} {
		st.ImageGen = v
		settings.Save(st)
		if m, ok := videomaker(); !ok || m != "grok/grok-imagine-video" {
			t.Fatalf("ImageGen %q: videomaker = %q %v", v, m, ok)
		}
	}
}

// The vendor names its models without a provider; so may a request.
func TestVideoModelByItsBareName(t *testing.T) {
	grokSignedIn(t)
	up := newGrokMedia(t)
	s := New()
	// in this order: the second request is the one the upstream's body is checked for
	for _, name := range []string{"grok-imagine-video", "grok-imagine-video-1.5"} {
		code, obj, raw, _ := request(t, s, "POST", "/v1/videos", "application/json", `{"prompt":"a kite","model":"`+name+`"}`)
		if code != 200 || obj["model"] != "grok/"+name {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
	if !strings.Contains(up.body("/v1/videos/generations", 1), `"model":"grok-imagine-video-1.5"`) {
		t.Fatalf("asked %s", up.body("/v1/videos/generations", 1))
	}
	for name, c := range map[string]struct {
		code int
		want string
	}{"nothing": {404, "knows no model"}, "grok-4.7": {400, "can't make videos"}} {
		code, obj, raw, _ := request(t, s, "POST", "/v1/videos", "application/json", `{"prompt":"a kite","model":"`+name+`"}`)
		if code != c.code || !strings.Contains(errorOf(obj), c.want) {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
}

// seconds is a whole number however JSON writes it; 1080p goes only where
// the model makes it.
func TestVideoSecondsAndResolutionAsAsked(t *testing.T) {
	grokSignedIn(t)
	up := newGrokMedia(t)
	s := New()
	n := 0
	ask := func(body string) map[string]any {
		t.Helper()
		code, _, raw, _ := request(t, s, "POST", "/v1/videos", "application/json", body)
		if code != 200 {
			t.Fatalf("%s: %d %s", body, code, raw)
		}
		var sent map[string]any
		json.Unmarshal([]byte(up.body("/v1/videos/generations", n)), &sent)
		n++
		return sent
	}
	for _, sec := range []string{`6`, `6.0`, `"6"`, `"6.0"`, `6e0`} {
		if sent := ask(`{"prompt":"a kite","seconds":` + sec + `}`); sent["duration"] != float64(6) {
			t.Errorf("seconds %s: asked %v", sec, sent)
		}
	}
	for _, sec := range []string{`6.5`, `"six"`, `1e99`, `"0x1p3"`, `-5`, `"-5"`, `0`, `"+6"`, `"6_0"`, `"inf"`, `"nan"`, `"1e3.5"`} {
		code, obj, raw, _ := request(t, s, "POST", "/v1/videos", "application/json", `{"prompt":"a kite","seconds":`+sec+`}`)
		if code != 400 || !strings.Contains(errorOf(obj), "whole number") {
			t.Errorf("seconds %s: %d %s", sec, code, raw)
		}
	}
	// the object says 6, as the video is, not the 6.0 that was written
	if _, obj, _, _ := request(t, s, "POST", "/v1/videos", "application/json", `{"prompt":"a kite","seconds":6.0}`); obj["seconds"] != "6" {
		t.Errorf("seconds echoed as %v", obj["seconds"])
	}
	n++
	if sent := ask(`{"prompt":"a kite","size":"1920x1080"}`); sent["resolution"] != "720p" || sent["aspect_ratio"] != "16:9" {
		t.Errorf("1080p on the base model: %v", sent)
	}
	if sent := ask(`{"prompt":"a kite","model":"grok/grok-imagine-video-1.5","size":"1920x1080"}`); sent["resolution"] != "1080p" {
		t.Errorf("1080p on 1.5: %v", sent)
	}
}
