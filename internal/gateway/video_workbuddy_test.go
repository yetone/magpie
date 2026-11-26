package gateway

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// The video models of WorkBuddy's China build as its product config lists
// them (GET /v3/config, read 2026-11-26), with their videoGeneration specs
// cut to the fields magpie reads, beside a chat model and an image model.
const wbVideoModels = `[{"id":"hy3","name":"Hy3","tags":[]},
	{"id":"hunyuan-image-alpha","name":"Hunyuan Image Alpha","tags":["text-to-image"]},
	{"id":"kling-video-v3","name":"Kling-V3","tags":["text-to-video","image-to-video"],
	 "videoGeneration":{"aspectRatios":["16:9","9:16","1:1"],"defaultAspectRatio":"16:9","resolutions":["720P","1080P","4K"],"defaultResolution":"720P",
	  "defaultSeconds":5,"minSeconds":3,"maxSeconds":15,"audioMode":"switchable","defaultEnableAudio":true,
	  "supportsFirstFrame":true,"supportsFirstLastFrame":true,"lastFrameRequiresFirstFrame":true,"maxPromptLength":2500,"maxReferenceImages":0}},
	{"id":"minimax-video-h3","name":"MiniMax-H3","tags":["text-to-video","image-to-video"],
	 "videoGeneration":{"aspectRatios":["adaptive","21:9","16:9","4:3","1:1","3:4","9:16"],"defaultAspectRatio":"adaptive","resolutions":["768P","2K"],"defaultResolution":"768P",
	  "defaultSeconds":5,"minSeconds":4,"maxSeconds":15,"audioMode":"always-on",
	  "supportsFirstFrame":true,"supportsFirstLastFrame":true,"maxPromptLength":2000,"maxReferenceImages":9}}]`

// wbFilming is WorkBuddy's videos API as its CLI meets it: a task started
// in the {code, msg, data} envelope, running on its first ask and done,
// with the video's URL, on the next.
func wbFilming(refuse *atomic.Bool) *wbStudio {
	var polls atomic.Int32
	return &wbStudio{models: wbVideoModels, answer: func(path string, body map[string]any) string {
		switch {
		case refuse != nil && refuse.Load():
			return `{"code":14018,"msg":"credits used up","requestId":"r9","data":null}`
		case path == "/v1/videos/generations":
			return `{"code":0,"msg":"OK","requestId":"r1","data":{"id":"vt-1"}}`
		case path == "/v1/videos/tasks" && body["task_id"] == "vt-2":
			return `{"code":0,"msg":"OK","data":{"status":"failed","message":"the prompt was refused","data":[]}}`
		case path == "/v1/videos/tasks" && polls.Add(1) == 1:
			return `{"code":0,"msg":"OK","data":{"status":"processing","data":[]}}`
		case path == "/v1/videos/tasks":
			return `{"code":0,"msg":"OK","data":{"status":"completed","data":[{"url":"https://cos.example/v/vt-1.mp4?sign=x"}],"usage":{"credit":60}}}`
		}
		return `{"code":404,"msg":"no such path"}`
	}}
}

// A WorkBuddy plan's video models are those its config tags as filming,
// listed with what each takes; never picked by Automatic, never among its
// drawers. A video is started at /v2/videos/generations as WorkBuddy's CLI
// starts one, polled at /v2/videos/tasks, and the finished one's URL is
// the video.
func TestWorkBuddyMakesVideos(t *testing.T) {
	st := wbFilming(nil)
	p := wbSignedIn(t, "workbuddy", st)
	if _, err := wbImageModelsOf(context.Background(), wbConfigClient, p, true); err != nil {
		t.Fatal(err)
	}
	var vs []string
	for _, m := range Videomakers(p) {
		if !m.Films || m.Provider != "workbuddy" {
			t.Fatalf("videomaker %+v", m)
		}
		vs = append(vs, m.ID+"="+m.Name)
	}
	if strings.Join(vs, ",") != "kling-video-v3=Kling-V3,minimax-video-h3=MiniMax-H3" {
		t.Fatalf("videomakers %v", vs)
	}
	if ds := Drawers(p); len(ds) != 1 || ds[0].ID != "hunyuan-image-alpha" {
		t.Fatalf("drawers %+v", ds)
	}
	if a := AutoVideomaker(); a != "" {
		t.Fatalf("AutoVideomaker picks %q", a)
	}

	s := New()
	code, obj, raw, _ := request(t, s, http.MethodPost, "/v1/videos", "application/json", `{"model":"workbuddy/kling-video-v3","prompt":"a magpie lands on a branch","seconds":"8","size":"1920x1080"}`)
	if code != 200 || obj["status"] != "queued" || obj["model"] != "workbuddy/kling-video-v3" {
		t.Fatalf("create: %d %s", code, raw)
	}
	body, head := st.got("/v1/videos/generations")
	extra, _ := body["extra_parameters"].(map[string]any)
	if body["model"] != "kling-video-v3" || body["prompt"] != "a magpie lands on a branch" || body["seconds"] != 8.0 || body["negative_prompt"] != "" || body["watermark"] != true {
		t.Fatalf("sent %v", body)
	}
	if extra["resolution"] != "1080P" || extra["enable_audio"] != true || extra["aspect_ratio"] != "16:9" || len(extra) != 3 {
		t.Fatalf("extra_parameters %v", extra)
	}
	if _, ok := body["image_url"]; ok {
		t.Fatalf("a frame was sent: %v", body)
	}
	if head.Get("Authorization") == "" {
		t.Fatalf("not sent through the plugin's fetch: %v", head)
	}

	id, _ := obj["id"].(string)
	code, obj, raw, _ = request(t, s, http.MethodGet, "/v1/videos/"+id, "", "")
	if code != 200 || obj["status"] != "in_progress" {
		t.Fatalf("running: %d %s", code, raw)
	}
	if body, _ := st.got("/v1/videos/tasks"); body["task_id"] != "vt-1" || len(body) != 1 {
		t.Fatalf("polled with %v", body)
	}
	code, obj, raw, _ = request(t, s, http.MethodGet, "/v1/videos/"+id, "", "")
	if code != 200 || obj["status"] != "completed" || obj["model"] != "workbuddy/kling-video-v3" {
		t.Fatalf("done: %d %s", code, raw)
	}
	_, vendorID, _, by, ok := parseVideoID(id)
	if !ok || by != keyMark(p) {
		t.Fatalf("id %q", id)
	}
	vs2, _, err := s.wbVideoStatus(context.Background(), p, vendorID)
	if err != nil || vs2.Video.URL != "https://cos.example/v/vt-1.mp4?sign=x" || vs2.Model != "kling-video-v3" {
		t.Fatalf("status %+v %v", vs2, err)
	}

	// a first frame goes as a data URL, the ratio left to it; MiniMax-H3's
	// own defaults where the caller said none
	png := "data:image/png;base64,cG5n"
	code, _, raw, _ = request(t, s, http.MethodPost, "/v1/videos", "application/json", `{"model":"workbuddy/minimax-video-h3","prompt":"it flies off","input_reference":"`+png+`","negative_prompt":"blur"}`)
	if code != 200 {
		t.Fatalf("frame: %d %s", code, raw)
	}
	body, _ = st.got("/v1/videos/generations")
	extra, _ = body["extra_parameters"].(map[string]any)
	if body["model"] != "minimax-video-h3" || body["image_url"] != png || body["seconds"] != 5.0 || body["negative_prompt"] != "blur" {
		t.Fatalf("frame sent %v", body)
	}
	if _, ok := extra["aspect_ratio"]; ok || extra["resolution"] != "768P" || extra["enable_audio"] != true {
		t.Fatalf("frame's extra_parameters %v", extra)
	}

	// a failed task says why
	failed := strings.TrimSuffix(videoID(p, wbVendorID("kling-video-v3", "vt-2"), time.Now()), "."+signer(p)) + "." + keyMark(p)
	code, obj, raw, _ = request(t, s, http.MethodGet, "/v1/videos/"+failed, "", "")
	if code != 200 || obj["status"] != "failed" || !strings.Contains(string(raw), "the prompt was refused") {
		t.Fatalf("failed: %d %s", code, raw)
	}
}

// What a WorkBuddy video model can't take is refused with why before
// WorkBuddy is asked, and nothing is spent; WorkBuddy's own refusal, an
// answer of 200 whose code isn't 0, is an error with its words.
func TestWorkBuddyVideoRefusals(t *testing.T) {
	var refuse atomic.Bool
	st := wbFilming(&refuse)
	p := wbSignedIn(t, "workbuddy", st)
	if _, err := wbImageModelsOf(context.Background(), wbConfigClient, p, true); err != nil {
		t.Fatal(err)
	}
	s := New()
	png := "data:image/png;base64,cG5n"
	for _, c := range []struct{ body, says string }{
		{`{"model":"workbuddy/kling-video-v3","prompt":"x","seconds":"20"}`, "3 to 15 seconds, not 20"},
		{`{"model":"workbuddy/kling-video-v3","prompt":"x","resolution":"2K"}`, "720P, 1080P, 4K, not 2K"},
		{`{"model":"workbuddy/kling-video-v3","prompt":"x","aspect_ratio":"21:9"}`, "not 21:9"},
		{`{"model":"workbuddy/kling-video-v3","prompt":"x","input_reference":"` + png + `","aspect_ratio":"9:16"}`, "no aspect_ratio with a frame"},
		{`{"model":"workbuddy/kling-video-v3","prompt":"x","last_frame":"` + png + `"}`, "only with the first"},
		{`{"model":"workbuddy/kling-video-v3","prompt":"x","reference_images":["` + png + `"]}`, "no reference images"},
		{`{"model":"workbuddy/minimax-video-h3","prompt":"x","enable_audio":false}`, "always makes its videos with sound"},
		{`{"model":"workbuddy/minimax-video-h3","prompt":"` + strings.Repeat("长", 2001) + `"}`, "at most 2000 characters"},
		{`{"model":"workbuddy/hy3","prompt":"x"}`, "kling-video-v3, minimax-video-h3"},
	} {
		code, obj, raw, _ := request(t, s, http.MethodPost, "/v1/videos", "application/json", c.body)
		if code != 400 || !strings.Contains(errorOf(obj), c.says) {
			t.Fatalf("%.80s: %d %s", c.body, code, raw)
		}
	}
	if n := st.count("POST /v1/videos/generations"); n != 0 {
		t.Fatalf("WorkBuddy was asked %d times", n)
	}
	// the caller's own ratio, length and sound, as said
	code, _, raw, _ := request(t, s, http.MethodPost, "/v1/videos", "application/json", `{"model":"workbuddy/minimax-video-h3","prompt":"x","seconds":4,"aspect_ratio":"21:9","resolution":"2k","size":"720x1280"}`)
	body, _ := st.got("/v1/videos/generations")
	extra, _ := body["extra_parameters"].(map[string]any)
	if code != 200 || body["seconds"] != 4.0 || extra["aspect_ratio"] != "21:9" || extra["resolution"] != "2K" {
		t.Fatalf("own fields: %d %s, sent %v", code, raw, body)
	}
	// a size alone picks the spec's nearest ratio, never adaptive
	request(t, s, http.MethodPost, "/v1/videos", "application/json", `{"model":"workbuddy/minimax-video-h3","prompt":"x","size":"720x1280"}`)
	body, _ = st.got("/v1/videos/generations")
	extra, _ = body["extra_parameters"].(map[string]any)
	if extra["aspect_ratio"] != "9:16" || extra["resolution"] != "768P" {
		t.Fatalf("size 720x1280 sent %v", extra)
	}
	// none said: 16:9 as WorkBuddy's CLI sends, not MiniMax-H3's "adaptive"
	request(t, s, http.MethodPost, "/v1/videos", "application/json", `{"model":"workbuddy/minimax-video-h3","prompt":"x"}`)
	body, _ = st.got("/v1/videos/generations")
	extra, _ = body["extra_parameters"].(map[string]any)
	if extra["aspect_ratio"] != "16:9" {
		t.Fatalf("no ratio sent %v", extra)
	}
	refuse.Store(true)
	code, obj, raw, _ := request(t, s, http.MethodPost, "/v1/videos", "application/json", `{"model":"workbuddy/kling-video-v3","prompt":"x"}`)
	if code != 502 || !strings.Contains(errorOf(obj), "credits used up (14018)") {
		t.Fatalf("refused: %d %s", code, raw)
	}
}

// wbVendorID is the vendor id wbVideoStarted gives a task of model.
func wbVendorID(model, task string) string {
	b, _ := wbVideoStarted(provider.Provider{Name: "WorkBuddy"}, model, []byte(`{"code":0,"data":{"id":"`+task+`"}}`))
	return strings.TrimSuffix(strings.TrimPrefix(string(b), `{"task_id":"`), `"}`)
}
