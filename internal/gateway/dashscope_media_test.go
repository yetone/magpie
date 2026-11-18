package gateway

import (
	"encoding/base64"
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

// fakeDashScopeMedia stands in for a Token Plan's DashScope host as
// Alibaba documents it (help.aliyun.com/zh/model-studio/
// qwen-audio-tts-http-api, qwen-audio-asr-http-api and
// wan3-video-generation-api-reference, read 11-18): the speech synthesizer
// answering with a URL to the audio, the multimodal generation API with a
// transcript, the video-synthesis API with a task, and the task API with
// its state. No real DashScope account was asked.
type fakeDashScopeMedia struct {
	*httptest.Server
	mu     sync.Mutex
	paths  []string
	bodies []string
	async  []string
	polls  int
	answer string // the next ASR or TTS answer, its documented one if ""
}

const (
	dashScopeTTSAnswer   = `{"request_id":"5c1b1e42-xxxx","output":{"finish_reason":"stop","audio":{"data":"","url":"AUDIO_URL","id":"audio_5c1b1e42","expires_at":1784342400}},"usage":{"characters":12}}`
	dashScopeASRAnswer   = `{"request_id":"9d3c-xxxx","output":{"text":"欢迎使用喜鹊。","sentence":{"sentence_id":0,"sentence_end":true,"begin_time":0,"end_time":1820,"text":"欢迎使用喜鹊。","channel_id":0,"words":[]}},"usage":{"duration":2}}`
	dashScopeTaskStarted = `{"request_id":"4909100c-xxxx","output":{"task_id":"0385dc79-5ff8-4d82-bcb6-xxxxxx","task_status":"PENDING"}}`
	dashScopeTaskDone    = `{"request_id":"caa62a12-xxxx","output":{"task_id":"0385dc79-5ff8-4d82-bcb6-xxxxxx","task_status":"SUCCEEDED","submit_time":"2026-11-18 10:00:00.000","scheduled_time":"2026-11-18 10:00:05.000","end_time":"2026-11-18 10:02:40.000","orig_prompt":"一只喜鹊落在枝头","video_url":"VIDEO_URL"},"usage":{"duration":5,"video_count":1,"SR":720}}`
	dashScopeTaskFailed  = `{"request_id":"e5d70b02-xxxx","output":{"task_id":"86ecf553-xxxx","task_status":"FAILED","code":"DataInspectionFailed","message":"Input data may contain inappropriate content."}}`
)

func newFakeDashScopeMedia(t *testing.T) *fakeDashScopeMedia {
	f := &fakeDashScopeMedia{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.paths = append(f.paths, r.Method+" "+r.URL.Path)
		f.bodies = append(f.bodies, string(body))
		f.async = append(f.async, r.Header.Get("X-DashScope-Async"))
		answer := f.answer
		f.answer = ""
		f.mu.Unlock()
		switch r.URL.Path {
		case "/oss/speech.mp3":
			w.Header().Set("Content-Type", "audio/mpeg")
			io.WriteString(w, "ID3-mp3-bytes")
			return
		case "/oss/video.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			io.WriteString(w, "wan-mp4-bytes")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer sk-sp-key" {
			w.WriteHeader(401)
			io.WriteString(w, `{"request_id":"1","code":"InvalidApiKey","message":"Invalid API-key provided."}`)
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v1/services/audio/tts/SpeechSynthesizer":
			if answer == "" {
				answer = strings.Replace(dashScopeTTSAnswer, "AUDIO_URL", f.URL+"/oss/speech.mp3", 1)
			}
		case r.Method == "POST" && r.URL.Path == "/api/v1/services/aigc/multimodal-generation/generation":
			if answer == "" {
				answer = dashScopeASRAnswer
			}
		case r.Method == "POST" && r.URL.Path == "/api/v1/services/aigc/video-generation/video-synthesis":
			if r.Header.Get("X-DashScope-Async") != "enable" {
				w.WriteHeader(403)
				io.WriteString(w, `{"request_id":"2","code":"AccessDenied","message":"current user api does not support synchronous calls"}`)
				return
			}
			if strings.Contains(string(body), "FAILING") {
				answer = strings.Replace(dashScopeTaskStarted, "0385dc79-5ff8-4d82-bcb6-xxxxxx", "86ecf553-xxxx", 1)
			} else {
				answer = dashScopeTaskStarted
			}
		case r.Method == "GET" && r.URL.Path == "/api/v1/tasks/0385dc79-5ff8-4d82-bcb6-xxxxxx":
			f.mu.Lock()
			f.polls++
			n := f.polls
			f.mu.Unlock()
			switch n {
			case 1:
				answer = strings.Replace(dashScopeTaskStarted, `"PENDING"`, `"PENDING","submit_time":"2026-11-18 10:00:00.000"`, 1)
			case 2:
				answer = strings.Replace(dashScopeTaskStarted, `"PENDING"`, `"RUNNING"`, 1)
			default:
				answer = strings.Replace(dashScopeTaskDone, "VIDEO_URL", f.URL+"/oss/video.mp4", 1)
			}
		case r.Method == "GET" && r.URL.Path == "/api/v1/tasks/86ecf553-xxxx":
			answer = dashScopeTaskFailed
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"request_id":"3","code":"NotFound","message":"unexpected path"}`)
			return
		}
		io.WriteString(w, answer)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeDashScopeMedia) seen() (paths, bodies, async []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...), append([]string(nil), f.bodies...), append([]string(nil), f.async...)
}

// tokenPlan saves a Token Plan key of preset pointed at the fake.
func tokenPlan(t *testing.T, preset string, up *fakeDashScopeMedia) provider.Provider {
	t.Helper()
	fresh(t)
	p, err := provider.FromPreset(preset)
	if err != nil {
		t.Fatal(err)
	}
	p.ID, p.Key = "qwen", "sk-sp-key"
	p.Chat, p.Anthropic = up.URL+"/compatible-mode/v1", up.URL+"/apps/anthropic"
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return p
}

// Every Token Plan names the plan's text, image, video, speech and
// transcription models, each kind in its own list (@gaubeebangeel on X:
// the plan's console lists the six kinds and their endpoints).
func TestTokenPlansNameEveryKind(t *testing.T) {
	fresh(t)
	ids := func(ms []catalog.Model) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.ID)
		}
		return out
	}
	for _, preset := range []string{"qwen-token-plan", "bailian-token-plan", "alibaba-token-plan", "qwencloud-token-plan"} {
		p, err := provider.FromPreset(preset)
		if err != nil {
			t.Fatal(err)
		}
		p.ID, p.Key = preset, "sk-sp-key"
		if chat := provider.Preset(preset).Models; !slices.Contains(chat, "deepseek-v4.1-flash-1017") {
			t.Fatalf("%s chats with %v", preset, chat)
		}
		if got := ids(p.PlanDrawers()); !strings.Contains(strings.Join(got, ","), "qwen-image-3.0-pro") {
			t.Fatalf("%s draws with %v", preset, got)
		}
		if got := ids(p.PlanVideos()); strings.Join(got, ",") != "wan3.0-video,happyhorse-1.1-t2v,happyhorse-1.1-i2v,happyhorse-1.1-r2v" {
			t.Fatalf("%s films with %v", preset, got)
		}
		if got := ids(p.PlanSpeakers()); strings.Join(got, ",") != "qwen-audio-3.0-tts-plus" {
			t.Fatalf("%s speaks with %v", preset, got)
		}
		if got := ids(p.PlanTranscribers()); strings.Join(got, ",") != "qwen-audio-3.0-asr-flash" {
			t.Fatalf("%s transcribes with %v", preset, got)
		}
		if !filmsDashScope(p) || !makesDashScopeVideo(p, "wan3.0-video") || makesDashScopeVideo(p, "qwen3.8-max") {
			t.Fatalf("%s isn't asked for videos at DashScope", preset)
		}
		if !dashScopeAudio(p, "qwen-audio-3.0-tts-plus", catalog.ServesSpeech) || !dashScopeAudio(p, "qwen-audio-3.0-asr-flash", catalog.ServesTranscription) || dashScopeAudio(p, "qwen-audio-3.0-asr-flash", catalog.ServesSpeech) {
			t.Fatalf("%s's audio models aren't asked at DashScope", preset)
		}
	}
	// another vendor's key, and a relay that serves Qwen's models: neither
	for _, p := range []provider.Provider{
		{ID: "x", Chat: "https://api.deepseek.com/v1", Key: "k"},
		{ID: "relay", Chat: "https://relay.example.com/qwen/v1", Key: "k"},
	} {
		if filmsDashScope(p) || dashScopeAudio(p, "qwen-audio-3.0-tts-plus", catalog.ServesSpeech) {
			t.Fatalf("%s is asked as DashScope", p.ID)
		}
	}
}

// /v1/audio/speech with a Token Plan's qwen-audio TTS model goes to
// DashScope's speech synthesizer, the input read in the model's own voice
// for an OpenAI name, the instructions and speed as its instruction and
// rate, and the audio fetched from the URL it answers with.
func TestTokenPlanSpeaks(t *testing.T) {
	up := newFakeDashScopeMedia(t)
	tokenPlan(t, "qwen-token-plan", up)
	s := New()
	code, h, got := postSpeech(t, s, "/v1/audio/speech", `{"model":"qwen/qwen-audio-3.0-tts-plus","input":"欢迎使用喜鹊。","voice":"alloy","instructions":"用开心的语气","speed":3}`)
	if code != 200 || string(got) != "ID3-mp3-bytes" || h.Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("%d %v %q", code, h, got)
	}
	paths, bodies, _ := up.seen()
	if paths[0] != "POST /api/v1/services/audio/tts/SpeechSynthesizer" || paths[1] != "GET /oss/speech.mp3" {
		t.Fatalf("paths %v", paths)
	}
	var sent struct {
		Model string         `json:"model"`
		Input map[string]any `json:"input"`
	}
	json.Unmarshal([]byte(bodies[0]), &sent)
	in := sent.Input
	if sent.Model != "qwen-audio-3.0-tts-plus" || in["text"] != "欢迎使用喜鹊。" || in["voice"] != "longanlingxin" || in["format"] != "mp3" || in["instruction"] != "用开心的语气" || in["rate"] != float64(2) {
		t.Fatalf("sent %s", bodies[0])
	}

	// its own voice and a WAV; audio in data is decoded, not fetched
	up.mu.Lock()
	up.answer = `{"output":{"audio":{"data":"` + base64.StdEncoding.EncodeToString([]byte("RIFF-wav")) + `","url":""}},"usage":{"characters":3}}`
	up.mu.Unlock()
	code, h, got = postSpeech(t, s, "/v1/audio/speech", `{"model":"qwen/qwen-audio-3.0-tts-plus","input":"你好","voice":"longanlufeng","response_format":"wav"}`)
	if code != 200 || string(got) != "RIFF-wav" || h.Get("Content-Type") != "audio/wav" {
		t.Fatalf("%d %v %q", code, h, got)
	}
	_, bodies, _ = up.seen()
	if !strings.Contains(bodies[len(bodies)-1], `"voice":"longanlufeng"`) || !strings.Contains(bodies[len(bodies)-1], `"format":"wav"`) {
		t.Fatalf("sent %s", bodies[len(bodies)-1])
	}

	// what DashScope doesn't make is turned away before it is asked
	n := len(bodies)
	for body, want := range map[string]string{
		`{"model":"qwen/qwen-audio-3.0-tts-plus","input":"x","response_format":"flac"}`: "mp3 (the default), wav, pcm or opus",
		`{"model":"qwen/qwen-audio-3.0-tts-plus","input":"x","stream_format":"sse"}`:    "without stream_format sse",
		`{"model":"qwen/qwen-audio-3.0-tts-plus","input":" "}`:                          "nothing to read aloud",
	} {
		if code, _, got := postSpeech(t, s, "/v1/audio/speech", body); code != 400 || !strings.Contains(string(got), want) {
			t.Fatalf("%s: %d %s", body, code, got)
		}
	}
	if _, bodies, _ = up.seen(); len(bodies) != n {
		t.Fatalf("a refused request was sent: %v", bodies[n:])
	}

	// DashScope's error says why
	up.mu.Lock()
	up.answer = `{"request_id":"7","code":"InvalidParameter","message":"Voice not supported for this model."}`
	up.mu.Unlock()
	if code, _, got := postSpeech(t, s, "/v1/audio/speech", `{"model":"qwen/qwen-audio-3.0-tts-plus","input":"x","voice":"nobody"}`); code != 502 || !strings.Contains(string(got), "Voice not supported") {
		t.Fatalf("%d %s", code, got)
	}
}

// /v1/audio/transcriptions with a Token Plan's qwen-audio ASR model sends
// the file to DashScope's multimodal generation API as a data URI, its
// format from the file's name and the language as a hint, and answers in
// OpenAI's shapes; a translation and srt are turned away.
func TestTokenPlanTranscribes(t *testing.T) {
	up := newFakeDashScopeMedia(t)
	tokenPlan(t, "bailian-token-plan", up)
	s := New()
	body, ctype := audioFormBody(t, "model", "qwen/qwen-audio-3.0-asr-flash", "language", "zh", "prompt", "喜鹊")
	code, h, got := postAudio(t, s, "/v1/audio/transcriptions", ctype, body)
	if code != 200 || got != `{"text":"欢迎使用喜鹊。"}` || !strings.HasPrefix(h.Get("Content-Type"), "application/json") {
		t.Fatalf("%d %v %s", code, h, got)
	}
	paths, bodies, _ := up.seen()
	if len(paths) != 1 || paths[0] != "POST /api/v1/services/aigc/multimodal-generation/generation" {
		t.Fatalf("paths %v", paths)
	}
	var sent struct {
		Model string `json:"model"`
		Input struct {
			Messages []struct {
				Role    string `json:"role"`
				Content []struct {
					Type       string            `json:"type"`
					InputAudio map[string]string `json:"input_audio"`
				} `json:"content"`
			} `json:"messages"`
		} `json:"input"`
		Parameters map[string]any `json:"parameters"`
	}
	if err := json.Unmarshal([]byte(bodies[0]), &sent); err != nil || len(sent.Input.Messages) != 1 || len(sent.Input.Messages[0].Content) != 1 {
		t.Fatalf("sent %.300s: %v", bodies[0], err)
	}
	c := sent.Input.Messages[0].Content[0]
	if sent.Model != "qwen-audio-3.0-asr-flash" || c.Type != "input_audio" || c.InputAudio["data"] != "data:audio/webm;base64,"+base64.StdEncoding.EncodeToString(voice) {
		t.Fatalf("sent %.300s", bodies[0])
	}
	if sent.Parameters["format"] != "webm" || strings.Join(anyStrings(sent.Parameters["language_hints"]), ",") != "zh" {
		t.Fatalf("parameters %v", sent.Parameters)
	}
	if strings.Contains(bodies[0], "喜鹊\"") {
		t.Fatalf("the prompt was sent: %.300s", bodies[0])
	}

	body, ctype = audioFormBody(t, "model", "qwen/qwen-audio-3.0-asr-flash", "response_format", "text")
	if code, h, got := postAudio(t, s, "/v1/audio/transcriptions", ctype, body); code != 200 || got != "欢迎使用喜鹊。" || !strings.HasPrefix(h.Get("Content-Type"), "text/plain") {
		t.Fatalf("text: %d %v %s", code, h, got)
	}
	body, ctype = audioFormBody(t, "model", "qwen/qwen-audio-3.0-asr-flash", "response_format", "verbose_json")
	if code, _, got := postAudio(t, s, "/v1/audio/transcriptions", ctype, body); code != 200 || !strings.Contains(got, `"duration":2`) || !strings.Contains(got, `"text":"欢迎使用喜鹊。"`) {
		t.Fatalf("verbose_json: %d %s", code, got)
	}

	_, bodies, _ = up.seen()
	n := len(bodies)
	body, ctype = audioFormBody(t, "model", "qwen/qwen-audio-3.0-asr-flash", "response_format", "srt")
	if code, _, got := postAudio(t, s, "/v1/audio/transcriptions", ctype, body); code != 400 || !strings.Contains(got, "json (the default), text or verbose_json") {
		t.Fatalf("srt: %d %s", code, got)
	}
	body, ctype = audioFormBody(t, "model", "qwen/qwen-audio-3.0-asr-flash")
	if code, _, got := postAudio(t, s, "/v1/audio/translations", ctype, body); code != 400 || !strings.Contains(got, "no translation") {
		t.Fatalf("translation: %d %s", code, got)
	}
	if _, bodies, _ = up.seen(); len(bodies) != n {
		t.Fatalf("a refused request was sent: %d", len(bodies)-n)
	}
}

func anyStrings(v any) []string {
	var out []string
	for _, e := range v.([]any) {
		out = append(out, e.(string))
	}
	return out
}

// /v1/videos with a Token Plan's Wan model starts a DashScope task with
// X-DashScope-Async, is asked after at /api/v1/tasks/<id> until it has
// succeeded, and its MP4 is fetched from video_url; the size becomes the
// resolution and ratio, seconds the duration, and the image the first
// frame.
func TestTokenPlanMakesVideos(t *testing.T) {
	up := newFakeDashScopeMedia(t)
	tokenPlan(t, "qwencloud-token-plan", up)
	s := New()
	png := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("png"))
	code, obj, raw, _ := request(t, s, http.MethodPost, "/v1/videos", "application/json", `{"model":"qwen/wan3.0-video","prompt":"一只喜鹊落在枝头","seconds":"8","size":"1280x720","input_reference":{"image_url":"`+png+`"}}`)
	if code != 200 || obj["status"] != "queued" || obj["model"] != "qwen/wan3.0-video" {
		t.Fatalf("create: %d %s", code, raw)
	}
	id, _ := obj["id"].(string)
	if !strings.HasSuffix(id, "."+keyMark(provider.Provider{Key: "sk-sp-key"})) {
		t.Fatalf("id %q", id)
	}
	for _, want := range []string{"queued", "in_progress"} {
		code, obj, raw, _ = request(t, s, http.MethodGet, "/v1/videos/"+id, "", "")
		if code != 200 || obj["status"] != want {
			t.Fatalf("want %s: %d %s", want, code, raw)
		}
	}
	code, obj, raw, _ = request(t, s, http.MethodGet, "/v1/videos/"+id, "", "")
	if code != 200 || obj["status"] != "completed" || obj["model"] != "qwen/wan3.0-video" {
		t.Fatalf("succeeded: %d %s", code, raw)
	}
	code, _, raw, head := request(t, s, http.MethodGet, "/v1/videos/"+id+"/content", "", "")
	if code != 200 || string(raw) != "wan-mp4-bytes" || head.Get("Content-Type") != "video/mp4" {
		t.Fatalf("content: %d %q %v", code, raw, head)
	}
	paths, bodies, async := up.seen()
	if paths[0] != "POST /api/v1/services/aigc/video-generation/video-synthesis" || async[0] != "enable" || paths[1] != "GET /api/v1/tasks/0385dc79-5ff8-4d82-bcb6-xxxxxx" || paths[len(paths)-1] != "GET /oss/video.mp4" {
		t.Fatalf("paths %v async %v", paths, async)
	}
	var sent struct {
		Model string `json:"model"`
		Input struct {
			Prompt string              `json:"prompt"`
			Media  []map[string]string `json:"media"`
		} `json:"input"`
		Parameters map[string]any `json:"parameters"`
	}
	json.Unmarshal([]byte(bodies[0]), &sent)
	pm := sent.Parameters
	if sent.Model != "wan3.0-video" || sent.Input.Prompt != "一只喜鹊落在枝头" || pm["resolution"] != "720P" || pm["ratio"] != "16:9" || pm["duration"] != float64(8) || pm["watermark"] != false {
		t.Fatalf("create body %s", bodies[0])
	}
	if len(sent.Input.Media) != 1 || sent.Input.Media[0]["type"] != "first_frame" || sent.Input.Media[0]["url"] != png {
		t.Fatalf("media %v", sent.Input.Media)
	}

	// a failed task says why
	code, obj, raw, _ = request(t, s, http.MethodPost, "/v1/videos", "application/json", `{"model":"qwen/wan3.0-video","prompt":"FAILING"}`)
	if code != 200 {
		t.Fatalf("create: %d %s", code, raw)
	}
	code, obj, raw, _ = request(t, s, http.MethodGet, "/v1/videos/"+obj["id"].(string), "", "")
	if code != 200 || obj["status"] != "failed" || !strings.Contains(string(raw), "inappropriate content") {
		t.Fatalf("failed: %d %s", code, raw)
	}
}

// The body as DashScope's video API takes it: an "a:b" size is the ratio,
// first and last frames and references each with their type, and the
// caller's own fields where DashScope reads them.
func TestDashScopeVideoBody(t *testing.T) {
	png := picture{Mime: "image/png", Data: []byte("png")}
	end := picture{URL: "https://example.com/end.png"}
	b, err := dashScopeVideoBody("wan3.0-video", filming{Prompt: "p", Size: "9:16", Start: &png, End: &end, References: []picture{end}, Extra: map[string]any{"resolution": "1080p", "negative_prompt": "blur", "audio": true}})
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Input      map[string]any `json:"input"`
		Parameters map[string]any `json:"parameters"`
	}
	json.Unmarshal(b, &m)
	media, _ := m.Input["media"].([]any)
	if len(media) != 3 || media[0].(map[string]any)["type"] != "first_frame" || media[1].(map[string]any)["type"] != "last_frame" || media[2].(map[string]any)["url"] != "https://example.com/end.png" {
		t.Fatalf("media %v", media)
	}
	if m.Input["negative_prompt"] != "blur" || m.Parameters["resolution"] != "1080P" || m.Parameters["ratio"] != "9:16" || m.Parameters["audio"] != true {
		t.Fatalf("body %s", b)
	}
	if _, ok := m.Parameters["duration"]; ok {
		t.Fatalf("a duration with none asked: %s", b)
	}
	if _, err := dashScopeVideoBody("wan3.0-video", filming{Prompt: "p", Seconds: "4.5"}); err == nil {
		t.Fatal("4.5 seconds was taken")
	}
	if _, _, ok := dashScopeTask("not-base64!"); ok {
		t.Fatal("a foreign id was read as a task")
	}
	if _, _, ok := dashScopeTask(base64.RawURLEncoding.EncodeToString([]byte("wan3.0-video ../../x"))); ok {
		t.Fatal("a task with a path was read")
	}
}
