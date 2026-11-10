package gateway

import (
	"bytes"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"
)

// Text that isn't text, in a JSON body an agent sends (laincatx on X):
// bytes that aren't UTF-8 (a GBK file, a lone surrogate encoded as UTF-8,
// \xed\xa0\x80) and an escaped surrogate with no partner ("\ud800").
// magpie kept such a string's bytes as they came and sent them on, and
// each vendor took them its own way: DeepSeek and OpenAI answered 400,
// MiniMax 200 with nothing in it, so in a routing group one request
// worked or failed by which member it reached.
//
// They become U+FFFD, the replacement character, where every route reads
// its body (readBoundedRequestBody; a Codex socket's turn is served as a
// POST through the same), before anything routes, counts or forwards it:
// what Go's own JSON decoder and every WHATWG text decoder make of them.
// A turn is not refused for them: an agent's tool result carries a
// file's bytes as they are (Claude Code reading a GBK source file, a log
// cut mid-character), and a 400 there (Anthropic's "no low surrogate in
// string") fails every later turn of the session, which sends the same
// history again. Each bad byte is one U+FFFD, as encoding/json writes it.
//
// Only a body that is JSON is touched; a multipart upload or audio is
// not text. A body that is valid already is returned as it is, with no
// copy: the check is utf8.Valid and a scan of its backslashes.
func textBody(h http.Header, body []byte) []byte {
	if !jsonBody(h, body) || !needsText(body) {
		return body
	}
	return fixText(body)
}

// jsonBody says the body is a JSON document: one that opens with { or [,
// under no media type that holds bytes of another kind.
func jsonBody(h http.Header, body []byte) bool {
	if ct := h.Get("Content-Type"); ct != "" {
		mt, _, err := mime.ParseMediaType(ct)
		if err != nil {
			mt = strings.ToLower(strings.TrimSpace(ct))
		}
		if strings.HasPrefix(mt, "multipart/") || strings.HasPrefix(mt, "audio/") || strings.HasPrefix(mt, "image/") ||
			strings.HasPrefix(mt, "video/") || mt == "application/octet-stream" || mt == "application/x-www-form-urlencoded" {
			return false
		}
	}
	t := bytes.TrimLeft(body, " \t\r\n")
	return len(t) > 0 && (t[0] == '{' || t[0] == '[')
}

// needsText says body holds a byte that isn't UTF-8 or an escape of a
// lone surrogate.
func needsText(body []byte) bool {
	if !utf8.Valid(body) {
		return true
	}
	for i := 0; i < len(body); {
		j := bytes.IndexByte(body[i:], '\\')
		if j < 0 {
			return false
		}
		i += j
		if i+1 >= len(body) {
			return false
		}
		if body[i+1] != 'u' {
			i += 2 // \\, \" and the like: the next byte is not an escape's start
			continue
		}
		switch r := hex4(body[i:]); {
		case r >= 0xD800 && r <= 0xDBFF:
			if !lowNext(body, i+6) {
				return true
			}
			i += 12 // a pair, as Python's json.dumps writes an emoji
		case r >= 0xDC00 && r <= 0xDFFF:
			return true
		default:
			i += 2
		}
	}
	return false
}

// lowNext says a low surrogate's escape is at body[i:].
func lowNext(body []byte, i int) bool {
	if i > len(body) {
		return false
	}
	lo := hex4(body[i:])
	return lo >= 0xDC00 && lo <= 0xDFFF
}

// fixText writes body with each byte that isn't UTF-8 and each lone
// surrogate escape as U+FFFD; a surrogate pair's two escapes are kept.
func fixText(body []byte) []byte {
	out := make([]byte, 0, len(body)+16)
	for i := 0; i < len(body); {
		c := body[i]
		if c == '\\' && i+1 < len(body) {
			if body[i+1] != 'u' {
				out = append(out, c, body[i+1])
				i += 2
				continue
			}
			r := hex4(body[i:])
			switch {
			case r < 0:
				out = append(out, c, 'u')
				i += 2
			case r >= 0xD800 && r <= 0xDBFF:
				if lowNext(body, i+6) {
					out = append(out, body[i:i+12]...)
					i += 12
					continue
				}
				out = append(out, `\ufffd`...)
				i += 6
			case r >= 0xDC00 && r <= 0xDFFF:
				out = append(out, `\ufffd`...)
				i += 6
			default:
				out = append(out, body[i:i+6]...)
				i += 6
			}
			continue
		}
		if c < utf8.RuneSelf {
			out = append(out, c)
			i++
			continue
		}
		r, size := utf8.DecodeRune(body[i:])
		if r == utf8.RuneError && size <= 1 {
			out = append(out, "\uFFFD"...)
			i++
			continue
		}
		out = append(out, body[i:i+size]...)
		i += size
	}
	return out
}
