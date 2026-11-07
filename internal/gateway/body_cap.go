package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// A provider's cap on the bytes of one request.
//
// Codex sends the whole conversation on every turn, its screenshots inline
// as base64, and its compaction's summary request is that conversation
// too. Past a vendor's byte cap — Command Code's "Request body is 40.1 MB,
// over the 32 MB limit for one request (33554432 bytes)" (EDwinpzy on
// Discord, a Codex session of many screenshots), Anthropic's 32 MB
// request_too_large, DeepSeek's 413 — every turn and every compaction was
// refused, the same body asked again until the compaction guard held it
// (compactFails), and the conversation could neither compact nor go on.
//
// A refusal that says the body is too large (byteCapRefusal) teaches the
// provider's cap: the one it states, or else three quarters of what was
// sent. The request is then sent again with the oldest images of its
// history left out, each replaced by a short text saying so, until it is
// under three quarters of the cap — room for the conversation to grow a
// few turns before more go, so its prompt cache isn't broken on every
// turn. The latest user message's images stay, and no text is touched:
// a tool's output keeps its words, only an image in it can go. Later
// requests to the provider are fitted the same way before they are sent
// (forwardOnce). Every route through serve goes through forward — the
// agent's turns, Codex's compaction, titles, the classifier, search and
// vision stand-ins — so each is fitted alike.
//
// When leaving every earlier image out still doesn't bring it under, the
// request isn't sent again: the refusal goes on as a 413 saying how large
// the request is, the cap, and that the latest images and the text were
// kept, which Codex takes as its cue to compact (streamError) and
// codexCompact as its cue to ask the summary as text (summaryTooLarge).

// bodyCapWords are a vendor's words for a request body over its byte
// cap, not a conversation over the model's window (tooLong) nor a rate.
var bodyCapWords = regexp.MustCompile(`(?i)request[ _](body|entity|payload)( size)?( is| was)? (too large|too big|over|larger than|exceeds?|exceeded)|request body is [\d.]+ ?[kmg]i?b|payload too large|entity too large|request_too_large|(body|request|payload) (size )?(exceeds?|exceeded|is over|is larger than) (the )?(max|limit|\d)|length limit exceeded|maximum allowed number of bytes|limit for one request|(max(imum)?|request) (request |body )*size (limit )?(exceeded|of)`)

// byteCapRefusal is a provider's refusal of a request for its bytes: a
// 413 that doesn't say the model's window is full, or any other refusal
// (Command Code's came as its own status) whose words say the body is
// over a limit. A sign-in's refusal or a rate limit is never one.
func byteCapRefusal(status int, errBody []byte) bool {
	if status < 400 || status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusTooManyRequests {
		return false
	}
	words := string(wordsOf(errBody))
	if tooLong(status, words) {
		return false
	}
	return status == http.StatusRequestEntityTooLarge || bodyCapWords.MatchString(words)
}

// pageCapLeast is the least a request weighs before a proxy's page in
// place of the API's answer is read as a refusal of its bytes.
const pageCapLeast = 4 << 20

// pageOverSize is a proxy's refusal of a large body said as its own page:
// an nginx or CDN "502 Bad Gateway" (or 413, 500, 503, 504, 520 to 524)
// page or an empty body, in place of the API's answer, to a request of
// pageCapLeast or more. chenxiang0010's relay (an OpenAI-compatible one
// in front of DeepSeek) answered an 8.2 MB Claude Code turn so: magpie
// passed the 502 on, Claude Code asked the same body again nine times
// and every --resume of the session met the same page. Said as a 413 it
// goes to the route's next member, isn't asked again unchanged, and tells
// the user to compact. A small request's page is the relay's own fault
// and stays what it was.
func pageOverSize(status int, errBody []byte, sent int) bool {
	if sent < pageCapLeast {
		return false
	}
	switch {
	case status == http.StatusRequestEntityTooLarge, status == http.StatusInternalServerError,
		status == http.StatusBadGateway, status == http.StatusServiceUnavailable,
		status == http.StatusGatewayTimeout, status >= 520 && status <= 524:
	default:
		return false
	}
	_, ok := provider.Unsaid(errBody)
	return ok
}

// capSizes are the sizes a refusal names: "33554432 bytes", "32 MB",
// "32MiB", "40.1 MB".
var capSizes = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(bytes|b|kb|kib|mb|mib|gb|gib)\b`)

// statedCap is the cap a refusal names: the smallest size in its words
// under what was sent (the body's own size is often named beside it). A
// megabyte is 2^20 bytes, as "32 MB … (33554432 bytes)" counts it; the
// request is fitted well under the cap anyway (capRoom). 0 when it names
// none.
func statedCap(errBody []byte, sent int) int {
	least := 0
	for _, m := range capSizes.FindAllStringSubmatch(string(wordsOf(errBody)), -1) {
		f, err := strconv.ParseFloat(m[1], 64)
		if err != nil || f <= 0 {
			continue
		}
		unit := map[string]float64{"bytes": 1, "b": 1, "kb": 1 << 10, "kib": 1 << 10, "mb": 1 << 20, "mib": 1 << 20, "gb": 1 << 30, "gib": 1 << 30}[strings.ToLower(m[2])]
		n := int(f * unit)
		if n < 1<<10 || n >= sent {
			continue // not a body's cap, or the body itself
		}
		if least == 0 || n < least {
			least = n
		}
	}
	return least
}

// bodyCaps are the byte caps providers refused requests over, by
// provider id, for as long as magpie runs.
type bodyCaps struct {
	mu sync.Mutex
	m  map[string]int
}

func (c *bodyCaps) of(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[id]
}

// learn keeps the smaller of n and the cap known before.
func (c *bodyCaps) learn(id string, n int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]int{}
	}
	if old := c.m[id]; old > 0 && old <= n {
		return old
	}
	c.m[id] = n
	return n
}

// capRoom is what a request is fitted to under a cap: three quarters of
// it. capNear is how near the cap a request may come before it is fitted.
func capRoom(n int) int { return n / 4 * 3 }
func capNear(n int) int { return n / 10 * 9 }

// withinCap is body as it goes to p: fitted under the cap p refused a
// larger request over, when one is known and body comes near it.
func (s *Server) withinCap(p provider.Provider, to provider.Protocol, path string, body []byte) []byte {
	out, left := s.capFit(p, to, path, body)
	if left > 0 {
		log.Printf("%s: the request is %s, over the %s it takes in one request; the %d oldest images left out (%s)", p.Name, mb(len(body)), mb(s.bodyCaps.of(p.ID)), left, mb(len(out)))
	}
	return out
}

// capFit is withinCap without its log line: body fitted under p's known
// cap, and how many images were left out for it.
func (s *Server) capFit(p provider.Provider, to provider.Protocol, path string, body []byte) ([]byte, int) {
	n := s.bodyCaps.of(p.ID)
	if n <= 0 || len(body) <= capNear(n) || countsTokens(path) {
		return body, 0
	}
	out, left, ok := withoutOldImages(to, body, capRoom(n))
	if !ok {
		return body, 0
	}
	return out, left
}

// refitOverCap is forward's answer to res, a refusal of body for its
// bytes (byteCapRefusal): the cap learned, and the request sent again
// with its oldest images left out, as often as that brings it lower and
// is refused again. When nothing more can go, the refusal is said as a
// 413 that names the size, the cap and what was kept.
func (s *Server) refitOverCap(ctx context.Context, p provider.Provider, to provider.Protocol, path string, body []byte, in http.Header, res *http.Response) (*http.Response, error) {
	var err error
	for range 3 {
		if res.StatusCode < 400 || countsTokens(path) {
			return res, nil
		}
		b := peekError(res)
		// body as it came is never smaller than what was sent: a page
		// to a small one is ruled out before body is fitted
		page := !byteCapRefusal(res.StatusCode, b)
		if page && !pageOverSize(res.StatusCode, b, len(body)) {
			return res, nil
		}
		sent := s.capSent(p, to, path, body)
		if page && sent < pageCapLeast {
			return res, nil
		}
		n := statedCap(b, sent)
		if n == 0 {
			n = capRoom(sent) // it said no number: less than was sent
		}
		if page {
			// a page is a guess at a cap, not a vendor's word: it isn't
			// kept for the provider's later requests
			if k := s.bodyCaps.of(p.ID); k > 0 && k < n {
				n = k
			}
		} else {
			n = s.bodyCaps.learn(p.ID, n)
		}
		fitted, left, ok := withoutOldImages(to, body, capRoom(n))
		if !ok || len(fitted) >= sent {
			res.Body.Close()
			return overCap(res, b, p, len(body), statedCap(b, len(body)), images(to, body), page), nil
		}
		log.Printf("%s refused a request of %s (HTTP %d: %s); asking again with the %d oldest images left out (%s)", p.Name, mb(sent), res.StatusCode, provider.APIError(b, ""), left, mb(len(fitted)))
		res.Body.Close()
		// fitted is under capNear already, so forwardOnce sends it as it is
		if res, err = s.forwardOnce(ctx, p, to, path, fitted, in); err != nil {
			return res, err
		}
	}
	if res.StatusCode >= 400 {
		b := peekError(res)
		page := !byteCapRefusal(res.StatusCode, b)
		if !page || pageOverSize(res.StatusCode, b, len(body)) && s.capSent(p, to, path, body) >= pageCapLeast {
			// refused as often as it was asked smaller: said as a 413, so
			// Codex's compaction asks as text (summaryTooLarge)
			res.Body.Close()
			return overCap(res, b, p, len(body), statedCap(b, len(body)), images(to, body), page), nil
		}
	}
	return res, nil
}

// capSent is how many bytes forwardOnce sent for body.
func (s *Server) capSent(p provider.Provider, to provider.Protocol, path string, body []byte) int {
	was, _ := s.capFit(p, to, path, codexImagesFit(p, to, body))
	return len(was)
}

// peekError is the first MiB of res's error body, res.Body left to read
// it all again.
func peekError(res *http.Response) []byte {
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	res.Body = readCloser{io.MultiReader(bytes.NewReader(b), res.Body), res.Body}
	return b
}

// overCap is a byte-cap refusal said as a 413 with what it is about: the
// request's size, the provider's cap when it named one, and that leaving
// out its earlier images didn't bring it under.
// page is a refusal said as a proxy's page (pageOverSize), which is named
// for what it most likely was.
func overCap(res *http.Response, errBody []byte, p provider.Provider, size, n, imgs int, page bool) *http.Response {
	said := provider.APIError(errBody, http.StatusText(res.StatusCode))
	if page {
		what := "an empty reply"
		if w, ok := provider.WebPage(errBody); ok {
			what = w
		}
		said = fmt.Sprintf("%s answered %s; a proxy in front of it answers so for a request larger than it takes", p.Name, what)
	}
	limit := "what " + p.Name + " takes in one request"
	if n > 0 {
		limit = fmt.Sprintf("the %s %s takes in one request", mb(n), p.Name)
	}
	why := "it has no earlier image to leave out"
	if imgs > 0 {
		why = "even with its earlier images left out; the latest message's images and every text, tool output included, are kept"
	}
	msg := fmt.Sprintf("%s (HTTP %d: the request is %s, over %s, %s)", said, res.StatusCode, mb(size), limit, why)
	out, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": "request_too_large", "message": msg}})
	h := res.Header.Clone()
	h.Set("Content-Type", "application/json")
	h.Del("Content-Length")
	h.Del("Content-Encoding")
	return &http.Response{
		Status: "413 Request Entity Too Large", StatusCode: http.StatusRequestEntityTooLarge,
		Proto: res.Proto, ProtoMajor: res.ProtoMajor, ProtoMinor: res.ProtoMinor,
		Header: h, Body: io.NopCloser(bytes.NewReader(out)), ContentLength: int64(len(out)), Request: res.Request,
	}
}

// imageLeftOut is what stands where an image was left out. It goes to the
// vendor, so it names neither magpie nor the vendor.
const imageLeftOut = "[An earlier image was left out here to keep this request under its size limit.]"

// capImage is one image of a request's history: where its JSON object sits
// and what it is replaced with.
type capImage struct {
	at, end int
	with    string
}

// historyOf is where a protocol's conversation sits in its body.
func historyOf(to provider.Protocol) string {
	switch to {
	case provider.Responses:
		return "input"
	case provider.Gemini:
		return "contents"
	}
	return "messages"
}

// imageWith is the part an image of the protocol is replaced with, ""
// when part is no image.
func imageWith(to provider.Protocol, part gjson.Result) string {
	text, _ := json.Marshal(imageLeftOut)
	switch to {
	case provider.Responses:
		if part.Get("type").String() == "input_image" {
			return `{"type":"input_text","text":` + string(text) + `}`
		}
	case provider.Chat:
		if t := part.Get("type").String(); t == "image_url" || t == "input_image" {
			return `{"type":"text","text":` + string(text) + `}`
		}
	case provider.Anthropic:
		if part.Get("type").String() == "image" {
			return `{"type":"text","text":` + string(text) + `}`
		}
	case provider.Gemini:
		for _, k := range []string{"inlineData", "inline_data"} {
			d := part.Get(k)
			mt := d.Get("mimeType").String() + d.Get("mime_type").String()
			if d.IsObject() && strings.HasPrefix(mt, "image/") {
				return `{"text":` + string(text) + `}`
			}
		}
	}
	return ""
}

// latestUser is the index of the history's latest user message: one the
// user wrote, not a tool's results (Anthropic's tool_result blocks,
// Gemini's functionResponse parts, Responses' function_call_output), nor
// Codex's compaction prompt that codexCompact puts last. -1 for none.
func latestUser(to provider.Protocol, items []gjson.Result) int {
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		if it.Get("role").String() != "user" {
			continue
		}
		if to == provider.Responses && it.Get("type").Exists() && it.Get("type").String() != "message" {
			continue
		}
		content := it.Get("content")
		if to == provider.Gemini {
			content = it.Get("parts")
		}
		said, own := "", !content.IsArray()
		if !content.IsArray() {
			said = content.String()
		}
		content.ForEach(func(_, part gjson.Result) bool {
			switch {
			case part.Get("type").String() == "tool_result" || part.Get("functionResponse").Exists() || part.Get("function_response").Exists():
			default:
				own = true
				said += part.Get("text").String()
			}
			return true
		})
		if own && strings.TrimSpace(said) != strings.TrimSpace(codexCompactPrompt) {
			return i
		}
	}
	return -1
}

// historyImages are the images of body's history, oldest first, that may
// be left out: inline ones (of 1 KB or more, which a link never is), not
// the latest user message's.
func historyImages(to provider.Protocol, body []byte) []capImage {
	var items []gjson.Result
	getIn(body, historyOf(to)).ForEach(func(_, it gjson.Result) bool {
		items = append(items, it)
		return true
	})
	keep := latestUser(to, items)
	var out []capImage
	var walk func(v gjson.Result, depth int)
	walk = func(v gjson.Result, depth int) {
		if depth > 6 || !v.IsObject() && !v.IsArray() {
			return
		}
		if v.IsObject() {
			if with := imageWith(to, v); with != "" {
				end := v.Index + len(v.Raw)
				if len(v.Raw) >= 1<<10 && v.Index > 0 && end <= len(body) && string(body[v.Index:end]) == v.Raw {
					out = append(out, capImage{at: v.Index, end: end, with: with})
				}
				return
			}
		}
		v.ForEach(func(_, c gjson.Result) bool {
			walk(c, depth+1)
			return true
		})
	}
	for i, it := range items {
		if i != keep {
			walk(it, 0)
		}
	}
	return out
}

// images counts what historyImages finds.
func images(to provider.Protocol, body []byte) int { return len(historyImages(to, body)) }

// withoutOldImages is body under most bytes with the fewest of its oldest
// history images left out, each replaced by imageLeftOut; the rest of it
// byte for byte as it came. ok is false, and body as it came, when
// leaving every one out doesn't bring it under.
func withoutOldImages(to provider.Protocol, body []byte, most int) (out []byte, left int, ok bool) {
	if len(body) <= most {
		return body, 0, true
	}
	imgs := historyImages(to, body)
	need, n := len(body)-most, 0
	for n < len(imgs) && need > 0 {
		need -= imgs[n].end - imgs[n].at - len(imgs[n].with)
		n++
	}
	if need > 0 {
		return body, 0, false
	}
	var b bytes.Buffer
	b.Grow(len(body))
	last := 0
	for _, im := range imgs[:n] {
		b.Write(body[last:im.at])
		b.WriteString(im.with)
		last = im.end
	}
	b.Write(body[last:])
	return b.Bytes(), n, true
}
