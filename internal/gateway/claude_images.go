package gateway

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"strings"

	"golang.org/x/image/webp"
)

// Claude Code fits an image it reads from a file before it sends one
// (Claude Code 2.1.316: 2000 px a side and 5 MiB of base64 at most), as it
// does with the files magpie pastes into its prompt. An image magpie hands
// it as an image block is sent as it is: the earlier turns' images in a
// transcript a run resumes (replaySession), a -p run's stream-json
// message, and, fitted here too, a tool's images in its MCP result. The API refuses an image over 8000 px a side, and Claude Code
// then says "API Error: an image in the conversation could not be
// processed and was removed" and the turn fails. DeepSeek Harness (dsh)
// sends images up to 8192 px a side (its normalizedImageMaxDimension, the
// long edge after a 2048x2048 pixel budget), so a tall screenshot of
// 400x8192 went through dsh and the -anthropic route to a Claude
// subscription and was refused (sean_80289 on Discord). Claude Code took
// the same image pasted, fitted to 2000 px. The API also holds every image
// of a request with more than 20 to 2000 px, which dsh's 2048 budget
// passes too.
//
// claudeImage gives an image block what Claude Code would have made of
// the image: scaled to fit 2000 px a side, a JPEG written again as JPEG at
// quality 85 and any other as PNG, then as JPEG at a lower quality if that
// is still over the base64 limit. An image that fits goes as it came, byte
// for byte, and so does one that can't be read.
const (
	claudeImageSide   = 2000
	claudeImageBase64 = 5 << 20
)

// claudeImageLimits keys the images claudeImage made in fitted, apart
// from Codex's.
var claudeImageLimits = imageLimits{side: claudeImageSide, patches: -1}

// claudeImage is the media type and base64 an inline image is sent to
// Claude Code with.
func claudeImage(mediaType, data string) (string, string) {
	if data == "" || len(data) > codexImageMaxBytes {
		return mediaType, data
	}
	cfg, _, err := image.DecodeConfig(base64.NewDecoder(base64.StdEncoding, strings.NewReader(data)))
	if err != nil {
		return mediaType, data
	}
	if cfg.Width <= claudeImageSide && cfg.Height <= claudeImageSide && len(data) <= claudeImageBase64 {
		return mediaType, data
	}
	key := fitKey(mediaType+";"+data, claudeImageLimits)
	e, ok := fitted.get(key)
	if !ok {
		e = claudeImageRefit(data)
		fitted.put(key, e)
	}
	if e.data == nil {
		return mediaType, data
	}
	return e.mime, base64.StdEncoding.EncodeToString(e.data)
}

// claudeImageRefit is the image scaled and written again, or an entry
// with no data when it goes as it came.
func claudeImageRefit(data string) fitEntry {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return fitEntry{}
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return fitEntry{}
	}
	holds, ok := imageHolds(cfg, format)
	if !ok || len(raw)+holds > codexImageMaxBytes {
		return fitEntry{}
	}
	var src image.Image
	switch format {
	case "png":
		src, err = png.Decode(bytes.NewReader(raw))
	case "jpeg":
		src, err = jpeg.Decode(bytes.NewReader(raw))
	case "gif":
		src, err = gif.Decode(bytes.NewReader(raw))
	case "webp":
		src, err = webp.Decode(bytes.NewReader(raw))
	default:
		return fitEntry{}
	}
	if err != nil {
		return fitEntry{}
	}
	w, h := cfg.Width, cfg.Height
	if scale := math.Min(float64(claudeImageSide)/float64(max(w, h)), 1); scale < 1 {
		w = max(int(math.Round(float64(w)*scale)), 1)
		h = max(int(math.Round(float64(h)*scale)), 1)
		src = triangleResize(src, w, h)
	}
	fits := func(b []byte) bool { return base64.StdEncoding.EncodedLen(len(b)) <= claudeImageBase64 }
	var out bytes.Buffer
	if format == "jpeg" {
		err = jpeg.Encode(&out, src, &jpeg.Options{Quality: 85})
		if err == nil && fits(out.Bytes()) {
			return fitEntry{mime: "image/jpeg", data: out.Bytes()}
		}
	} else if err = png.Encode(&out, src); err == nil && fits(out.Bytes()) {
		return fitEntry{mime: "image/png", data: out.Bytes()}
	}
	// still over the limit: JPEG, on white where it was clear
	flat := image.NewRGBA(src.Bounds())
	draw.Draw(flat, flat.Rect, image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(flat, flat.Rect, src, src.Bounds().Min, draw.Over)
	for _, q := range []int{75, 60, 40} {
		out.Reset()
		if jpeg.Encode(&out, flat, &jpeg.Options{Quality: q}) == nil && fits(out.Bytes()) {
			return fitEntry{mime: "image/jpeg", data: out.Bytes()}
		}
	}
	return fitEntry{}
}
