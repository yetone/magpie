package gateway

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"image"
	"image/png"
	"net/http"
	"strings"
)

// geminiImageTypes are the image types Gemini reads, as Antigravity's
// model list gives them for Gemini 4 (supportedMimeTypes: png, jpeg,
// webp, heic, heif). Any other type in a request turns the whole call away
// with "Request contains an invalid argument." (fwxkogaia on X: Claude
// Desktop → gemini-4-carbon), and a history keeps the image, so every
// later turn is turned away too.
var geminiImageTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/webp": true, "image/heic": true, "image/heif": true,
}

// geminiInline is an image or file as the part Gemini takes it in, or the
// text that says it was left out. An image of a type Gemini doesn't read
// goes as a PNG when Go can read it (a GIF's first frame), a type that says
// nothing is named by its bytes, and one that can't be read is a note, not
// a refusal of the turn. Files that aren't images go as they are.
func geminiInline(mediaType, data string) map[string]any {
	mt := strings.ToLower(strings.TrimSpace(mediaType))
	if i := strings.IndexByte(mt, ';'); i >= 0 {
		mt = strings.TrimSpace(mt[:i])
	}
	if mt == "" || mt == "application/octet-stream" || mt == "image" {
		if raw, err := base64.StdEncoding.DecodeString(firstChunk(data)); err == nil || len(raw) > 0 {
			if d := http.DetectContentType(raw); strings.HasPrefix(d, "image/") || mt == "" {
				mt = d
			}
		}
	}
	if !strings.HasPrefix(mt, "image/") || geminiImageTypes[mt] {
		return map[string]any{"inlineData": map[string]any{"mimeType": cmp.Or(mt, mediaType), "data": data}}
	}
	if raw, err := base64.StdEncoding.DecodeString(data); err == nil {
		if img, _, err := image.Decode(bytes.NewReader(raw)); err == nil {
			var out bytes.Buffer
			if png.Encode(&out, img) == nil {
				return map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(out.Bytes())}}
			}
		}
	}
	return map[string]any{"text": "[An image of type " + mt + " was left out: Gemini reads PNG, JPEG, WebP, HEIC and HEIF.]"}
}

// firstChunk is the start of base64 data, a multiple of four characters
// long, enough to tell its type.
func firstChunk(data string) string {
	if len(data) > 64 {
		data = data[:64]
	}
	return data[:len(data)/4*4]
}
