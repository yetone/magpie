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

// AI Studio's OpenAI-compatible /models names each model models/{id}, and
// an image model listed so was drawn at /v1beta/models/models/{id}, which
// the Gemini API answered 404 "models/nano-banana-2.1 is not found" (Dani
// Ariyanto on Discord: google/models/gemini-nano-banana-2.1, the image
// model Automatic picked). It is drawn at /v1beta/models/{id}, as its
// chat already was (GeminiPath), and its imageConfig is read from the id
// without models/. A Lite image model, which answers imageSize 2K with
// 400 "Image size 2K is not supported for this model", is sent the ratio
// alone.
func TestGeminiImageModelListedWithModelsPrefix(t *testing.T) {
	fresh(t)
	var path string
	var body []byte
	if err := provider.Save(provider.Provider{ID: "gg", Name: "Google", Chat: "https://generativelanguage.googleapis.com/v1beta/openai", Key: "key",
		Models: []string{"models/gemini-nano-banana-2.1", "models/gemini-3-pro-image-preview", "models/gemini-3.1-flash-lite-image", "gemini-3.1-flash-lite-image"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		path, body = r.URL.Path, b
		if strings.Contains(r.URL.Path, "/models/models/") {
			return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(`{"error":{"code":404,"message":"models/nano-banana-2.1 is not found for API version v1beta, or is not supported for generateContent.","status":"NOT_FOUND"}}`))}, nil
		}
		if !strings.HasSuffix(r.URL.Path, ":generateContent") {
			return nil, fmt.Errorf("unexpected request to %s", r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(geminiDrawn))}, nil
	})}
	draw := func(model, size string) {
		t.Helper()
		req := `{"model":"gg/` + model + `","prompt":"a magpie"`
		if size != "" {
			req += `,"size":"` + size + `"`
		}
		code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", req+"}")
		if code != 200 || len(a.Data) != 1 {
			t.Fatalf("%s: %d %s (asked %s)", model, code, raw, path)
		}
	}

	draw("models/gemini-nano-banana-2.1", "")
	if want := "/v1beta/models/gemini-nano-banana-2.1:generateContent"; path != want {
		t.Fatalf("drawn at %s, want %s", path, want)
	}

	// models/ hides nothing of the model: a Gemini 3 image model still
	// takes the size past 1K
	draw("models/gemini-3-pro-image-preview", "1536x1024")
	if sent := gjson.GetBytes(body, "generationConfig.imageConfig").Raw; !sameConfig(sent, `{"aspectRatio":"3:2","imageSize":"2K"}`) {
		t.Fatalf("pro 1536x1024: imageConfig was %s", sent)
	}

	// Nano Banana 2 Lite draws at 1K alone: the ratio, no imageSize, under
	// either spelling
	for _, m := range []string{"gemini-3.1-flash-lite-image", "models/gemini-3.1-flash-lite-image"} {
		draw(m, "1024x1536")
		if sent := gjson.GetBytes(body, "generationConfig.imageConfig").Raw; !sameConfig(sent, `{"aspectRatio":"2:3"}`) {
			t.Fatalf("%s 1024x1536: imageConfig was %s", m, sent)
		}
	}
}

// OpenRouter's Image API is sent no resolution for a model that draws at
// 1K alone, and one for any other model asked past 1K, as before.
func TestOpenRouterSizeOfALiteImageModel(t *testing.T) {
	for _, c := range []struct{ model, res string }{
		{"google/gemini-3.1-flash-lite-image", ""},
		{"google/gemini-3-pro-image-preview", "2K"},
		{"sourceful/riverflow-v2", "2K"},
	} {
		req := map[string]any{}
		openRouterSize(req, c.model, "1024x1536")
		got, _ := req["resolution"].(string)
		if got != c.res || req["aspect_ratio"] != "2:3" {
			t.Errorf("%s: %v, want resolution %q beside 2:3", c.model, req, c.res)
		}
	}
}
