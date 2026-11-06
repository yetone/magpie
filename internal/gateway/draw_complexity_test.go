package gateway

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// GPT Image 2.6's complexity and 2.5's quality xhigh and max reach an
// images API that takes OpenAI's fields, from JSON, a form and an edit.
func TestDrawComplexityAndQualityReachTheVendor(t *testing.T) {
	s, e := easeled(t)
	code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"art/gpt-image-1","prompt":"a magpie","quality":"xhigh","complexity":"low"}`)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	sent := e.got("/v1/images/generations")
	if len(sent) != 1 || !strings.Contains(sent[0], `"complexity":"low"`) || !strings.Contains(sent[0], `"quality":"xhigh"`) {
		t.Fatalf("vendor was sent %v", sent)
	}

	form := func(path string, image bool) {
		t.Helper()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("model", "art/gpt-image-1")
		mw.WriteField("prompt", "a magpie")
		mw.WriteField("quality", "max")
		mw.WriteField("complexity", "medium")
		if image {
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", `form-data; name="image"; filename="image.png"`)
			h.Set("Content-Type", "image/png")
			part, _ := mw.CreatePart(h)
			part.Write(pngBytes)
		}
		mw.Close()
		if code, _, raw := postImages(t, s, path, mw.FormDataContentType(), buf.String()); code != 200 {
			t.Fatalf("%s: %d %s", path, code, raw)
		}
	}
	form("/v1/images/generations", false)
	if sent := e.got("/v1/images/generations"); len(sent) != 2 || !strings.Contains(sent[1], `"complexity":"medium"`) || !strings.Contains(sent[1], `"quality":"max"`) {
		t.Fatalf("form: vendor was sent %v", sent)
	}
	form("/v1/images/edits", true)
	sent = e.got("/v1/images/edits")
	if len(sent) != 1 {
		t.Fatalf("edit: vendor was sent %v", sent)
	}
	if v, ok := partNamed(t, e.ctOf("/v1/images/edits"), sent[0], "complexity"); !ok || string(v) != "medium" {
		t.Fatalf("edit's complexity %q %v", v, ok)
	}
	if v, ok := partNamed(t, e.ctOf("/v1/images/edits"), sent[0], "quality"); !ok || string(v) != "max" {
		t.Fatalf("edit's quality %q %v", v, ok)
	}

	// none given, none sent: the vendor's default stands
	postImages(t, s, "/v1/images/generations", "application/json", `{"model":"art/gpt-image-1","prompt":"a magpie"}`)
	if sent := e.got("/v1/images/generations"); len(sent) != 3 || strings.Contains(sent[2], "complexity") {
		t.Fatalf("vendor was sent %v", sent)
	}
}

// A ChatGPT account's image backend ignores complexity (tried with a real
// account: none, low and a bogus value drew alike), so its request stays
// the one Codex CLI sends.
func TestCodexDrawSendsNoComplexity(t *testing.T) {
	codexSignedIn(t)
	var mu sync.Mutex
	var bodies []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"created":1,"output_format":"png","data":[{"b64_json":"`+base64.StdEncoding.EncodeToString(pngBytes)+`"}]}`)
	}))
	defer up.Close()
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = was }()
	s := New()
	if code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"codex/gpt-image-2","prompt":"a magpie","complexity":"low"}`); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	img := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	if code, _, raw := postImages(t, s, "/v1/images/edits", "application/json", `{"model":"codex/gpt-image-2","prompt":"bluer","complexity":"low","images":[{"image_url":"`+img+`"}]}`); code != 200 {
		t.Fatalf("edit: %d %s", code, raw)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || strings.Contains(bodies[0], "complexity") || strings.Contains(bodies[1], "complexity") {
		t.Fatalf("sent %v", bodies)
	}
}
