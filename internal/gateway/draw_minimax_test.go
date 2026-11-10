package gateway

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// minimaxImages stands in for a MiniMax host as its image_generation API
// is documented (platform.minimax.io/docs/api-reference/image-generation-
// t2i, platform.minimax.cn the same): POST /v1/image_generation answering
// data.image_base64 and base_resp, and, as the reporter's minimax-cn key
// was answered, chat completions refusing image-01 with 2013. No real
// MiniMax account was asked.
type minimaxImages struct {
	mu     sync.Mutex
	paths  []string
	bodies []string
	answer string // what image_generation answers, one image if ""
}

func (f *minimaxImages) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.Path)
	f.bodies = append(f.bodies, string(body))
	answer := f.answer
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer key" {
		io.WriteString(w, `{"base_resp":{"status_code":1004,"status_msg":"login fail: Please carry the API secret key in the 'Authorization' field of the request header"}}`)
		return
	}
	switch r.URL.Path {
	case "/v1/image_generation":
		if answer == "" {
			answer = `{"id":"03ff3cd0820949eb8a410056b5f21d38","data":{"image_base64":["` + base64.StdEncoding.EncodeToString(pngBytes) + `"]},"metadata":{"failed_count":"0","success_count":"1"},"base_resp":{"status_code":0,"status_msg":"success"}}`
		}
		io.WriteString(w, answer)
	case "/v1/chat/completions":
		w.WriteHeader(400)
		io.WriteString(w, `{"type":"error","error":{"type":"bad_request_error","message":"invalid params, unknown model 'image-01' (2013)","http_code":"400"},"request_id":"1"}`)
	default:
		w.WriteHeader(404)
	}
}

func (f *minimaxImages) sent(t *testing.T) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) == 0 {
		t.Fatal("nothing sent")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(f.bodies[len(f.bodies)-1]), &m); err != nil {
		t.Fatalf("sent %s: %v", f.bodies[len(f.bodies)-1], err)
	}
	return m
}

// minimaxDrawing saves a MiniMax key from preset pointed at a fake host.
func minimaxDrawing(t *testing.T, preset string) (*Server, *minimaxImages, provider.Provider) {
	t.Helper()
	fresh(t)
	f := &minimaxImages{}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	was := isMiniMaxHost
	t.Cleanup(func() { isMiniMaxHost = was })
	host := strings.TrimPrefix(up.URL, "http://")
	isMiniMaxHost = func(h string) bool { return h == host || was(h) }
	p, err := provider.FromPreset(preset)
	if err != nil {
		t.Fatal(err)
	}
	p.ID, p.Key = preset, "key"
	p.Chat, p.Anthropic = up.URL+"/v1", up.URL+"/anthropic"
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return New(), f, p
}

// rocbomb123 on X: minimax-cn/image-01 went to chat completions, which
// answered "invalid params, unknown model 'image-01' (2013)". A MiniMax
// key, intl and CN alike, draws image-01 on its image_generation API, its
// size as an aspect ratio, its image read from data.image_base64.
func TestMiniMaxDrawsOnImageGeneration(t *testing.T) {
	for _, preset := range []string{"minimax-cn", "minimax"} {
		t.Run(preset, func(t *testing.T) {
			s, f, p := minimaxDrawing(t, preset)
			var ids []string
			for _, m := range Drawers(p) {
				ids = append(ids, m.ID)
			}
			if !strings.Contains(","+strings.Join(ids, ",")+",", ",image-01,image-01-live,") {
				t.Fatalf("%s draws with %v", preset, ids)
			}
			if len(catalog.Chat(minimaxDrawers(p))) != 0 {
				t.Fatal("image-01 is a chat model")
			}
			code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"`+preset+`/image-01","prompt":"a cat","n":1,"size":"1024x1024"}`)
			if code != 200 || len(a.Data) != 1 {
				t.Fatalf("%d %s (asked %v)", code, raw, f.paths)
			}
			if got, _ := base64.StdEncoding.DecodeString(a.Data[0].B64); string(got) != string(pngBytes) {
				t.Fatalf("image %q", got)
			}
			if strings.Join(f.paths, ",") != "/v1/image_generation" {
				t.Fatalf("asked %v", f.paths)
			}
			m := f.sent(t)
			if m["model"] != "image-01" || m["prompt"] != "a cat" || m["aspect_ratio"] != "1:1" || m["response_format"] != "base64" || m["n"] != float64(1) {
				t.Fatalf("sent %v", m)
			}
			if _, ok := m["size"]; ok {
				t.Fatalf("sent OpenAI's size: %v", m)
			}
		})
	}
}

// A size is a ratio MiniMax lists, or what it draws for one, else
// image-01's own width and height, else the nearest ratio; a portrait to
// draw from goes as subject_reference; what it can't take is said first.
func TestMiniMaxDrawingFields(t *testing.T) {
	for _, c := range []struct{ model, size, want string }{
		{"image-01", "16:9", `{"aspect_ratio":"16:9"}`},
		{"image-01", "720x1280", `{"aspect_ratio":"9:16"}`},
		{"image-01", "1536x1024", `{"height":1024,"width":1536}`},
		{"image-01", "4096x4096", `{"aspect_ratio":"1:1"}`},
		{"image-01-live", "1344x576", `{"aspect_ratio":"16:9"}`},
		{"image-01-live", "1536x1024", `{"aspect_ratio":"3:2"}`},
		{"image-01", "auto", `null`},
		{"image-01", "", `null`},
	} {
		b, _ := json.Marshal(minimaxShape(c.model, c.size))
		if string(b) != c.want {
			t.Errorf("%s %s: %s, want %s", c.model, c.size, b, c.want)
		}
	}

	s, f, _ := minimaxDrawing(t, "minimax-cn")
	code, _, raw := postImages(t, s, "/v1/images/edits", "application/json", `{"model":"minimax-cn/image-01","prompt":"in a garden","image":["https://example.com/me.jpg"],"seed":7,"prompt_optimizer":true}`)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	m := f.sent(t)
	refs, _ := m["subject_reference"].([]any)
	if len(refs) != 1 || refs[0].(map[string]any)["type"] != "character" || refs[0].(map[string]any)["image_file"] != "https://example.com/me.jpg" ||
		m["seed"] != float64(7) || m["prompt_optimizer"] != true {
		t.Fatalf("sent %v", m)
	}

	code, _, raw = postImages(t, s, "/v1/images/generations", "application/json", `{"model":"minimax-cn/image-01","prompt":"cats","n":3,"size":"21:9"}`)
	if m = f.sent(t); code != 200 || m["n"] != float64(3) || m["aspect_ratio"] != "21:9" {
		t.Fatalf("%d %s, sent %v", code, raw, m)
	}
}

// MiniMax says a failure in a 200's base_resp; it is told for what it is,
// and an answer whose images were all held back says so.
func TestMiniMaxDrawingErrors(t *testing.T) {
	s, f, _ := minimaxDrawing(t, "minimax")
	for _, c := range []struct {
		answer, want string
		code         int
	}{
		{`{"base_resp":{"status_code":1008,"status_msg":"insufficient balance"}}`, "out of balance", 402},
		{`{"base_resp":{"status_code":1026,"status_msg":"input new_sensitive"}}`, "sensitive", 400},
		{`{"base_resp":{"status_code":2013,"status_msg":"invalid params, aspect_ratio"}}`, "invalid params, aspect_ratio (2013)", 400},
		{`{"data":{"image_base64":[]},"metadata":{"failed_count":"1","success_count":"0"},"base_resp":{"status_code":0,"status_msg":"success"}}`, "held back 1", 400},
	} {
		f.mu.Lock()
		f.answer = c.answer
		f.mu.Unlock()
		code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"minimax/image-01","prompt":"a cat"}`)
		if code != c.code || !strings.Contains(raw, c.want) {
			t.Errorf("%s: %d %s", c.answer, code, raw)
		}
	}
}

// A relay serving MiniMax's models under another host is not MiniMax's
// API: image-01 there is asked as before.
func TestMiniMaxDrawingOnlyAtMiniMax(t *testing.T) {
	p := provider.Provider{ID: "relay", Chat: "https://relay.example.com/minimax/v1", Key: "k"}
	if drawsMiniMax(p, "image-01") {
		t.Fatal("a relay draws on MiniMax's API")
	}
	if !drawsMiniMax(provider.Provider{ID: "minimax-cn", Chat: "https://api.minimaxi.com/v1", Key: "k"}, "image-01") ||
		!drawsMiniMax(provider.Provider{ID: "mm", Chat: "https://api.minimax.cn/v1", Key: "k"}, "image-01-live") ||
		drawsMiniMax(provider.Provider{ID: "minimax", Chat: "https://api.minimax.io/v1", Key: "k"}, "MiniMax-M3") {
		t.Fatal("drawsMiniMax")
	}
}
