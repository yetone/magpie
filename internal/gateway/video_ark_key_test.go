package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// arkKeyVideos is a Volcengine Ark pay-as-you-go endpoint (/api/v3) that
// makes Seedance videos at its content-generation tasks, answering with
// the replies captured from Ark (volcengine_fixtures_test.go), and 404 at
// an OpenAI-shaped /videos, which Ark doesn't have.
type arkKeyVideos struct {
	*httptest.Server
	mu     sync.Mutex
	paths  []string
	bodies []string
	keys   []string
	polled int
}

func newArkKeyVideos(t *testing.T) *arkKeyVideos {
	t.Helper()
	a := &arkKeyVideos{}
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		a.mu.Lock()
		defer a.mu.Unlock()
		a.paths = append(a.paths, r.Method+" "+r.URL.Path)
		a.bodies = append(a.bodies, string(body))
		a.keys = append(a.keys, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/contents/generations/tasks":
			io.WriteString(w, volcengineVideoCreateResponse)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/contents/generations/tasks/cgt-20261008165954-4sc9n":
			a.polled++
			if a.polled == 1 {
				io.WriteString(w, volcengineVideoRunningResponse)
			} else {
				io.WriteString(w, strings.Replace(volcengineVideoSucceededResponse, "VIDEO_URL", a.URL+"/ark/video.mp4", 1))
			}
		case r.Method == http.MethodGet && r.URL.Path == "/ark/video.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			io.WriteString(w, "ark-video-bytes")
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"code":"NotFound","message":"The specified resource is not found."}}`)
		}
	}))
	t.Cleanup(a.Close)
	return a
}

// A Volcengine Ark key that isn't the Agent Plan's (pay-as-you-go, or an
// Ark endpoint added by hand) makes its Seedance models' videos at Ark's
// content-generation tasks, not at an OpenAI /videos Ark doesn't have
// (axelburks on Discord): a caller's OpenAI-shaped request is turned into
// Ark's, and its poll and download asked on the key that started it.
func TestArkKeyMakesSeedanceVideos(t *testing.T) {
	fresh(t)
	a := newArkKeyVideos(t)
	was := isArk
	isArk = func(p provider.Provider) bool { return p.ID == "ark" }
	t.Cleanup(func() { isArk = was })
	if err := provider.Save(provider.Provider{ID: "ark", Name: "Volcengine Ark", Chat: a.URL + "/api/v3", Key: "key",
		Models: []string{"doubao-seed-2.1-pro", "doubao-seedance-2-0-260128"}}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("ark")
	if err != nil {
		t.Fatal(err)
	}
	ms := Videomakers(*p)
	if !slices.ContainsFunc(ms, func(m catalog.Model) bool { return m.ID == "doubao-seedance-2-0-260128" }) ||
		slices.ContainsFunc(ms, func(m catalog.Model) bool { return m.ID == "doubao-seed-2.1-pro" }) {
		t.Fatalf("Ark key's video models: %+v", ms)
	}

	s := New()
	code, obj, raw, _ := request(t, s, http.MethodPost, "/v1/videos", "application/json",
		`{"model":"ark/doubao-seedance-2-0-260128","prompt":"一只喜鹊飞过","seconds":"5","size":"1280x720","input_reference":"data:image/png;base64,iVBORw0KGgo="}`)
	if code != 200 || obj["status"] != "queued" || obj["model"] != "ark/doubao-seedance-2-0-260128" {
		t.Fatalf("create: %d %s", code, raw)
	}
	id, _ := obj["id"].(string)
	if !strings.HasPrefix(id, "video_ark.cgt-20261008165954-4sc9n.") || !strings.HasSuffix(id, "."+keyMark(*p)) {
		t.Fatalf("id %q doesn't name Ark's task and the key", id)
	}
	code, obj, raw, _ = request(t, s, http.MethodGet, "/v1/videos/"+id, "", "")
	if code != 200 || obj["status"] != "in_progress" {
		t.Fatalf("running: %d %s", code, raw)
	}
	code, obj, raw, _ = request(t, s, http.MethodGet, "/v1/videos/"+id, "", "")
	if code != 200 || obj["status"] != "completed" || obj["seconds"] != "5" || obj["model"] != "ark/doubao-seedance-2.0-mini" {
		t.Fatalf("succeeded: %d %s", code, raw)
	}
	code, _, raw, head := request(t, s, http.MethodGet, "/v1/videos/"+id+"/content", "", "")
	if code != 200 || string(raw) != "ark-video-bytes" || head.Get("Content-Type") != "video/mp4" {
		t.Fatalf("content: %d %q", code, raw)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.paths[0] != "POST /api/v3/contents/generations/tasks" || slices.Contains(a.paths, "POST /api/v3/videos") {
		t.Fatalf("asked at %v", a.paths)
	}
	for i, k := range a.keys {
		if a.paths[i] != "GET /ark/video.mp4" && k != "Bearer key" {
			t.Fatalf("%s sent with %q", a.paths[i], k)
		}
	}
	var sent struct {
		Model   string           `json:"model"`
		Content []map[string]any `json:"content"`
		Dur     int              `json:"duration"`
		Ratio   string           `json:"ratio"`
		Res     string           `json:"resolution"`
	}
	if err := json.Unmarshal([]byte(a.bodies[0]), &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Model != "doubao-seedance-2-0-260128" || sent.Dur != 5 || sent.Ratio != "16:9" || sent.Res != "720p" || len(sent.Content) != 2 ||
		sent.Content[0]["text"] != "一只喜鹊飞过" || sent.Content[1]["role"] != "first_frame" {
		t.Fatalf("sent %s", a.bodies[0])
	}
}

// OpenAI's videos request, as its SDK sends it (a multipart form, the
// first frame a file in input_reference), reaches Seedance as Ark's: the
// last frame with role last_frame, and the fields OpenAI's API doesn't
// have (Ark's generate_audio, seed) passed on, typed as Ark takes them.
func TestArkVideoTakesLastFrameAndItsOwnFields(t *testing.T) {
	fresh(t)
	a := newArkKeyVideos(t)
	was := isArk
	isArk = func(p provider.Provider) bool { return p.ID == "ark" }
	t.Cleanup(func() { isArk = was })
	if err := provider.Save(provider.Provider{ID: "ark", Name: "Volcengine Ark", Chat: a.URL + "/api/v3", Key: "key",
		Models: []string{"doubao-seedance-1-0-pro-250528"}}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("model", "ark/doubao-seedance-1-0-pro-250528")
	mw.WriteField("prompt", "花开")
	mw.WriteField("seconds", "5")
	mw.WriteField("generate_audio", "false")
	mw.WriteField("seed", "42")
	mw.WriteField("ratio", "adaptive")
	for _, k := range []string{"input_reference", "last_frame"} {
		part, _ := mw.CreateFormFile(k, k+".png")
		part.Write(pngBytes)
	}
	mw.Close()
	code, _, raw, _ := request(t, New(), http.MethodPost, "/v1/videos", mw.FormDataContentType(), buf.String())
	if code != 200 {
		t.Fatalf("create: %d %s", code, raw)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var sent map[string]any
	if err := json.Unmarshal([]byte(a.bodies[0]), &sent); err != nil {
		t.Fatal(err)
	}
	content, _ := sent["content"].([]any)
	var roles []string
	for _, c := range content[1:] {
		roles = append(roles, c.(map[string]any)["role"].(string))
	}
	if !slices.Equal(roles, []string{"first_frame", "last_frame"}) || sent["generate_audio"] != false || sent["seed"] != float64(42) || sent["ratio"] != "adaptive" || sent["duration"] != float64(5) {
		t.Fatalf("sent %s", a.bodies[0])
	}
}
