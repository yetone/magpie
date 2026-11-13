package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
	"golang.org/x/image/webp"
)

// Claude Desktop's browser tool answers a screenshot as image/webp, and
// GLM can't read one: its 400 [1210] "图片输入格式/解析错误" turns the
// picture away, and its Anthropic-style endpoint says of the same picture
// 400 [1214] "messages[0].content[0].file必须传入file_id、file_url、
// file_data至少之一", having taken it for a file of its own — the two
// alternating (zhangqingwen204's report, #4732). The picture rides in the
// conversation's history, so every later turn was refused the same way,
// and the session was stuck. Most vendors do take webp, so it is not
// written again for them all the way a HEIC is (heic.go): a provider that
// refused one (webpRefused) has every webp of its requests written again
// as JPEG — as PNG when it has transparency — before anything reads the
// body, the first turn paying one refusal to learn it.

// webpRefused is how unfit remembers a provider that turned a webp away.
const webpRefused = "image/webp"

// zhipuImageRefusal is how GLM's says it can't read the picture: its
// [1210] word for a format it doesn't parse, its [1214] word for one it
// took for a file and found none of its file fields in, and the pair of
// codes in the double brackets it puts before them. Read of the words
// wordsOf gives, as a relay may pass them on \u-escaped (#1669).
var zhipuImageRefusal = lazyre.New(`必须传入file_id、file_url、file_data至少之一|图片输入格式|\[(?:1210|1214)\]\[`)

// mayHoldWebP is whether the body may hold a webp: an image/webp type, or
// the base64 of a RIFF file's head, wherever its base64 starts (a webp
// sent under another type is one too, as a HEIC sent as image/jpeg is).
func mayHoldWebP(body []byte) bool {
	return bodyHas(body, "image/webp") || bodyHas(body, `image\/webp`) ||
		bodyHas(body, "UklG")
}

// webpAsJPEG is the body with each webp in it written again as JPEG (PNG
// with transparency); the body as it came when there is none.
func webpAsJPEG(proto provider.Protocol, body []byte) []byte {
	if !mayHoldWebP(body) {
		return body
	}
	out, err := walkImages(proto, body, func(im imageAt) (json.RawMessage, error) {
		raw := webpOf(im.Src)
		if raw == nil {
			return nil, nil
		}
		mime, data := webpConverted(raw)
		if data == nil {
			return nil, nil
		}
		return withImageData(proto, im.Block, mime, base64.StdEncoding.EncodeToString(data)), nil
	})
	if err != nil {
		return body
	}
	return out
}

// webpOf is the bytes of a data: URL's image when they are a webp's.
func webpOf(src string) []byte {
	rest, ok := strings.CutPrefix(src, "data:")
	if !ok {
		return nil
	}
	head, b64, ok := strings.Cut(rest, ",")
	if !ok || !strings.HasSuffix(head, ";base64") || len(b64) < 16 || len(b64) > heicMaxBase64 {
		return nil
	}
	sniff, err := base64.StdEncoding.DecodeString(b64[:16])
	if err != nil || !isWebP(sniff) {
		return nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(b64, "="))
	if err != nil {
		return nil
	}
	return raw
}

// isWebP is whether b starts as a webp's file does.
func isWebP(b []byte) bool {
	return len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP"
}

// webpConverted is the webp raw written again, from the cache when it was
// before (converted, heic.go): an agent sends its conversation's images on
// every turn.
func webpConverted(raw []byte) (string, []byte) {
	return converted(raw, webpEncode)
}

// webpEncode writes a webp again; a var for tests.
var webpEncode = encodeWebP

// encodeWebP decodes a webp and writes it as pictureWritten does; "", nil
// when it can't.
func encodeWebP(raw []byte) (string, []byte) {
	cfg, err := webp.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > heicMaxPixels {
		return "", nil
	}
	img, err := webp.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", nil
	}
	return pictureWritten(img, cfg.Width*cfg.Height)
}
