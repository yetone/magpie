package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"strings"
	"testing"
)

func gifData(t *testing.T) string {
	t.Helper()
	im := image.NewPaletted(image.Rect(0, 0, 4, 4), color.Palette{color.Black, color.White})
	var b bytes.Buffer
	if err := gif.Encode(&b, im, nil); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

// fwxkogaia on X: a Claude Desktop conversation on gemini-4-carbon was
// turned away with "Request contains an invalid argument." Gemini reads
// PNG, JPEG, WebP, HEIC and HEIF; a GIF or an SVG in the history failed
// every turn after it.
func TestGeminiRequestHasOnlyImageTypesGeminiReads(t *testing.T) {
	svg := base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))
	r := &Request{Messages: []Message{
		{Role: "user", Parts: []Part{
			{Kind: Text, Text: "look"},
			{Kind: Image, MediaType: "image/gif", Data: gifData(t)},
			{Kind: Image, MediaType: "image/svg+xml", Data: svg},
			{Kind: Image, MediaType: "", Data: gifData(t)},
			{Kind: ToolCall, ID: "t1", Name: "Read", Args: json.RawMessage(`{}`)},
		}},
		{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "t1", Text: "ok", Images: []Part{{MediaType: "image/gif", Data: gifData(t)}}}}},
	}}
	for _, agent := range []string{"antigravity", "gemini"} {
		body := string(buildCodeAssist(r, "gemini-4-carbon", agent))
		for _, bad := range []string{`"mimeType":"image/gif"`, `"mimeType":"image/svg+xml"`, `"mimeType":""`} {
			if strings.Contains(body, bad) {
				t.Errorf("%s: request carries %s", agent, bad)
			}
		}
		if !strings.Contains(body, `"mimeType":"image/png"`) || !strings.Contains(body, "image of type image/svg+xml was left out") {
			t.Errorf("%s: GIF not as PNG or SVG not noted: %s", agent, body)
		}
	}
	var pngOK bool
	if m := geminiInline("image/gif", gifData(t))["inlineData"].(map[string]any); m["mimeType"] == "image/png" {
		raw, _ := base64.StdEncoding.DecodeString(m["data"].(string))
		_, err := png.Decode(bytes.NewReader(raw))
		pngOK = err == nil
	}
	if !pngOK {
		t.Error("GIF not written as a readable PNG")
	}
	// what Gemini reads, and files that aren't images, go as they are
	for _, mt := range []string{"image/png", "image/jpeg", "image/webp", "image/heic", "application/pdf"} {
		if m := geminiInline(mt, "AAAA")["inlineData"].(map[string]any); m["mimeType"] != mt || m["data"] != "AAAA" {
			t.Errorf("%s changed: %v", mt, m)
		}
	}
}
