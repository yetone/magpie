package gateway

import (
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// Google's Python SDK (google-genai) writes a part's bytes with
// base64.urlsafe_b64encode, '-' and '_' where standard base64 has '+' and
// '/', and Google's own API reads either. A vendor or relay behind magpie
// may read only the standard alphabet: dludream's image through a Gemini
// provider came back 400 "Failed to base64-decode image data", the same
// request with '+' and '/' answered (#5697). Every place in magpie that
// reads an image's bytes (HEIC and webp conversion, resizing, describing,
// token counts) read them as standard base64 too, and passed over one it
// couldn't. stdBase64Body writes the inline base64 a client sends as
// standard, padded base64 before anything reads the body, so every route
// has it so: a provider's own protocol passed through, one translated to,
// a plugin's fetch. Base64 that is standard and padded already is left as
// it came, byte for byte.

// stdBase64 is s as standard, padded base64 (RFC 4648 §4) when it is the
// URL-safe alphabet (§5) or unpadded; ok is false when s is standard and
// padded already, or isn't base64 of either alphabet (it then goes as it
// came, and the vendor says what it says of it).
func stdBase64(s string) (string, bool) {
	if s == "" {
		return s, false
	}
	urlSafe := strings.ContainsAny(s, "-_")
	if !urlSafe && len(s)%4 == 0 {
		return s, false
	}
	if urlSafe && strings.ContainsAny(s, "+/") {
		return s, false // both alphabets: neither's
	}
	enc := base64.RawStdEncoding
	if urlSafe {
		enc = base64.RawURLEncoding
	}
	raw, err := enc.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		return s, false
	}
	return base64.StdEncoding.EncodeToString(raw), true
}

// mayHoldOddBase64 is whether body may hold base64 stdBase64 would write
// again, read in place: a run of at least 64 base64 characters (of either
// alphabet) ending a JSON string, with '-' or '_' in it or a length that
// isn't a multiple of four. Standard base64 of an image has '+' or '/'
// every few dozen characters, which ends a URL-safe run, and prose has
// spaces; a body it says yes to wrongly is only walked for nothing.
func mayHoldOddBase64(body []byte) bool {
	const least = 64
	start, odd := -1, false
	for i, c := range body {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '+', c == '/', c == '=':
			if start < 0 {
				start, odd = i, false
			}
		case c == '-' || c == '_':
			if start < 0 {
				start = i
			}
			odd = true
		default:
			if start >= 0 && c == '"' && i-start >= least && (odd || (i-start)%4 != 0) {
				return true
			}
			start = -1
		}
	}
	return false
}

// stdBase64Body is the body with each image's inline base64, and each
// Gemini inlineData part's whatever its type (a PDF, a recording), written
// as standard, padded base64; the body as it came when there is none.
func stdBase64Body(proto provider.Protocol, body []byte) []byte {
	if !mayHoldOddBase64(body) {
		return body
	}
	if proto == provider.Gemini {
		return geminiStdBase64(body)
	}
	out, err := walkImages(proto, body, func(im imageAt) (json.RawMessage, error) {
		rest, ok := strings.CutPrefix(im.Src, "data:")
		if !ok {
			return nil, nil
		}
		head, b64, ok := strings.Cut(rest, ",")
		mime, isB64 := strings.CutSuffix(head, ";base64")
		if !ok || !isB64 {
			return nil, nil
		}
		std, ok := stdBase64(b64)
		if !ok {
			return nil, nil
		}
		return withImageData(proto, im.Block, mime, std), nil
	})
	if err != nil {
		return body
	}
	return out
}

// geminiStdBase64 writes every inlineData part's data of a Gemini request
// (a turn's, a function response's, the system instruction's) as standard
// base64, in place: the rest of the body keeps its bytes.
func geminiStdBase64(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	type edit struct {
		at, end   int
		raw, with string
	}
	var edits []edit
	var parts func(gjson.Result)
	parts = func(list gjson.Result) {
		list.ForEach(func(_, part gjson.Result) bool {
			for _, k := range []string{"inlineData", "inline_data"} {
				d := part.Get(k + ".data")
				if d.Type != gjson.String || d.Index <= 0 {
					continue
				}
				if std, ok := stdBase64(d.Str); ok {
					q, _ := json.Marshal(std)
					edits = append(edits, edit{d.Index, d.Index + len(d.Raw), d.Raw, string(q)})
				}
			}
			for _, k := range []string{"functionResponse", "function_response"} {
				if fr := part.Get(k + ".parts"); fr.IsArray() {
					parts(fr)
				}
			}
			return true
		})
	}
	root := gjson.ParseBytes(body)
	root.Get("contents").ForEach(func(_, turn gjson.Result) bool {
		parts(turn.Get("parts"))
		return true
	})
	for _, k := range []string{"systemInstruction", "system_instruction"} {
		parts(root.Get(k + ".parts"))
	}
	if len(edits) == 0 {
		return body
	}
	// the system instruction may come before the turns or after them;
	// each edit is checked to be where gjson placed it
	sort.Slice(edits, func(i, j int) bool { return edits[i].at < edits[j].at })
	out := make([]byte, 0, len(body))
	last := 0
	for _, e := range edits {
		if e.at < last || e.end > len(body) || string(body[e.at:e.end]) != e.raw {
			return body // overlapping, as a key spelled twice: as it came
		}
		out = append(out, body[last:e.at]...)
		out = append(out, e.with...)
		last = e.end
	}
	out = append(out, body[last:]...)
	if !json.Valid(out) {
		return body
	}
	return out
}
