package gateway

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// axelburks's list (Discord, 11-03): what Ark's own API takes and answers
// that magpie's OpenAI-shaped routes dropped, changed or turned away, each
// checked against Ark's docs (82379/1520757 create, 1521309 query,
// 1541523 images).

func arkVideoProvider(t *testing.T) (*Server, *arkKeyVideos) {
	t.Helper()
	fresh(t)
	a := newArkKeyVideos(t)
	was := isArk
	isArk = func(p provider.Provider) bool { return p.ID == "ark" }
	t.Cleanup(func() { isArk = was })
	if err := provider.Save(provider.Provider{ID: "ark", Name: "Volcengine Ark", Chat: a.URL + "/api/v3", Key: "key",
		Models: []string{"doubao-seedance-2-0-260128", "doubao-seedream-4-5-251128"}}); err != nil {
		t.Fatal(err)
	}
	return New(), a
}

func (a *arkKeyVideos) sentBody(t *testing.T, i int) map[string]any {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.bodies) <= i {
		t.Fatalf("sent %d requests: %v", len(a.bodies), a.paths)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(a.bodies[i]), &m); err != nil {
		t.Fatalf("%v: %s", err, a.bodies[i])
	}
	return m
}

// A caller's own content (a reference video and audio, a draft task) goes
// to Ark as it came, the prompt added as its text only when it has none.
func TestArkVideoKeepsCallersContent(t *testing.T) {
	s, a := arkVideoProvider(t)
	body := `{"model":"ark/doubao-seedance-2-0-260128","prompt":"跟着节奏跳舞","content":[
		{"type":"video_url","video_url":{"url":"https://example.com/ref.mp4"},"role":"reference_video"},
		{"type":"audio_url","audio_url":{"url":"https://example.com/beat.mp3"},"role":"reference_audio"}]}`
	if code, _, raw, _ := request(t, s, http.MethodPost, "/v1/videos", "application/json", body); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	content, _ := a.sentBody(t, 0)["content"].([]any)
	if len(content) != 3 || content[0].(map[string]any)["text"] != "跟着节奏跳舞" ||
		content[1].(map[string]any)["role"] != "reference_video" || content[2].(map[string]any)["role"] != "reference_audio" {
		t.Fatalf("sent %v", content)
	}

	// a draft task's content, with no prompt, goes alone
	body = `{"model":"ark/doubao-seedance-2-0-260128","content":[{"type":"draft_task","draft_task":{"id":"cgt-draft-1"}}]}`
	if code, _, raw, _ := request(t, s, http.MethodPost, "/v1/videos", "application/json", body); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	content, _ = a.sentBody(t, 1)["content"].([]any)
	if len(content) != 1 || content[0].(map[string]any)["type"] != "draft_task" {
		t.Fatalf("sent %v", content)
	}

	// a text of the caller's own isn't doubled by the prompt
	body = `{"model":"ark/doubao-seedance-2-0-260128","prompt":"花开","content":[{"type":"text","text":"花开 --rs 720p"}]}`
	if code, _, raw, _ := request(t, s, http.MethodPost, "/v1/videos", "application/json", body); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	content, _ = a.sentBody(t, 2)["content"].([]any)
	if len(content) != 1 || content[0].(map[string]any)["text"] != "花开 --rs 720p" {
		t.Fatalf("sent %v", content)
	}
}

// Image-to-video takes no prompt; an asset:// image and seconds -1 are
// Ark's own, and go as they came.
func TestArkVideoFromImageNeedsNoPrompt(t *testing.T) {
	s, a := arkVideoProvider(t)
	body := `{"model":"ark/doubao-seedance-2-0-260128","input_reference":"asset://asset-20261103-abc","seconds":"-1"}`
	if code, _, raw, _ := request(t, s, http.MethodPost, "/v1/videos", "application/json", body); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	sent := a.sentBody(t, 0)
	content, _ := sent["content"].([]any)
	if len(content) != 1 || sent["duration"] != float64(-1) {
		t.Fatalf("sent %v", sent)
	}
	img, _ := content[0].(map[string]any)["image_url"].(map[string]any)
	if img["url"] != "asset://asset-20261103-abc" || content[0].(map[string]any)["role"] != "first_frame" {
		t.Fatalf("sent %v", content)
	}

	// with neither a prompt nor an image there is nothing to film
	code, obj, _, _ := request(t, s, http.MethodPost, "/v1/videos", "application/json", `{"model":"ark/doubao-seedance-2-0-260128"}`)
	if code != 400 || !strings.Contains(errorOf(obj), "prompt") {
		t.Fatalf("%d %v", code, obj)
	}
}

// What only Ark takes is turned away before another vendor is asked.
func TestOnlyArkTakesItsOwnVideoAsks(t *testing.T) {
	fresh(t)
	up := newFakeMiniMax(t)
	minimaxProvider(t, "minimax", "minimax", up)
	s := New()
	for _, body := range []string{
		`{"model":"minimax/MiniMax-H3-Lite","prompt":"a magpie","seconds":"-1"}`,
		`{"model":"minimax/MiniMax-H3-Lite","input_reference":"data:image/png;base64,iVBORw0KGgo="}`,
		`{"model":"minimax/MiniMax-H3-Lite","prompt":"a magpie","input_reference":"asset://asset-1"}`,
	} {
		code, obj, raw, _ := request(t, s, http.MethodPost, "/v1/videos", "application/json", body)
		if code != 400 || errorOf(obj) == "" {
			t.Fatalf("%s: %d %s", body, code, raw)
		}
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.paths) != 0 {
		t.Fatalf("MiniMax was asked: %v", up.paths)
	}
}

// A size is Ark's nearest tier and ratio, and an explicit ratio or
// resolution (adaptive among them) isn't overridden by it.
func TestArkVideoSize(t *testing.T) {
	for _, c := range []struct{ size, res, ratio string }{
		{"3840x2160", "4k", "16:9"},
		{"1920x1080", "1080p", "16:9"},
		{"1280x720", "720p", "16:9"},
		{"854x480", "480p", "16:9"},
		{"1470x630", "720p", "21:9"},
		{"1080x1920", "1080p", "9:16"},
		{"720p", "720p", ""},
		{"2160p", "4k", ""},
		{"3:4", "", "3:4"},
	} {
		res, ratio := arkVideoSize(c.size)
		if res != c.res || ratio != c.ratio {
			t.Errorf("%s: %s %s, want %s %s", c.size, res, ratio, c.res, c.ratio)
		}
	}
	s, a := arkVideoProvider(t)
	body := `{"model":"ark/doubao-seedance-2-0-260128","prompt":"花开","size":"1920x1080","ratio":"adaptive"}`
	if code, _, raw, _ := request(t, s, http.MethodPost, "/v1/videos", "application/json", body); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if sent := a.sentBody(t, 0); sent["ratio"] != "adaptive" || sent["resolution"] != "1080p" {
		t.Fatalf("sent %v", sent)
	}
}

// A multipart form's JSON fields reach Ark parsed, and its text fields
// as text; a JSON request's fields go as they were typed.
func TestArkVideoFieldTypes(t *testing.T) {
	s, a := arkVideoProvider(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("model", "ark/doubao-seedance-2-0-260128")
	mw.WriteField("prompt", "花开")
	mw.WriteField("tools", `[{"type":"web_search"}]`)
	mw.WriteField("safety_identifier", "12345")
	mw.WriteField("return_last_frame", "true")
	mw.Close()
	if code, _, raw, _ := request(t, s, http.MethodPost, "/v1/videos", mw.FormDataContentType(), buf.String()); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	sent := a.sentBody(t, 0)
	tools, _ := sent["tools"].([]any)
	if len(tools) != 1 || sent["safety_identifier"] != "12345" || sent["return_last_frame"] != true {
		t.Fatalf("sent %v", sent)
	}

	body := `{"model":"ark/doubao-seedance-2-0-260128","prompt":"花开","safety_identifier":"12345","seed":"7"}`
	if code, _, raw, _ := request(t, s, http.MethodPost, "/v1/videos", "application/json", body); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if sent := a.sentBody(t, 1); sent["safety_identifier"] != "12345" || sent["seed"] != "7" {
		t.Fatalf("sent %v", sent)
	}
}

// A finished task's own fields (the last frame, usage, resolution, ratio,
// frames per second, seed…) are in the video magpie answers.
func TestArkVideoAnswersItsOwnFields(t *testing.T) {
	st := videoState{Status: "succeeded"}
	if err := json.Unmarshal([]byte(volcengineVideoSucceededResponse), &st.Ark); err != nil {
		t.Fatal(err)
	}
	st.Ark["content"].(map[string]any)["last_frame_url"] = "https://example.com/last.png"
	obj := videoObject("video_ark.x", "ark/doubao-seedance-2-0-260128", time.Unix(1, 0), st, filming{})
	content, _ := obj["content"].(map[string]any)
	usage, _ := obj["usage"].(map[string]any)
	if content["last_frame_url"] != "https://example.com/last.png" || content["video_url"] != "VIDEO_URL" || usage["completion_tokens"] != float64(48400) ||
		obj["resolution"] != "480p" || obj["ratio"] != "1:1" || obj["framespersecond"] != float64(24) || obj["seed"] != float64(14410) {
		t.Fatalf("answered %v", obj)
	}
	if obj["id"] != "video_ark.x" || obj["status"] != "completed" || obj["model"] != "ark/doubao-seedance-2-0-260128" {
		t.Fatalf("Ark's fields took magpie's place: %v", obj)
	}

	// Ark's expired is a task that ran out of time
	obj = videoObject("video_ark.x", "ark/m", time.Unix(1, 0), videoState{Status: "expired", Ark: map[string]any{"status": "expired"}}, filming{})
	e, _ := obj["error"].(map[string]string)
	if msg := e["message"]; !strings.Contains(msg, "timed out") {
		t.Fatalf("expired says %v", obj)
	}
	obj = videoObject("video_x.x", "x/m", time.Unix(1, 0), videoState{Status: "expired"}, filming{})
	e, _ = obj["error"].(map[string]string)
	if msg := e["message"]; strings.Contains(msg, "timed out") {
		t.Fatalf("another vendor's expired says %v", obj)
	}
}

// A MOV is served as one.
func TestVideoType(t *testing.T) {
	mov := []byte("\x00\x00\x00\x14ftypqt  ")
	mp4 := []byte("\x00\x00\x00\x18ftypisom")
	for _, c := range []struct {
		head []byte
		said string
		ark  map[string]any
		want string
	}{
		{mov, "video/mp4", nil, "video/quicktime"},
		{mp4, "video/mp4", nil, "video/mp4"},
		{mp4, "application/octet-stream", nil, "video/mp4"},
		{nil, "video/webm", nil, "video/webm"},
		{nil, "", map[string]any{"output_format": "mov"}, "video/quicktime"},
	} {
		if got := videoType(c.head, c.said, c.ark); got != c.want {
			t.Errorf("%q %q: %s, want %s", c.head, c.said, got, c.want)
		}
	}
}
