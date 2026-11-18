package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A resolution tier goes to each vendor in its own field (skingstyle48487
// on X: a 4K asked through the image MCP came back at 2K).
func TestTierReachesEachProtocol(t *testing.T) {
	t.Run("gemini", func(t *testing.T) {
		c := imageConfigOf("gemini-3-pro-image-preview", "16:9", "4K")
		if c["imageSize"] != "4K" || c["aspectRatio"] != "16:9" {
			t.Fatalf("imageConfig %v", c)
		}
		// a model that draws at 1K alone is asked the ratio only
		if c := imageConfigOf("gemini-2.5-flash-image", "16:9", "4K"); c["imageSize"] != "" {
			t.Fatalf("2.x imageConfig %v", c)
		}
	})
	t.Run("chat", func(t *testing.T) {
		c := chatImageConfig("google/gemini-3-pro-image-preview", drawing{Size: "16:9", Tier: "4K"})
		if c["image_size"] != "4K" || c["aspect_ratio"] != "16:9" {
			t.Fatalf("image_config %v", c)
		}
		// a pixel size over 1K asked in chat is its tier, not dropped
		if c := chatImageConfig("gemini-3-pro-image-preview", drawing{Size: "2048x2048"}); c["image_size"] != "2K" || c["aspect_ratio"] != "1:1" {
			t.Fatalf("pixels' image_config %v", c)
		}
		if c := chatImageConfig("some-painter", drawing{Tier: "4K"}); c != nil {
			t.Fatalf("a model not Gemini's is sent %v", c)
		}
	})
	t.Run("openrouter", func(t *testing.T) {
		req := openRouterImagesReq("google/gemini-3-pro-image-preview", drawing{Prompt: "p", N: 1, Size: "16:9", Tier: "4K"})
		if req["resolution"] != "4K" || req["aspect_ratio"] != "16:9" || req["size"] != nil {
			t.Fatalf("sent %v", req)
		}
		// a tier as size goes as resolution, not as size
		d := drawing{Prompt: "p", N: 1, Size: "2k"}
		d.settleTier("")
		if req := openRouterImagesReq("google/gemini-3-pro-image-preview", d); req["resolution"] != "2K" || req["size"] != nil {
			t.Fatalf("tier as size: sent %v", req)
		}
	})
	t.Run("grok", func(t *testing.T) {
		if req := grokImagesReq("grok-imagine-image", drawing{Tier: "4K", Size: "16:9"}); req["resolution"] != "2k" || req["aspect_ratio"] != "16:9" {
			t.Fatalf("4K: sent %v", req)
		}
		if req := grokImagesReq("grok-imagine-image", drawing{Tier: "1K"}); req["resolution"] != "1k" {
			t.Fatalf("1K: sent %v", req)
		}
	})
	t.Run("ark", func(t *testing.T) {
		for _, c := range []struct{ model, size, tier, want string }{
			{"doubao-seedream-4-5-251128", "", "4K", "4K"},
			{"doubao-seedream-4-5-251128", "16:9", "4K", "5504x3040"},
			{"doubao-seedream-4-5-251128", "", "1K", "2K"}, // 4.5 draws at 2K at the least
			{"doubao-seedream-4-0-250828", "", "1K", "1K"},
			{"doubao-seedream-4-0-250828", "9:16", "1K", "720x1280"},
			{"doubao-seedream-5-0-pro-260628", "", "4K", "2K"}, // 5.0 pro's largest
			{"doubao-seedream-5-0-pro-260628", "16:9", "1K", "1424x800"},
			{"doubao-seedream-5-0-lite-20260812", "", "4K", "4K"},
			{"doubao-seedream-4-5-251128", "1536x1024", "", "1536x1024"},
		} {
			b, err := arkBody(c.model, drawing{Prompt: "p", N: 1, Size: c.size, Tier: c.tier})
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			json.Unmarshal(b, &m)
			if m["size"] != c.want {
				t.Errorf("%s %q at %s: size %v, want %s", c.model, c.size, c.tier, m["size"], c.want)
			}
		}
	})
	t.Run("minimax", func(t *testing.T) {
		b, _, err := minimaxH3Body("MiniMax-H3-Image", drawing{Prompt: "p", N: 1, Size: "16:9", Tier: "2K"})
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.Unmarshal(b, &m)
		if m["area"] != "2K" || m["aspect_ratio"] != "16:9" || m["size"] != nil {
			t.Fatalf("sent %v", m)
		}
	})
	t.Run("images API", func(t *testing.T) {
		p := provider.Provider{ID: "relay", Chat: "http://relay.test/v1"}
		for _, c := range []struct{ model, size, tier, want string }{
			{"gpt-image-2", "16:9", "4K", "3840x2160"},
			{"gpt-image-2", "", "4K", "2880x2880"}, // within 8,294,400 pixels
			{"gpt-image-2.5", "9:16", "2K", "1152x2048"},
			{"gpt-image-2", "21:9", "1K", "1248x544"}, // at least 655,360 pixels
			{"gpt-image-1", "16:9", "4K", "16:9"},     // its own sizes: the ratio as asked
			{"gpt-image-1.5", "4K", "4K", ""},
			{"dall-e-3", "", "2K", ""},
			{"painter", "3:2", "2K", "2048x1360"},
			{"painter", "1024x1024", "", "1024x1024"},
			{"doubao-seedream-4-5-251128", "", "4K", "4K"},
		} {
			if got := imagesAPISize(p, c.model, drawing{Size: c.size, Tier: c.tier}); got != c.want {
				t.Errorf("%s %q at %s: size %q, want %q", c.model, c.size, c.tier, got, c.want)
			}
		}
	})
}

// A tier is read from resolution, or from a tier given as size; a
// WIDTHxHEIGHT size is the size, and an unknown resolution is refused.
func TestSettleTier(t *testing.T) {
	for _, c := range []struct{ size, resolution, want string }{
		{"", "4k", "4K"},
		{"16:9", "2K", "2K"},
		{"4K", "", "4K"},
		{"4K", "auto", ""},
		{"1024x1024", "4K", ""},
		{"", "", ""},
	} {
		d := drawing{Size: c.size}
		if err := d.settleTier(c.resolution); err != nil || d.Tier != c.want {
			t.Errorf("size %q resolution %q: tier %q (%v), want %q", c.size, c.resolution, d.Tier, err, c.want)
		}
	}
	d := drawing{}
	if err := d.settleTier("8K"); err == nil {
		t.Fatal("8K taken")
	}
}

// The image MCP asks the tier the agent names, else Settings' unless the
// agent gave a size of its own.
func TestImageTierPrecedence(t *testing.T) {
	fresh(t)
	st := settings.Load()
	st.ImageResolution = "2K"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ arg, size, want string }{
		{"", "", "2K"},
		{"", "16:9", "2K"},
		{"", "1024x1024", ""},
		{"", "4K", ""}, // the size's own tier, read by the gateway
		{"4K", "", "4K"},
		{"auto", "", ""},
	} {
		if got, err := ImageTier(c.arg, c.size); err != nil || got != c.want {
			t.Errorf("resolution %q size %q: %q (%v), want %q", c.arg, c.size, got, err, c.want)
		}
	}
	if _, err := ImageTier("8K", ""); err == nil {
		t.Fatal("8K taken")
	}
	st.ImageResolution = "8K"
	if err := settings.Save(st); err == nil {
		t.Fatal("settings took 8K")
	}
}

// pngOf is a real PNG of w by h.
func pngOf(w, h int) []byte {
	var b bytes.Buffer
	png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h)))
	return b.Bytes()
}

// tierEasel is a relay's images API that turns size away when refuse says
// so, and draws a picture of px pixels otherwise.
type tierEasel struct {
	mu     sync.Mutex
	bodies []map[string]any
	refuse func(map[string]any) bool
	img    []byte
}

func (e *tierEasel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	json.Unmarshal(b, &m)
	e.mu.Lock()
	e.bodies = append(e.bodies, m)
	refuse := e.refuse != nil && e.refuse(m)
	e.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if refuse {
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"message":"Invalid size"}}`)
		return
	}
	io.WriteString(w, `{"created":1,"data":[{"b64_json":"`+base64.StdEncoding.EncodeToString(e.img)+`"}]}`)
}

func (e *tierEasel) sent() []map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]map[string]any(nil), e.bodies...)
}

func tierEaseled(t *testing.T, img []byte) (*Server, *tierEasel) {
	t.Helper()
	fresh(t)
	e := &tierEasel{img: img}
	up := httptest.NewServer(e)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: up.URL + "/v1", Key: "key", Models: []string{"gpt-image-2", "gpt-image-1"}}); err != nil {
		t.Fatal(err)
	}
	return New(), e
}

// The images API takes resolution: GPT Image 2 is asked the pixels the
// tier is at the ratio, GPT Image 1 its own size; a vendor that turns the
// tier away is asked once more without it; and an unknown tier is a 400.
func TestImagesAPIResolution(t *testing.T) {
	s, e := tierEaseled(t, pngBytes)
	if code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"relay/gpt-image-2","prompt":"a magpie","size":"16:9","resolution":"4K"}`); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if m := e.sent()[0]; m["size"] != "3840x2160" || m["resolution"] != nil {
		t.Fatalf("gpt-image-2 was sent %v", m)
	}
	if code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"relay/gpt-image-1","prompt":"a magpie","size":"4K"}`); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if m := e.sent()[1]; m["size"] != nil {
		t.Fatalf("gpt-image-1 was sent %v", m)
	}

	e.refuse = func(m map[string]any) bool { return m["size"] != nil }
	code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"relay/gpt-image-2","prompt":"a magpie","resolution":"4K"}`)
	if code != 200 {
		t.Fatalf("refused tier: %d %s", code, raw)
	}
	if sent := e.sent(); len(sent) != 4 || sent[2]["size"] != "2880x2880" || sent[3]["size"] != nil {
		t.Fatalf("sent %v", sent)
	}
	// a size the caller gave is not asked again without it
	code, _, _ = postImages(t, s, "/v1/images/generations", "application/json", `{"model":"relay/gpt-image-2","prompt":"a magpie","size":"1536x1024"}`)
	if code != 400 || len(e.sent()) != 5 {
		t.Fatalf("%d, sent %d", code, len(e.sent()))
	}

	if code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"relay/gpt-image-2","prompt":"a magpie","resolution":"8K"}`); code != 400 || !strings.Contains(raw, "1K, 2K or 4K") {
		t.Fatalf("8K: %d %s", code, raw)
	}
}

// generate_image asks Settings' resolution when the agent names no size,
// and says what size each image came back at, and when that is under the
// tier asked.
func TestImageMCPAsksTheSetResolution(t *testing.T) {
	s, e := tierEaseled(t, pngOf(2048, 1152))
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Cleanup(s.imageStore.running.Wait)
	st := settings.Load()
	st.ImageResolution = "4K"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	h := lanGuard(s.Handler())
	m := readImageReply(t, imagePost(h, "127.0.0.1:5000", imageToolCall(GenerateImageTool, map[string]any{"prompt": "a magpie", "model": "relay/gpt-image-2", "size": "16:9"})))
	if m.Result.IsError {
		t.Fatalf("drawn: %+v", m)
	}
	if got := e.sent()[0]["size"]; got != "3840x2160" {
		t.Fatalf("sent size %v", got)
	}
	if text := m.Result.Content[0].Text; !strings.Contains(text, "2048x1152 pixels, under the 4K asked") {
		t.Fatalf("said %q", text)
	}

	// a size of the agent's own is the size: no tier goes beside it
	readImageReply(t, imagePost(h, "127.0.0.1:5000", imageToolCall(GenerateImageTool, map[string]any{"prompt": "a magpie", "model": "relay/gpt-image-2", "size": "1536x1024"})))
	if got := e.sent()[1]["size"]; got != "1536x1024" {
		t.Fatalf("sent size %v", got)
	}
	// and so is one the agent names in resolution
	readImageReply(t, imagePost(h, "127.0.0.1:5000", imageToolCall(GenerateImageTool, map[string]any{"prompt": "a magpie", "model": "relay/gpt-image-2", "resolution": "1K"})))
	if got := e.sent()[2]["size"]; got != "1024x1024" {
		t.Fatalf("sent size %v", got)
	}
}
