package gateway

import (
	"encoding/base64"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// loomy is iFlytek's Loomy gateway (#4913): an OpenAI model list naming
// three image generators beside its chat models, and an images API all
// three answer in OpenAI's shape (measured live: 200 with a URL each).
type loomy struct {
	mu   sync.Mutex
	sent map[string][]string
}

func (v *loomy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
		io.WriteString(w, `{"object":"list","data":[{"id":"Hy-Image-3.5-preview","object":"model"},{"id":"doubao-seedream-5-lite","object":"model"},{"id":"qwen-image-3.0-pro","object":"model"},{"id":"spark-lite-max","object":"model"}]}`)
	case "/v1/images/generations":
		io.WriteString(w, `{"created":1,"data":[{"b64_json":"`+base64.StdEncoding.EncodeToString(pngBytes)+`"}]}`)
	default:
		w.WriteHeader(404)
	}
}

func (v *loomy) got(path string) []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.sent[path]...)
}

// The Loomy plugin lists all three image generators its gateway serves
// (#4913): qwen-image and seedream draw on the images API as any such
// model does, and Hy-Image-3.5-preview, named for images only by
// "hy-image", draws there too — not left a chat model with no way to
// pick it for drawing.
func TestLoomyPluginDraws(t *testing.T) {
	t.Setenv("FAKE_LOOMY", "1")
	v := &loomy{}
	pid := besideFake(t, "loomy", v)
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
	slices.Sort(drawers)
	if want := []string{"Hy-Image-3.5-preview", "doubao-seedream-5-lite", "qwen-image-3.0-pro"}; !slices.Equal(drawers, want) {
		t.Fatalf("drawers %v, want %v", drawers, want)
	}
	s := New()
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"`+pid+`/Hy-Image-3.5-preview","prompt":"a magpie","size":"1024x1024"}`)
	if code != 200 || len(a.Data) != 1 {
		t.Fatalf("%d %s", code, raw)
	}
	if b, _ := base64.StdEncoding.DecodeString(a.Data[0].B64); string(b) != string(pngBytes) {
		t.Fatalf("image %q", b)
	}
	if sent := v.got("/v1/images/generations"); len(sent) != 1 || !strings.Contains(sent[0], `"model":"Hy-Image-3.5-preview"`) {
		t.Fatalf("generation was sent %v", sent)
	}
	if sent := v.got("/v1/chat/completions"); len(sent) != 0 {
		t.Fatalf("an image model was asked on chat: %v", sent)
	}
}
