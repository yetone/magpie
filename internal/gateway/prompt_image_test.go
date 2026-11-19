package gateway

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// pngB64 is a real PNG of w×h, as base64.
func pngB64(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x += 7 {
		img.Set(x, x%h, color.RGBA{R: byte(x), G: 90, B: 200, A: 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// jpegB64 is a real JPEG of w×h with a 40 KB APP1 segment (EXIF's place)
// before its frame header, as a camera's or a phone's has.
func jpegB64(t *testing.T, w, h int) string {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	app := make([]byte, 40000)
	seg := append([]byte{0xFF, 0xE1, byte((len(app) + 2) >> 8), byte(len(app) + 2)}, app...)
	out := append(append(append([]byte{}, raw[:2]...), seg...), raw[2:]...)
	return base64.StdEncoding.EncodeToString(out)
}

// An inline image is counted by the vendor's rule from its own size, not
// at 1600 whatever it is: a Codex session of a few hundred small
// screenshots showed 413K of images in a prompt OpenAI counted at about
// 371K in all (@paiven_x on X). The numbers are the vendors' own docs'.
func TestPromptImageTokensByVendor(t *testing.T) {
	small, shot, hd := pngB64(t, 800, 500), pngB64(t, 2880, 1800), pngB64(t, 1920, 1080)
	resp := func(model, detail, b64 string) string {
		d := ""
		if detail != "" {
			d = `,"detail":"` + detail + `"`
		}
		return `{"model":"` + model + `","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"look"},{"type":"input_image","image_url":"data:image/png;base64,` + b64 + `"` + d + `}]}]}`
	}
	chat := func(model, detail, b64 string) string {
		return `{"model":"` + model + `","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + b64 + `","detail":"` + detail + `"}}]}]}`
	}
	anth := func(model, b64 string) string {
		return `{"model":"` + model + `","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}]}]}`
	}
	cases := []struct {
		name string
		from provider.Protocol
		body string
		want int
	}{
		// 32 px patches, 25×16, times 1.2; auto is original on GPT-6
		{"codex small screenshot", provider.Responses, resp("codex/gpt-6.1-terra", "", small), 480},
		// high: 2048 a side, then the 2500-patch budget: 1996×1248, 63×39 patches
		{"retina at high", provider.Responses, resp("gpt-6.1-terra", "high", shot), 2949},
		// low on GPT-5.5 and after: fits 512×512, 16×10 patches
		{"low", provider.Responses, resp("gpt-5.5", "low", small), 192},
		// tiles: 1024² is 768² at high, 4 tiles of 170 and 85 (OpenAI's example)
		{"gpt-4o tiles", provider.Chat, chat("gpt-4o", "high", pngB64(t, 1024, 1024)), 765},
		{"gpt-4o low", provider.Chat, chat("gpt-4o", "low", shot), 85},
		// Anthropic's table: 1920×1080 is 2691 on the high-resolution tier,
		// 1456×819 and 1560 on the standard
		{"claude high tier", provider.Anthropic, anth("claude-opus-5-5", hd), 2691},
		{"claude standard tier", provider.Anthropic, anth("anthropic/claude-sonnet-4-5-20250929", hd), 1560},
		// 3840×2160: 2576×1449, 4784
		{"claude 4k", provider.Anthropic, anth("claude-opus-4-7", pngB64(t, 3840, 2160)), 4784},
		// a JPEG's frame header behind 40 KB of EXIF is still read: 600×400
		// on the standard tier is 22×15
		{"jpeg behind exif", provider.Anthropic, strings.Replace(anth("claude-haiku-4-5", jpegB64(t, 600, 400)), "image/png", "image/jpeg", 1), 330},
		// a JSON encoder's \/ in the base64
		{"escaped slashes", provider.Responses, strings.ReplaceAll(resp("gpt-6.1-terra", "", small), "/", `\/`), 480},
	}
	for _, c := range cases {
		p := promptOf(c.from, []byte(c.body))
		if p == nil {
			t.Fatalf("%s: no prompt", c.name)
		}
		it, ok := itemOf(p, PartFiles, "image")
		if !ok || it.Tokens != c.want {
			t.Errorf("%s: image %+v, want %d tokens", c.name, it, c.want)
		}
	}
}

// Only the image's header is read: its size comes out of the base64 of a
// PNG's first few KB, the rest of it never decoded, so hundreds of
// screenshots in one Codex request cost a few KB read each.
func TestPromptImageSizeReadsHeaderOnly(t *testing.T) {
	b64 := pngB64(t, 2880, 1800)
	src := []byte(`"data:image/png;base64,` + b64[:4096] + `"`)
	w, h, ok := inlineImageSize(src)
	if !ok || w != 2880 || h != 1800 {
		t.Fatalf("size %d×%d %v", w, h, ok)
	}
	// a link, a file, bytes that aren't an image: the estimate for a picture
	for _, s := range []string{`"https://example.com/a.png"`, `{"type":"url","url":"https://x/a.png"}`, `"data:image/png;base64,AAAA"`} {
		if n := imageTokensOf(provider.Responses, "gpt-6.1-terra", "", []byte(s)); n != imageTokens {
			t.Fatalf("%s: %d", s, n)
		}
	}
}
