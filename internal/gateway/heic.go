package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/gen2brain/h265/heic"

	"github.com/yetone/magpie/internal/provider"
)

// An iPhone's photos are HEIC, and an agent sends one dragged in as it is
// (Pi, Pi Desktop). Most vendors take JPEG, PNG, GIF and WebP only, and
// turn the request away: DeepSeek on WorkBuddy did (qikamatata on X).
// heicAsJPEG writes each HEIC image of a request again as JPEG, or as PNG
// when it has transparency, before anything reads the body, so every
// route has it so: a provider's own, a plugin's fetch, a describer, the
// count of its tokens. It goes by the bytes, not the type the request
// gives them: a HEIC sent as image/jpeg is one too.
//
// The decoder is gen2brain/h265's, pure Go (magpie is built without cgo
// for every system), so it is the same on every computer, and no system
// converter need be there.
//
// A request with no HEIC in it is not walked: mayHoldHEIC is a scan of
// its bytes. An image that can't be read, is too big, or takes too long
// goes as it came, and the vendor says what it says of it.

// heicBrands are the ftyp brands of an HEVC still (ISO/IEC 23008-12), as
// the decoder registers them.
var heicBrands = []string{"heic", "heix", "heim", "heis", "hevc", "hevx", "hevm", "hevs", "mif1", "msf1"}

// heicMarks are the base64 of "yp" and a brand: the file's bytes 6 to 11,
// "ftyp" ending at 8, are its base64's characters 8 to 15 wherever the
// file's base64 starts. All begin "eXB".
var heicMarks = func() map[string]bool {
	m := map[string]bool{}
	for _, b := range heicBrands {
		m[base64.StdEncoding.EncodeToString([]byte("yp"+b))] = true
	}
	return m
}()

const (
	// a HEIC larger than this goes as it came
	heicMaxBase64 = 48 << 20
	// nor is one decoded with more pixels than a 48 MP iPhone photo
	heicMaxPixels = 50_000_000
	// past this many pixels, what a conversion held is given back at once
	heicFreeAbove = 16_000_000
	// the longest side written: no vendor reads more, and a 24 or 48 MP
	// photo written whole would be a JPEG of tens of megabytes
	heicMaxSide = 4096
	// what the cache of converted images holds at most
	heicCacheBytes   = 64 << 20
	heicCacheEntries = 32
)

// heicWait is how long a request waits for its image; one not converted
// by then goes as it came, and its conversion, finished, serves the
// request's next turn.
var heicWait = 30 * time.Second

// heicEncode writes a HEIC image again; a var for tests.
var heicEncode = encodeHEIC

// mayHoldHEIC is whether the body may have a HEIC in it: an image/heic or
// image/heif type, or the base64 of an HEVC still's ftyp.
func mayHoldHEIC(body []byte) bool {
	if bodyHas(body, "image/hei") || bodyHas(body, `image\/hei`) {
		return true
	}
	for rest := body; ; {
		i := bodyIndex(rest, "eXB")
		if i < 0 {
			return false
		}
		if i+8 <= len(rest) && heicMarks[string(rest[i:i+8])] {
			return true
		}
		rest = rest[i+3:]
	}
}

// heicAsJPEG is the body with each HEIC image in it written as JPEG (PNG
// with transparency); the body as it came when there is none.
func heicAsJPEG(proto provider.Protocol, body []byte) []byte {
	if !mayHoldHEIC(body) {
		return body
	}
	out, err := walkImages(proto, body, func(im imageAt) (json.RawMessage, error) {
		raw := heicOf(im.Src)
		if raw == nil {
			return nil, nil
		}
		mime, data := heicConverted(raw)
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

// heicOf is the bytes of a data: URL's image when they are a HEIC's.
func heicOf(src string) []byte {
	rest, ok := strings.CutPrefix(src, "data:")
	if !ok {
		return nil
	}
	head, b64, ok := strings.Cut(rest, ",")
	if !ok || !strings.HasSuffix(head, ";base64") || len(b64) < 16 || len(b64) > heicMaxBase64 {
		return nil
	}
	sniff, err := base64.StdEncoding.DecodeString(b64[:16])
	if err != nil || !isHEIC(sniff) {
		return nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(b64, "="))
	if err != nil {
		return nil
	}
	return raw
}

// isHEIC is whether b starts as an HEVC still's file does.
func isHEIC(b []byte) bool {
	if len(b) < 12 || string(b[4:8]) != "ftyp" {
		return false
	}
	for _, brand := range heicBrands {
		if string(b[8:12]) == brand {
			return true
		}
	}
	return false
}

type heicResult struct {
	mime string
	data []byte // nil: it goes as it came
}

var heicCache struct {
	sync.Mutex
	m     map[[32]byte]heicResult
	order [][32]byte
	size  int
}

// heicSlots is how many images are decoded at once: a 48 MP photo holds
// a few hundred megabytes while it is.
var heicSlots = make(chan struct{}, 1)

// heicConverted is the HEIC raw written again, from the cache when it was
// before: an agent sends its conversation's images on every turn.
func heicConverted(raw []byte) (string, []byte) {
	return converted(raw, heicEncode)
}

// converted is heicConverted for an encode: the raw written again by enc,
// from the cache when it was before.
func converted(raw []byte, enc func([]byte) (string, []byte)) (string, []byte) {
	k := sha256.Sum256(raw)
	heicCache.Lock()
	r, ok := heicCache.m[k]
	heicCache.Unlock()
	if ok {
		return r.mime, r.data
	}
	done := make(chan heicResult, 1)
	go func() {
		heicSlots <- struct{}{}
		defer func() { <-heicSlots }()
		var r heicResult
		func() {
			defer func() {
				if recover() != nil {
					r = heicResult{} // the decoder's fault on a bad file
				}
			}()
			r.mime, r.data = enc(raw)
		}()
		heicKeep(k, r)
		done <- r
	}()
	timer := time.NewTimer(heicWait)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.mime, r.data
	case <-timer.C:
		return "", nil
	}
}

// heicKeep caches r, dropping the oldest past the cache's bounds.
func heicKeep(k [32]byte, r heicResult) {
	heicCache.Lock()
	defer heicCache.Unlock()
	if heicCache.m == nil {
		heicCache.m = map[[32]byte]heicResult{}
	}
	if _, ok := heicCache.m[k]; ok || len(r.data) > heicCacheBytes {
		return
	}
	heicCache.m[k] = r
	heicCache.order = append(heicCache.order, k)
	heicCache.size += len(r.data)
	for len(heicCache.order) > heicCacheEntries || heicCache.size > heicCacheBytes {
		old := heicCache.order[0]
		heicCache.order = heicCache.order[1:]
		heicCache.size -= len(heicCache.m[old].data)
		delete(heicCache.m, old)
	}
}

// encodeHEIC decodes a HEIC, turned as it says it is shown, and writes it
// as JPEG, or as PNG when it has transparency; "", nil when it can't.
func encodeHEIC(raw []byte) (string, []byte) {
	cfg, err := heic.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > heicMaxPixels {
		return "", nil
	}
	img, err := heic.Decode(bytes.NewReader(raw), heic.Options{AutoRotate: true, FrameSizeLimit: heicMaxPixels})
	if err != nil {
		return "", nil
	}
	return pictureWritten(img, cfg.Width*cfg.Height)
}

// pictureWritten is img written as JPEG, or as PNG when it has
// transparency, its longest side heicMaxSide at most; "", nil when it
// can't. pixels is the count of what the decode held, a big one's home
// going back to the system once it is written.
func pictureWritten(img image.Image, pixels int) (string, []byte) {
	if pixels > heicFreeAbove {
		// a 48 MP photo held about a gigabyte while it was written
		// again (1.6s on an M5 Pro); what it held goes back to the system
		// now, not minutes later
		defer debug.FreeOSMemory()
	}
	if b := img.Bounds(); max(b.Dx(), b.Dy()) > heicMaxSide {
		w, h := heicMaxSide, max(1, b.Dy()*heicMaxSide/b.Dx())
		if b.Dy() > b.Dx() {
			w, h = max(1, b.Dx()*heicMaxSide/b.Dy()), heicMaxSide
		}
		img = triangleResize(img, w, h)
	}
	var out bytes.Buffer
	if o, ok := img.(interface{ Opaque() bool }); ok && !o.Opaque() {
		if png.Encode(&out, img) != nil {
			return "", nil
		}
		return "image/png", out.Bytes()
	}
	if jpeg.Encode(&out, img, &jpeg.Options{Quality: 90}) != nil {
		return "", nil
	}
	return "image/jpeg", out.Bytes()
}

// withImageData is an image block with its data replaced by b64 of mime,
// its other fields as they were; nil when the block can't be read.
func withImageData(proto provider.Protocol, block json.RawMessage, mime, b64 string) json.RawMessage {
	var m map[string]json.RawMessage
	if decodeJSON(block, &m) != nil {
		return nil
	}
	set := func(field string, values map[string]string) bool {
		inner := map[string]json.RawMessage{}
		if len(m[field]) > 0 && decodeJSON(m[field], &inner) != nil {
			return false
		}
		for k, v := range values {
			inner[k], _ = json.Marshal(v)
		}
		m[field], _ = json.Marshal(inner)
		return true
	}
	url := "data:" + mime + ";base64," + b64
	switch proto {
	case provider.Chat:
		var s string
		if decodeJSON(m["image_url"], &s) == nil {
			m["image_url"], _ = json.Marshal(url)
		} else if !set("image_url", map[string]string{"url": url}) {
			return nil
		}
	case provider.Responses:
		m["image_url"], _ = json.Marshal(url)
	case provider.Anthropic:
		if !set("source", map[string]string{"type": "base64", "media_type": mime, "data": b64}) {
			return nil
		}
	case provider.Gemini:
		if !set("inlineData", map[string]string{"mimeType": mime, "data": b64}) {
			return nil
		}
	default:
		return nil
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return out
}
