package gateway

// What an image a request sends inline takes of the model's context, by
// the rule of the vendor whose protocol the request speaks, from the
// image's own size. The size is read from the image's header alone, the
// base64 decoded as a stream as far as the format needs (a few KB of a
// PNG, a JPEG up to its frame header), never the picture, so a request
// carrying hundreds of screenshots costs a few KB read for each.
//
// Every image used to be taken at 1600 tokens (imageTokens). A Codex
// session of a few hundred small screenshots then showed 413K of images in
// a 764K prompt that OpenAI counted at about 371K (@paiven_x on X): small
// pictures cost OpenAI a few hundred tokens each. An image whose size
// can't be read (a link, a file id, a document) is still taken at
// imageTokens, and the vendor's count, once it answers, scales them all
// (calibrate).

import (
	"bytes"
	"encoding/base64"
	"image"
	_ "image/gif"  // DecodeConfig of a GIF
	_ "image/jpeg" // of a JPEG
	_ "image/png"  // of a PNG
	"io"
	"math"
	"strconv"
	"strings"

	_ "golang.org/x/image/webp" // of a WebP

	"github.com/yetone/magpie/internal/provider"
)

// imageTokensOf is what an inline image takes of the context: src as the
// request sent it (an Anthropic source, a Responses image_url, a Chat
// image_url object, Gemini's inlineData), at detail when the request
// named one, to model, by the rule of the vendor of protocol from.
// imageTokens when its size can't be read.
func imageTokensOf(from provider.Protocol, model, detail string, src []byte) int {
	w, h, ok := inlineImageSize(src)
	if !ok {
		return imageTokens
	}
	if detail == "" {
		detail = jsonField(src, "detail")
	}
	switch from {
	case provider.Anthropic:
		return claudeImageTokens(w, h, model)
	case provider.Responses, provider.Chat:
		return openAIImageTokens(w, h, detail, model)
	}
	return imageTokens
}

// inlineImageSize is an inline image's width and height, read from its header.
func inlineImageSize(src []byte) (w, h int, ok bool) {
	b64 := imageBase64(src)
	if len(b64) == 0 {
		return 0, 0, false
	}
	var r io.Reader = bytes.NewReader(b64)
	if bytes.IndexByte(b64, '\\') >= 0 {
		r = unescaped{r} // a JSON encoder's \/
	}
	if ref, isRef := bytes.CutPrefix(b64, []byte(imageRef)); isRef {
		// a picture a lean body kept apart (leanImages)
		data, found := keptImageOf(string(ref))
		if !found {
			return 0, 0, false
		}
		r = strings.NewReader(data)
	}
	cfg, _, err := image.DecodeConfig(base64.NewDecoder(base64.StdEncoding, r))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

// imageBase64 is the base64 of an inline image's source, where it sits in
// the source's JSON, not copied: after a data: URL's comma, else Anthropic's
// and Gemini's "data" string. nil for a link or a file.
func imageBase64(src []byte) []byte {
	var s []byte
	if i := bytes.Index(src, []byte(";base64,")); i >= 0 {
		s = src[i+len(";base64,"):]
	} else if i := bytes.Index(src, []byte(`"data"`)); i >= 0 {
		s = bytes.TrimLeft(src[i+len(`"data"`):], " \t\r\n:")
		if len(s) == 0 || s[0] != '"' {
			return nil
		}
		s = s[1:]
	} else {
		return nil
	}
	if end := bytes.IndexByte(s, '"'); end >= 0 {
		s = s[:end]
	}
	return s
}

// unescaped reads a JSON string's bytes without its backslashes, which
// base64 has none of.
type unescaped struct{ r io.Reader }

func (u unescaped) Read(p []byte) (int, error) {
	n, err := u.r.Read(p)
	k := 0
	for _, c := range p[:n] {
		if c != '\\' {
			p[k] = c
			k++
		}
	}
	return k, err
}

// jsonField is a short string field of a small JSON object, "" when it
// has none: a Chat image_url's detail, read past its url.
func jsonField(obj []byte, key string) string {
	if len(obj) == 0 || obj[0] != '{' {
		return ""
	}
	q := []byte(`"` + key + `"`)
	i := bytes.LastIndex(obj, q)
	if i < 0 {
		return ""
	}
	s := bytes.TrimLeft(obj[i+len(q):], " \t\r\n:")
	if len(s) < 2 || s[0] != '"' {
		return ""
	}
	end := bytes.IndexByte(s[1:], '"')
	if end < 0 || end > 16 {
		return ""
	}
	return string(s[1 : 1+end])
}

// modelSlug is a model id as its vendor names it: lower case, without
// magpie's provider prefix.
func modelSlug(model string) string {
	m := strings.ToLower(model)
	if i := strings.LastIndexByte(m, '/'); i >= 0 {
		m = m[i+1:]
	}
	return m
}

// --- Anthropic ---

// claudeImageTokens is Anthropic's count of a w×h image: a token for each
// 28 px patch, ⌈w/28⌉×⌈h/28⌉, once the image is scaled to the largest
// size that fits the model's tier (platform.claude.com, Vision and
// "How Claude resizes and pads images"): Claude 4.7 and later 2576 px
// a side and 4784 tokens, the models before 1568 and 1568.
func claudeImageTokens(w, h int, model string) int {
	edge, most := 2576, 4784
	if claudeStandardTier(model) {
		edge, most = 1568, 1568
	}
	w, h = claudeResized(w, h, edge, most)
	return claudePatches(w, h)
}

func claudePatches(w, h int) int { return ((w + 27) / 28) * ((h + 27) / 28) }

// claudeResized is Anthropic's reference resizedSize: the largest
// aspect-kept size whose padded sides fit edge and whose patches fit most.
func claudeResized(w, h, edge, most int) (int, int) {
	fits := func(w, h int) bool {
		return (w+27)/28*28 <= edge && (h+27)/28*28 <= edge && claudePatches(w, h) <= most
	}
	if fits(w, h) {
		return w, h
	}
	if h > w {
		rh, rw := claudeResized(h, w, edge, most)
		return rw, rh
	}
	ratio := float64(w) / float64(h)
	short := func(long int) int { return max(int(math.RoundToEven(float64(long)/ratio)), 1) }
	lo, hi := 1, w
	for lo+1 < hi {
		mid := (lo + hi) / 2
		if fits(mid, short(mid)) {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo, short(lo)
}

// claudeStandardTier: a Claude model before 4.7 (Opus 4.6, Sonnet 4.5,
// Haiku 4.5, the 3s). Any other, a model magpie doesn't know among them,
// is taken at the high-resolution tier of the models Claude Code runs now.
func claudeStandardTier(model string) bool {
	m := modelSlug(model)
	if !strings.HasPrefix(m, "claude-") {
		return false
	}
	// claude-opus-4-6, claude-sonnet-4-5-20250929, claude-3-5-sonnet-…
	var nums []int
	for _, f := range strings.FieldsFunc(m, func(r rune) bool { return r == '-' || r == '.' || r == '[' }) {
		if n, err := strconv.Atoi(f); err == nil && len(f) <= 2 {
			nums = append(nums, n)
		}
	}
	if len(nums) == 0 {
		return false
	}
	major, minor := nums[0], 0
	if len(nums) > 1 {
		minor = nums[1]
	}
	return major < 4 || major == 4 && minor < 7
}

// --- OpenAI ---

// openAIImageTokens is OpenAI's count of a w×h image at detail on model
// (developers.openai.com, Images and vision: "Calculating costs"). The
// models before GPT-5.2 count 512 px tiles; the rest 32 px patches, times
// the model's multiplier, in limits each detail sets.
func openAIImageTokens(w, h int, detail, model string) int {
	m := modelSlug(model)
	if base, tile, ok := openAITiles(m); ok {
		if detail == "low" {
			return base
		}
		// fit 2048×2048, then the shortest side to 768, as tiles of 512
		fw, fh := fitSide(w, h, 2048)
		if s := min(fw, fh); s > 768 {
			r := 768 / float64(s)
			fw, fh = int(float64(fw)*r), int(float64(fh)*r)
			if fw < fh {
				fw = 768
			} else {
				fh = 768
			}
		}
		return base + tile*((fw+511)/512)*((fh+511)/512)
	}
	side, budget := openAIPatchLimits(m, detail)
	pw, ph := fitSide(w, h, side)
	if budget > 0 && openAIPatches(pw, ph) > budget {
		fw, fh := float64(pw), float64(ph)
		s := math.Sqrt(32 * 32 * float64(budget) / (fw * fh))
		s *= min(math.Floor(fw*s/32)/(fw*s/32), math.Floor(fh*s/32)/(fh*s/32))
		pw, ph = max(int(fw*s), 1), max(int(fh*s), 1)
	}
	return int(math.Ceil(float64(openAIPatches(pw, ph)) * openAIMultiplier(m)))
}

func openAIPatches(w, h int) int { return ((w + 31) / 32) * ((h + 31) / 32) }

// fitSide scales w×h to fit side×side, never up.
func fitSide(w, h, side int) (int, int) {
	if side <= 0 || w <= side && h <= side {
		return w, h
	}
	r := float64(side) / float64(max(w, h))
	return max(int(math.Round(float64(w)*r)), 1), max(int(math.Round(float64(h)*r)), 1)
}

// openAITiles: the base and per-tile tokens of a model that counts tiles.
func openAITiles(m string) (base, tile int, ok bool) {
	switch {
	case strings.HasPrefix(m, "gpt-4o-mini"):
		return 2833, 5667, true
	case strings.HasPrefix(m, "gpt-4o"), m == "gpt-4.1" || strings.HasPrefix(m, "gpt-4.1-20"):
		return 85, 170, true
	case m == "o1" || strings.HasPrefix(m, "o1-"), m == "o3" || strings.HasPrefix(m, "o3-pro") || strings.HasPrefix(m, "o3-20"):
		return 75, 150, true
	case strings.HasPrefix(m, "gpt-5.1"),
		strings.HasPrefix(m, "gpt-5") && !strings.HasPrefix(m, "gpt-5.") && !strings.HasPrefix(m, "gpt-5-mini") && !strings.HasPrefix(m, "gpt-5-nano"):
		return 70, 140, true
	}
	return 0, 0, false
}

// openAIMultiplier is a patch model's multiplier: 1.2 for GPT-5.2 and
// after, and for a model OpenAI doesn't list.
func openAIMultiplier(m string) float64 {
	switch {
	case strings.HasPrefix(m, "gpt-5-nano"):
		return 1.5
	case strings.HasPrefix(m, "gpt-4.1-mini"):
		return 1.62
	case strings.HasPrefix(m, "gpt-4.1-nano"):
		return 2.46
	case strings.HasPrefix(m, "o4-mini"):
		return 1.72
	}
	return 1.2
}

// openAIPatchLimits is the longest side and the patch budget (0: none) a
// patch model holds an image to at detail; auto is the model's default.
func openAIPatchLimits(m, detail string) (side, budget int) {
	switch {
	case strings.HasPrefix(m, "gpt-5.2"), strings.HasPrefix(m, "gpt-4.1-"), strings.HasPrefix(m, "gpt-5-mini"),
		strings.HasPrefix(m, "gpt-5-nano"), strings.HasPrefix(m, "o4-mini"):
		return 2048, 6144 // every detail alike
	case strings.HasPrefix(m, "gpt-5.4"), strings.HasPrefix(m, "gpt-5.3"):
		switch detail {
		case "low":
			return 2048, 6144
		case "original":
			return 6000, 10000
		}
		return 2048, 2500 // high, auto
	}
	// GPT-5.5 and after, and models OpenAI doesn't list: auto is original
	switch detail {
	case "low":
		return 512, 0
	case "high":
		return 2048, 2500
	}
	if strings.HasPrefix(m, "gpt-5.5") {
		return 6000, 10000
	}
	return 65535, 0
}
