package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
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

// senseNova is token.sensenova.cn as its docs describe it: an OpenAI
// model list, /images/generations in JSON, /images/edits in JSON alone
// with the images as [{"image_url": "data:…"}] (a multipart form is
// turned away), and a link unless b64_json is asked for.
type senseNova struct {
	mu   sync.Mutex
	sent map[string][]string
}

func (v *senseNova) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	v.mu.Lock()
	if v.sent == nil {
		v.sent = map[string][]string{}
	}
	v.sent[r.URL.Path] = append(v.sent[r.URL.Path], string(body))
	v.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v1/models":
		io.WriteString(w, `{"object":"list","data":[{"id":"sensenova-6.8-flash-lite","object":"model"},{"id":"sensenova-u1.5-lite","object":"model"}]}`)
	case "/v1/images/generations", "/v1/images/edits":
		var req struct {
			Format string `json:"response_format"`
			Images []struct {
				URL string `json:"image_url"`
			} `json:"images"`
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") || json.Unmarshal(body, &req) != nil {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"invalid request body"}}`)
			return
		}
		if r.URL.Path == "/v1/images/edits" && (len(req.Images) == 0 || !strings.HasPrefix(req.Images[0].URL, "data:image/")) {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"images is required"}}`)
			return
		}
		if req.Format != "b64_json" {
			io.WriteString(w, `{"created":1,"data":[{"url":"https://sensenova.invalid/out.png"}]}`)
			return
		}
		io.WriteString(w, `{"created":1,"data":[{"b64_json":"`+base64.StdEncoding.EncodeToString(pngBytes)+`"}]}`)
	default:
		w.WriteHeader(404)
	}
}

func (v *senseNova) got(path string) []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.sent[path]...)
}

// SenseNova's sensenova-u1.5-lite, listed by its own /models beside its
// chat models, is an image model: Settings' image generation picker lists
// it, and it draws and edits on SenseNova's images API in the shape that
// API takes, with no setting of the user's (lileo0668 on Discord).
func TestSenseNovaU1Draws(t *testing.T) {
	fresh(t)
	v := &senseNova{}
	up := httptest.NewServer(v)
	t.Cleanup(up.Close)
	base := up.URL + "/v1"
	ms, err := catalog.Fetch(context.Background(), base, "key", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("sensenova", base, ms); err != nil {
		t.Fatal(err)
	}
	p := provider.Provider{ID: "sensenova", Name: "SenseNova", Chat: base, Key: "key"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	var drawers []string
	for _, m := range Drawers(p) {
		drawers = append(drawers, m.ID)
	}
	if !slices.Equal(drawers, []string{"sensenova-u1.5-lite"}) {
		t.Fatalf("drawers %v, want sensenova-u1.5-lite alone", drawers)
	}
	s := New()
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"sensenova/sensenova-u1.5-lite","prompt":"a magpie","size":"1024x1024"}`)
	if code != 200 || len(a.Data) != 1 {
		t.Fatalf("%d %s", code, raw)
	}
	if b, _ := base64.StdEncoding.DecodeString(a.Data[0].B64); string(b) != string(pngBytes) {
		t.Fatalf("image %q", b)
	}
	if sent := v.got("/v1/chat/completions"); len(sent) != 0 {
		t.Fatalf("an image model was asked on chat: %v", sent)
	}
	img := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	code, a, raw = postImages(t, s, "/v1/images/edits", "application/json", `{"model":"sensenova/sensenova-u1.5-lite","prompt":"bluer","images":[{"image_url":"`+img+`"}]}`)
	if code != 200 || len(a.Data) != 1 {
		t.Fatalf("edit: %d %s", code, raw)
	}
	if sent := v.got("/v1/images/edits"); len(sent) != 1 || !strings.Contains(sent[0], `"images":[{"image_url":"data:image/png;base64,`) {
		t.Fatalf("edit was sent %v", sent)
	}
}

// The SenseNova plugin (aqiu817/magpie-sensenova), in front of
// token.sensenova.cn/v1 on @ai-sdk/openai-compatible, lists
// sensenova-u1.5-lite from SenseNova's /models: Settings' image
// generation picker lists it, and it draws and edits on SenseNova's images
// API through the plugin's fetch, in the shape TestSenseNovaU1Draws holds
// a key to. An image model the plugin speaks on another vendor's package
// isn't listed, as no images API sits under its base (01huadalang on
// Discord).
func TestSenseNovaPluginDraws(t *testing.T) {
	t.Setenv("FAKE_SENSENOVA", "1")
	v := &senseNova{}
	pid := besideFake(t, "sensenova", v)
	p, err := provider.Find(pid)
	if err != nil {
		t.Fatal(err)
	}
	var drawers []string
	for _, m := range Drawers(*p) {
		if m.Provider != pid {
			t.Fatalf("drawer %s of %q", m.ID, m.Provider)
		}
		drawers = append(drawers, m.ID)
	}
	if !slices.Equal(drawers, []string{"sensenova-u1.5-lite"}) {
		t.Fatalf("drawers %v, want sensenova-u1.5-lite alone", drawers)
	}
	s := New()
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"`+pid+`/sensenova-u1.5-lite","prompt":"a magpie","size":"1024x1024"}`)
	if code != 200 || len(a.Data) != 1 {
		t.Fatalf("%d %s", code, raw)
	}
	if b, _ := base64.StdEncoding.DecodeString(a.Data[0].B64); string(b) != string(pngBytes) {
		t.Fatalf("image %q", b)
	}
	if sent := v.got("/v1/images/generations"); len(sent) != 1 || !strings.Contains(sent[0], `"model":"sensenova-u1.5-lite"`) || !strings.Contains(sent[0], `"response_format":"b64_json"`) {
		t.Fatalf("generation was sent %v", sent)
	}
	if sent := v.got("/v1/chat/completions"); len(sent) != 0 {
		t.Fatalf("an image model was asked on chat: %v", sent)
	}
	img := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	code, a, raw = postImages(t, s, "/v1/images/edits", "application/json", `{"model":"`+pid+`/sensenova-u1.5-lite","prompt":"bluer","images":[{"image_url":"`+img+`"}]}`)
	if code != 200 || len(a.Data) != 1 {
		t.Fatalf("edit: %d %s", code, raw)
	}
	if sent := v.got("/v1/images/edits"); len(sent) != 1 || !strings.Contains(sent[0], `"images":[{"image_url":"data:image/png;base64,`) {
		t.Fatalf("edit was sent %v", sent)
	}
}

// A plugin's model that says it gives images alone (modalities.output
// ["image"]) is an image model whatever its name: Settings' picker lists
// it, and it draws on the images API through the plugin's fetch, never on
// chat. One that answers in text and images stays a chat model
// (maonekone on Discord).
func TestPluginDeclaredDrawerDraws(t *testing.T) {
	t.Setenv("FAKE_DECLARED_DRAWS", "1")
	v := &senseNova{}
	pid := besideFake(t, "sensenova", v)
	p, err := provider.Find(pid)
	if err != nil {
		t.Fatal(err)
	}
	var drawers []string
	for _, m := range Drawers(*p) {
		drawers = append(drawers, m.ID)
	}
	if !slices.Equal(drawers, []string{"jimeng-4.0"}) {
		t.Fatalf("drawers %v, want jimeng-4.0 alone", drawers)
	}
	s := New()
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"`+pid+`/jimeng-4.0","prompt":"a magpie","size":"1024x1024"}`)
	if code != 200 || len(a.Data) != 1 {
		t.Fatalf("%d %s", code, raw)
	}
	if sent := v.got("/v1/images/generations"); len(sent) != 1 || !strings.Contains(sent[0], `"model":"jimeng-4.0"`) {
		t.Fatalf("generation was sent %v", sent)
	}
	if sent := v.got("/v1/chat/completions"); len(sent) != 0 {
		t.Fatalf("an image model was asked on chat: %v", sent)
	}
}

// senseNovaKeyed is SenseNova added with a key in front of v.
func senseNovaKeyed(t *testing.T, v *senseNova) *Server {
	t.Helper()
	fresh(t)
	up := httptest.NewServer(v)
	t.Cleanup(up.Close)
	base := up.URL + "/v1"
	ms, err := catalog.Fetch(context.Background(), base, "key", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("sensenova", base, ms); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "sensenova", Name: "SenseNova", Chat: base, Key: "key"}); err != nil {
		t.Fatal(err)
	}
	return New()
}

// sentJSON is the last body v was sent at path, as JSON.
func (v *senseNova) sentJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	sent := v.got(path)
	if len(sent) == 0 {
		t.Fatalf("nothing was sent to %s", path)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(sent[len(sent)-1]), &m); err != nil {
		t.Fatalf("%s was sent %q: %v", path, sent[len(sent)-1], err)
	}
	return m
}

// SenseNova's watermark switch and prompt_extend, which OpenAI's images
// API hasn't, go to SenseNova as the caller set them, on a generation and
// on an edit, JSON or a form; a caller's response_format url is asked as
// url and answered by SenseNova's link (justforlxz on X: "sensenova的生图
// 传递watermark开关无效，response_format 参数也不认").
func TestSenseNovaTakesWatermarkAndFormat(t *testing.T) {
	v := &senseNova{}
	s := senseNovaKeyed(t, v)

	code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"sensenova/sensenova-u1.5-lite","prompt":"a magpie","watermark":false,"prompt_extend":false,"response_format":"url"}`)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	m := v.sentJSON(t, "/v1/images/generations")
	if m["watermark"] != false || m["prompt_extend"] != false || m["response_format"] != "url" {
		t.Fatalf("generation was sent %v", m)
	}
	if got := imageURLs(t, raw); len(got) != 1 || got[0] != "https://sensenova.invalid/out.png" {
		t.Fatalf("asked for url, answered %s", raw)
	}

	// nothing asked: SenseNova's own watermark default, and the image itself
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"sensenova/sensenova-u1.5-lite","prompt":"a magpie"}`)
	if code != 200 || len(a.Data) != 1 || a.Data[0].B64 == "" {
		t.Fatalf("%d %s", code, raw)
	}
	m = v.sentJSON(t, "/v1/images/generations")
	if _, ok := m["watermark"]; ok || m["response_format"] != "b64_json" {
		t.Fatalf("generation was sent %v", m)
	}

	img := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	code, _, raw = postImages(t, s, "/v1/images/edits", "application/json", `{"model":"sensenova/sensenova-u1.5-lite","prompt":"bluer","watermark":true,"response_format":"url","images":[{"image_url":"`+img+`"}]}`)
	if code != 200 {
		t.Fatalf("edit: %d %s", code, raw)
	}
	m = v.sentJSON(t, "/v1/images/edits")
	if m["watermark"] != true || m["response_format"] != "url" {
		t.Fatalf("edit was sent %v", m)
	}
	if got := imageURLs(t, raw); len(got) != 1 || got[0] != "https://sensenova.invalid/out.png" {
		t.Fatalf("edit asked for url, answered %s", raw)
	}

	// a form's fields are text: "false" goes as the bool SenseNova takes
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("model", "sensenova/sensenova-u1.5-lite")
	mw.WriteField("prompt", "bluer")
	mw.WriteField("watermark", "false")
	fw, _ := mw.CreateFormFile("image", "a.png")
	fw.Write(pngBytes)
	mw.Close()
	code, a, raw = postImages(t, s, "/v1/images/edits", mw.FormDataContentType(), buf.String())
	if code != 200 || len(a.Data) != 1 || a.Data[0].B64 == "" {
		t.Fatalf("form edit: %d %s", code, raw)
	}
	m = v.sentJSON(t, "/v1/images/edits")
	if m["watermark"] != false || m["response_format"] != "b64_json" {
		t.Fatalf("form edit was sent %v", m)
	}
}

// dall-e answers in a link unless asked otherwise, as SenseNova's U1
// does: a caller asking for url is answered by the link, not the bytes.
func TestDallEKeepsAskedURL(t *testing.T) {
	fresh(t)
	v := &senseNova{}
	up := httptest.NewServer(v)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "art", Name: "Art", Chat: up.URL + "/v1", Key: "key", Models: []string{"text", "dall-e-3"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("art", up.URL+"/v1", []catalog.Model{{ID: "text"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"art/dall-e-3","prompt":"a magpie","response_format":"url"}`)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if m := v.sentJSON(t, "/v1/images/generations"); m["response_format"] != "url" {
		t.Fatalf("sent %v", m)
	}
	if got := imageURLs(t, raw); len(got) != 1 || got[0] != "https://sensenova.invalid/out.png" {
		t.Fatalf("asked for url, answered %s", raw)
	}
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"art/dall-e-3","prompt":"a magpie"}`)
	if code != 200 || len(a.Data) != 1 || a.Data[0].B64 == "" {
		t.Fatalf("%d %s", code, raw)
	}
	if m := v.sentJSON(t, "/v1/images/generations"); m["response_format"] != "b64_json" {
		t.Fatalf("sent %v", m)
	}
}
