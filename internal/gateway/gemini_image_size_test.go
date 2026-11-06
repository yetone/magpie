package gateway

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// geminiDrawn is a Gemini API's answer with one image drawn.
const geminiDrawn = `{"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3}}`

// A size an images API client asks for reaches a Gemini image model on
// Google's own API as an imageSize beside the aspect ratio (#3936): a size
// asked as pixels only became a ratio, so the model drew at its default 1K
// and no size above 1K could be asked. A size the 1K default covers is the
// aspect ratio alone, as before, and a ratio asked as one is no size.
func TestImagesAPISizeReachesTheGeminiAPI(t *testing.T) {
	fresh(t)
	var body []byte
	if err := provider.Save(provider.Provider{ID: "gg", Name: "Google", Chat: "https://generativelanguage.googleapis.com/v1beta/openai", Key: "key", Models: []string{"gemini-3-pro-image-preview", "gemini-2.5-flash-image"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if !strings.HasSuffix(r.URL.Path, ":generateContent") {
			return nil, fmt.Errorf("unexpected request to %s", r.URL)
		}
		body = b
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(geminiDrawn))}, nil
	})}
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"gg/gemini-3-pro-image-preview","prompt":"a magpie","size":"4096x4096"}`)
	if code != 200 || len(a.Data) != 1 {
		t.Fatalf("%d %s", code, raw)
	}
	if sent := gjson.GetBytes(body, "generationConfig.imageConfig").Raw; !sameConfig(sent, `{"aspectRatio":"1:1","imageSize":"4K"}`) {
		t.Fatalf("size 4096x4096: imageConfig was %s: %s", sent, body)
	}
	code, _, raw = postImages(t, s, "/v1/images/generations", "application/json", `{"model":"gg/gemini-3-pro-image-preview","prompt":"a magpie","size":"1536x1024"}`)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if sent := gjson.GetBytes(body, "generationConfig.imageConfig").Raw; !sameConfig(sent, `{"aspectRatio":"3:2","imageSize":"2K"}`) {
		t.Fatalf("size 1536x1024: imageConfig was %s: %s", sent, body)
	}
	// a size the model's 1K default draws at is the ratio alone, as before
	code, _, raw = postImages(t, s, "/v1/images/generations", "application/json", `{"model":"gg/gemini-3-pro-image-preview","prompt":"a magpie","size":"1024x1024"}`)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if sent := gjson.GetBytes(body, "generationConfig.imageConfig").Raw; !sameConfig(sent, `{"aspectRatio":"1:1"}`) {
		t.Fatalf("size 1024x1024: imageConfig was %s: %s", sent, body)
	}
	// a Gemini 2.x image model draws at 1K only: the ratio alone, as before
	code, _, raw = postImages(t, s, "/v1/images/generations", "application/json", `{"model":"gg/gemini-2.5-flash-image","prompt":"a magpie","size":"4096x4096"}`)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if sent := gjson.GetBytes(body, "generationConfig.imageConfig").Raw; !sameConfig(sent, `{"aspectRatio":"1:1"}`) {
		t.Fatalf("gemini-2.5-flash-image 4096x4096: imageConfig was %s: %s", sent, body)
	}
	// a ratio asked as one is no size
	code, _, raw = postImages(t, s, "/v1/images/generations", "application/json", `{"model":"gg/gemini-3-pro-image-preview","prompt":"a magpie","size":"16:9"}`)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if sent := gjson.GetBytes(body, "generationConfig.imageConfig").Raw; !sameConfig(sent, `{"aspectRatio":"16:9"}`) {
		t.Fatalf("size 16:9: imageConfig was %s: %s", sent, body)
	}
}

// The same reaches a Google sign-in's image model (#3936): the images API's
// size becomes the imageConfig a Gemini client's own arrives in
// (TestGeminiImageConfigReachesCodeAssist), inside Code Assist's envelope
// too. None asked, none sent, as before.
func TestImagesAPISizeReachesCodeAssist(t *testing.T) {
	rec := &asksKept{}
	s, _ := antigravityGroupFor(t, []string{"u@example.com"}, agDrawer, func(w http.ResponseWriter, r *http.Request) {
		rec.keep(r)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(drawnPNG))
	})
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"antigravity/`+agDrawer+`","prompt":"a cat","size":"4096x4096"}`)
	if code != 200 || len(a.Data) != 1 {
		t.Fatalf("%d %s", code, raw)
	}
	got, _ := rec.last()
	if sent := gjson.GetBytes(got.body, "request.generationConfig.imageConfig").Raw; !sameConfig(sent, `{"aspectRatio":"1:1","imageSize":"4K"}`) {
		t.Fatalf("size 4096x4096: imageConfig was %s: %s", sent, got.body)
	}
	code, _, raw = postImages(t, s, "/v1/images/generations", "application/json", `{"model":"antigravity/`+agDrawer+`","prompt":"a cat"}`)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	got, _ = rec.last()
	if sent := gjson.GetBytes(got.body, "request.generationConfig.imageConfig").Raw; sent != "" {
		t.Fatalf("no size asked: imageConfig was %s", sent)
	}
}
