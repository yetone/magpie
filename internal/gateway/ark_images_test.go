package gateway

import (
	"bytes"
	"mime/multipart"
	"strings"
	"testing"
)

// Ark's images fields go as they came: a group with
// sequential_image_generation, optimize_prompt_options, a watermark the
// caller asks for, and an answer as Ark's URL when the caller asks for url.
func TestArkImagesPassOnTheirOwnFields(t *testing.T) {
	s, a := arked(t)
	body := `{"model":"ark/doubao-seedream-4-5-251128","prompt":"四季","sequential_image_generation":"auto","sequential_image_generation_options":{"max_images":4},
		"optimize_prompt_options":{"mode":"fast"},"watermark":true,"tools":[{"type":"web_search"}],"response_format":"url"}`
	if code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", body); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	m := a.sent(t)[0]
	opts, _ := m["sequential_image_generation_options"].(map[string]any)
	opt, _ := m["optimize_prompt_options"].(map[string]any)
	tools, _ := m["tools"].([]any)
	if m["sequential_image_generation"] != "auto" || opts["max_images"] != float64(4) || opt["mode"] != "fast" || m["watermark"] != true ||
		len(tools) != 1 || m["response_format"] != "url" {
		t.Fatalf("sent %v", m)
	}

	// a form's fields are typed as Ark takes them
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("model", "ark/doubao-seedream-4-5-251128")
	mw.WriteField("prompt", "四季")
	mw.WriteField("watermark", "true")
	mw.WriteField("sequential_image_generation", "auto")
	mw.Close()
	if code, _, raw := postImages(t, s, "/v1/images/generations", mw.FormDataContentType(), buf.String()); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if m := a.sent(t)[1]; m["watermark"] != true || m["sequential_image_generation"] != "auto" {
		t.Fatalf("sent %v", m)
	}

	// a stream is Ark's events, which this route doesn't give
	code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"ark/doubao-seedream-4-5-251128","prompt":"四季","stream":true}`)
	if code != 400 || !strings.Contains(raw, NativePath) {
		t.Fatalf("%d %s", code, raw)
	}
}
