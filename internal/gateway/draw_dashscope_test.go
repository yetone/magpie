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

	"github.com/yetone/magpie/internal/provider"
)

// dashScopeImages stands in for a DashScope host as Qwen Cloud's Token
// Plan is documented (docs.qwencloud.com: "Qwen-Image text-to-image", the
// Token Plan's "Integrate multimodal generation"): the multimodal-
// generation API at /api/v1/services/aigc/multimodal-generation/generation,
// answering with its documented bytes, and no images API under
// /compatible-mode/v1. No real DashScope or Qwen Cloud account was asked.
type dashScopeImages struct {
	mu     sync.Mutex
	paths  []string
	bodies []string
	answer string // what the native API answers, its documented one if ""
	status int
}

const dashScopeOneImage = `{"status_code":200,"request_id":"d2d1a8c0-325f-9b9d-8b90-xxxxxx","code":"","message":"","output":{"text":null,"finish_reason":null,"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":[{"image":"https://dashscope-result-intl.oss-cn-shanghai.aliyuncs.com/xxx.png?Expires=xxx"}]}}]},"usage":{"output_height":2048,"output_width":2048,"input_image_count":0,"input_image_type":"qima_input_2k","output_image_count":1,"output_image_type":"qima_output_2k"}}`

// the image-editing example's answer for n=2: one choice, two images
const dashScopeTwoImages = `{"status_code":200,"request_id":"fa41f9f9-3cb6-434d-a95d-xxxxxx","code":"","message":"","output":{"text":null,"finish_reason":null,"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":[{"image":"https://dashscope-result-intl.oss-cn-shanghai.aliyuncs.com/a.png?Expires=xxx"},{"image":"https://dashscope-result-intl.oss-cn-shanghai.aliyuncs.com/b.png?Expires=xxx"}]}}]},"usage":{"width":1024,"height":1024,"image_count":2}}`

func (d *dashScopeImages) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	body, _ := io.ReadAll(r.Body)
	d.mu.Lock()
	d.paths = append(d.paths, r.URL.Path)
	d.bodies = append(d.bodies, string(body))
	answer, status := d.answer, d.status
	d.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer sk-sp-key" {
		w.WriteHeader(401)
		io.WriteString(w, `{"request_id":"1","code":"InvalidApiKey","message":"Invalid API-key provided."}`)
		return
	}
	if r.Method != "POST" || r.URL.Path != "/api/v1/services/aigc/multimodal-generation/generation" {
		w.WriteHeader(404)
		return
	}
	if status != 0 {
		w.WriteHeader(status)
	}
	if answer == "" {
		answer = dashScopeOneImage
	}
	io.WriteString(w, answer)
}

func (d *dashScopeImages) sent(t *testing.T) map[string]any {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.bodies) == 0 {
		t.Fatal("nothing sent")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(d.bodies[len(d.bodies)-1]), &m); err != nil {
		t.Fatalf("sent %s: %v", d.bodies[len(d.bodies)-1], err)
	}
	return m
}

func dashScoped(t *testing.T, p provider.Provider) (*Server, *dashScopeImages) {
	t.Helper()
	fresh(t)
	d := &dashScopeImages{}
	up := httptest.NewServer(d)
	t.Cleanup(up.Close)
	p.Chat, p.Anthropic = up.URL+"/compatible-mode/v1", up.URL+"/apps/anthropic"
	if p.Key == "" {
		p.Key = "sk-sp-key"
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return New(), d
}

func imageURLs(t *testing.T, raw string) []string {
	t.Helper()
	var a struct {
		Data []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(raw), &a)
	var out []string
	for _, e := range a.Data {
		out = append(out, e.URL)
	}
	return out
}

// qwen-image-3.0-pro on a Qwen Token Plan drew nothing (judi0713 on
// Discord): its host has no OpenAI images API, and the chat completions it
// went on to answer with no image magpie reads. It is asked on DashScope's
// own API, and its image read from output.choices[].message.content[].image.
func TestDashScopeDrawsOnItsOwnAPI(t *testing.T) {
	s, d := dashScoped(t, provider.Provider{ID: "qwen", Name: "Qwen Token Plan", Preset: "qwen-token-plan", Models: []string{"qwen3.8-max", "qwen-image-3.0-pro"}})
	code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"qwen/qwen-image-3.0-pro","prompt":"一只喜鹊","size":"1024x1024"}`)
	if code != 200 {
		t.Fatalf("%d %s (asked %v)", code, raw, d.paths)
	}
	if got := imageURLs(t, raw); len(got) != 1 || got[0] != "https://dashscope-result-intl.oss-cn-shanghai.aliyuncs.com/xxx.png?Expires=xxx" {
		t.Fatalf("images %v: %s", got, raw)
	}
	m := d.sent(t)
	params, _ := m["parameters"].(map[string]any)
	if m["model"] != "qwen-image-3.0-pro" || params["size"] != "1024*1024" || params["watermark"] != false {
		t.Fatalf("sent %v", m)
	}
	if _, ok := params["n"]; ok {
		t.Fatalf("sent n for one image: %v", m)
	}
	msgs, _ := m["input"].(map[string]any)["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("sent %v", m)
	}
	content, _ := msgs[0].(map[string]any)["content"].([]any)
	if len(content) != 1 || content[0].(map[string]any)["text"] != "一只喜鹊" {
		t.Fatalf("sent content %v", content)
	}

	// several images come in one choice's content, each read
	d.mu.Lock()
	d.answer = dashScopeTwoImages
	d.mu.Unlock()
	code, _, raw = postImages(t, s, "/v1/images/generations", "application/json", `{"model":"qwen/qwen-image-3.0-pro","prompt":"两只喜鹊","n":2}`)
	if got := imageURLs(t, raw); code != 200 || len(got) != 2 || !strings.Contains(got[1], "/b.png") {
		t.Fatalf("%d %v: %s", code, got, raw)
	}
	params, _ = d.sent(t)["parameters"].(map[string]any)
	if params["n"] != float64(2) {
		t.Fatalf("sent %v", d.sent(t))
	}
	if _, ok := params["size"]; ok {
		t.Fatalf("sent a size with none asked: %v", params)
	}
}

// An edit's images go before the prompt in the same content, as data URLs
// or the URLs given; a mask, which DashScope doesn't take, is turned away
// before it is asked.
func TestDashScopeEdits(t *testing.T) {
	s, d := dashScoped(t, provider.Provider{ID: "qwen", Name: "Qwen", Preset: "qwen", Models: []string{"qwen3.8-max", "qwen-image-3.0-pro"}})
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("model", "qwen/qwen-image-3.0-pro")
	mw.WriteField("prompt", "make it night")
	part, _ := mw.CreateFormFile("image", "a.png")
	part.Write(pngBytes)
	mw.Close()
	code, _, raw := postImages(t, s, "/v1/images/edits", mw.FormDataContentType(), buf.String())
	if code != 200 || len(imageURLs(t, raw)) != 1 {
		t.Fatalf("%d %s", code, raw)
	}
	msgs, _ := d.sent(t)["input"].(map[string]any)["messages"].([]any)
	content, _ := msgs[0].(map[string]any)["content"].([]any)
	if len(content) != 2 || content[0].(map[string]any)["image"] != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(pngBytes) ||
		content[1].(map[string]any)["text"] != "make it night" {
		t.Fatalf("sent content %v", content)
	}

	code, _, raw = postImages(t, s, "/v1/images/edits", "application/json", `{"model":"qwen/qwen-image-3.0-pro","prompt":"merge","image":["https://example.com/a.png","https://example.com/b.png"]}`)
	msgs, _ = d.sent(t)["input"].(map[string]any)["messages"].([]any)
	content, _ = msgs[0].(map[string]any)["content"].([]any)
	if code != 200 || len(content) != 3 || content[1].(map[string]any)["image"] != "https://example.com/b.png" {
		t.Fatalf("%d %s, sent %v", code, raw, content)
	}

	before := len(d.paths)
	buf.Reset()
	mw = multipart.NewWriter(&buf)
	mw.WriteField("model", "qwen/qwen-image-3.0-pro")
	mw.WriteField("prompt", "change the sky")
	part, _ = mw.CreateFormFile("image", "a.png")
	part.Write(pngBytes)
	part, _ = mw.CreateFormFile("mask", "m.png")
	part.Write(pngBytes)
	mw.Close()
	code, _, raw = postImages(t, s, "/v1/images/edits", mw.FormDataContentType(), buf.String())
	if code != 400 || !strings.Contains(raw, "no mask") || len(d.paths) != before {
		t.Fatalf("a mask: %d %s, asked %v", code, raw, d.paths[before:])
	}
}

// DashScope's refusal, and an answer with no image, say DashScope's own
// words; a ratio is turned away with what to send.
func TestDashScopeSaysWhy(t *testing.T) {
	s, d := dashScoped(t, provider.Provider{ID: "qwen", Name: "Qwen Token Plan", Preset: "qwen-token-plan", Models: []string{"qwen3.8-max", "qwen-image-3.0-pro"}})
	d.answer, d.status = `{"request_id":"1","code":"InvalidParameter","message":"The size is not match the allowed size"}`, 400
	code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"qwen/qwen-image-3.0-pro","prompt":"a","size":"9000x9000"}`)
	if code != 400 || !strings.Contains(raw, "The size is not match the allowed size (InvalidParameter)") {
		t.Fatalf("%d %s", code, raw)
	}
	d.answer, d.status = `{"status_code":200,"request_id":"2","code":"","message":"","output":{"choices":[{"finish_reason":"content_filter","message":{"role":"assistant","content":[]}}]}}`, 0
	code, _, raw = postImages(t, s, "/v1/images/generations", "application/json", `{"model":"qwen/qwen-image-3.0-pro","prompt":"a"}`)
	if code < 400 || !strings.Contains(raw, "no image") || !strings.Contains(raw, "content_filter") {
		t.Fatalf("%d %s", code, raw)
	}
	code, _, raw = postImages(t, s, "/v1/images/generations", "application/json", `{"model":"qwen/qwen-image-3.0-pro","prompt":"a","size":"16:9"}`)
	if code != 400 || !strings.Contains(raw, "WIDTHxHEIGHT") {
		t.Fatalf("%d %s", code, raw)
	}
}

// The native API is DashScope's alone, and for its Qwen-Image and Wan 2.7
// image models: a relay serving qwen-image, or another Wan model on
// DashScope, is asked on the images API as before.
func TestDashScopeDrawsScoped(t *testing.T) {
	ds := provider.Provider{Preset: "qwen-token-plan", Chat: "https://token-plan.maas.qianwenaiapi.com/compatible-mode/v1"}
	for _, m := range []string{"qwen-image-3.0-pro", "qwen-image-edit-plus", "wan2.7-image", "wan2.7-image-pro"} {
		if !dashScopeDraws(ds, m) {
			t.Errorf("%s isn't drawn natively", m)
		}
	}
	if dashScopeDraws(ds, "wan2.2-t2i-flash") {
		t.Error("an older Wan model is drawn on the multimodal API")
	}
	if dashScopeDraws(provider.Provider{Chat: "https://relay.example.com/v1"}, "qwen-image-3.0-pro") {
		t.Error("a relay is drawn on DashScope's API")
	}
	for base, want := range map[string]string{
		"https://token-plan.maas.qianwenaiapi.com/compatible-mode/v1": "https://token-plan.maas.qianwenaiapi.com",
		"https://dashscope-intl.aliyuncs.com/api/v1":                  "https://dashscope-intl.aliyuncs.com",
		"https://dashscope.aliyuncs.com":                              "https://dashscope.aliyuncs.com",
	} {
		if got := dashScopeRoot(provider.Provider{Chat: base}); got != want {
			t.Errorf("root of %s: %s, want %s", base, got, want)
		}
	}
}

// A watermark is off unless the caller asks for one: watermark true,
// beside OpenAI's fields, goes in DashScope's parameters as it came.
func TestDashScopeTakesAskedWatermark(t *testing.T) {
	s, d := dashScoped(t, provider.Provider{ID: "qwen", Name: "Qwen Token Plan", Preset: "qwen-token-plan", Models: []string{"qwen3.8-max", "qwen-image-3.0-pro"}})
	for _, c := range []struct {
		body string
		want bool
	}{
		{`{"model":"qwen/qwen-image-3.0-pro","prompt":"一只喜鹊","watermark":true}`, true},
		{`{"model":"qwen/qwen-image-3.0-pro","prompt":"一只喜鹊","watermark":false}`, false},
		{`{"model":"qwen/qwen-image-3.0-pro","prompt":"一只喜鹊"}`, false},
	} {
		if code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", c.body); code != 200 {
			t.Fatalf("%s: %d %s", c.body, code, raw)
		}
		params, _ := d.sent(t)["parameters"].(map[string]any)
		if params["watermark"] != c.want {
			t.Fatalf("%s: sent %v", c.body, params)
		}
	}
}
