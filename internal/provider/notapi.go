package provider

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/lazyre"
)

// NotAnAPIReply turns a 2xx reply that can't be a model API's answer into
// the 502 it stands for: a web page (a sign-in page, Cloudflare's
// challenge, a relay's own error page or its site, served 200 text/html),
// a body that is empty, or one sent as JSON that doesn't begin as JSON.
// Each was taken for an answer: relayed to the agent as it came, logged as
// served with nothing in or out, the group's next member never asked —
// Codex was handed a text/html page from a relay and failed with "stream
// closed before response.completed" (#1012). The gateway reads every reply
// through it, and a test does too: a test that took a relay's site for an
// answer said a model answered while nothing reached the API (Neo on
// Discord: a relay's Gemini models were green, the relay logged no
// request, and Antigravity couldn't use them). Read as a 502, it is a
// failure like any other before the reply begins. Only the reply's first
// bytes are looked at, which a stream waits for anyway; a read that fails
// is left to the reader, as it was. name prefixes the reason, for a caller
// that doesn't put the provider's name before it.
func NotAnAPIReply(res *http.Response, name string) *http.Response {
	if res == nil || res.StatusCode < 200 || res.StatusCode >= 300 || res.StatusCode == http.StatusNoContent || res.Body == nil {
		return res
	}
	ct := strings.ToLower(res.Header.Get("Content-Type"))
	if strings.HasPrefix(ct, "text/html") {
		return asBadGateway(res, name, servedPage(res, ct))
	}
	br := bufio.NewReaderSize(res.Body, 4<<10)
	res.Body = bufferedBody{br, res.Body}
	if _, err := br.Peek(1); err == io.EOF {
		return asBadGateway(res, name, "answered "+res.Status+" with nothing in it, not an API reply")
	} else if err != nil {
		return res
	}
	if ce := res.Header.Get("Content-Encoding"); ce != "" && ce != "identity" {
		return res // compressed: its first bytes say nothing of what it is
	}
	head, _ := br.Peek(br.Buffered())
	head = bytes.TrimLeft(head, " \t\r\n\ufeff")
	switch {
	case HTMLStart.Match(head) && !strings.Contains(ct, "xml"):
		return asBadGateway(res, name, servedPage(res, ct))
	case strings.Contains(ct, "json") && len(head) > 0 && head[0] != '{' && head[0] != '[' && !EventsStart(head):
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<10))
		s := strings.TrimSpace(string(b))
		if r := []rune(s); len(r) > 200 {
			s = string(r[:200]) + "…"
		}
		return asBadGateway(res, name, "answered "+res.Status+" with a reply that isn't JSON, not an API reply: "+s)
	}
	return res
}

// EventsStart tells whether a body begins as server-sent events: a stream
// a vendor labels JSON is still its answer (eventStream reads it so).
func EventsStart(head []byte) bool {
	for _, pfx := range []string{"event:", "data:", ":"} {
		if bytes.HasPrefix(head, []byte(pfx)) {
			return true
		}
	}
	return false
}

// HTMLStart is how a web page begins.
var HTMLStart = lazyre.New(`(?i)^<(!doctype\s+html|html[\s>]|head[\s>]|body[\s>])`)

// pageWhy is in every reason NotAnAPIReply gives for a web page served in
// place of a reply, and in no other (ServedPage).
const pageWhy = " with a web page ("

// ServedPage reports whether msg, an error NotAnAPIReply's 502 carries,
// says a web page was served in place of a reply: a relay's site, which
// answers a path its API isn't at (missingV1).
func ServedPage(msg string) bool { return strings.Contains(msg, pageWhy) }

// servedPage says what a page served in place of a reply was, by its
// title, and for a firewall's block page what to do about it.
func servedPage(res *http.Response, ct string) string {
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if ct == "" {
		ct = "no Content-Type"
	}
	why := "answered " + res.Status + pageWhy + strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]) + "), not an API reply"
	if t := PageTitle(b); t != "" {
		why += ": “" + t + "”"
	}
	if EdgeBlocked(b) {
		why += " — " + BlockedHint
	}
	return why
}

// asBadGateway is res as the 502 it stands for, saying why in an error
// body every reader of one finds its message in.
func asBadGateway(res *http.Response, name, why string) *http.Response {
	io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	msg := name + why
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "bad_gateway"}, "message": msg})
	h := res.Header.Clone()
	for _, k := range []string{"Content-Type", "Content-Length", "Content-Encoding", "Transfer-Encoding"} {
		h.Del(k)
	}
	h.Set("Content-Type", "application/json")
	out := *res
	out.StatusCode, out.Status = http.StatusBadGateway, "502 Bad Gateway"
	out.Header, out.Body, out.ContentLength = h, io.NopCloser(bytes.NewReader(b)), int64(len(b))
	return &out
}

// bufferedBody reads through a buffer in front of a body, and closes the
// body.
type bufferedBody struct {
	io.Reader
	io.Closer
}

// AsBadGateway is res as a 502 saying name and why (asBadGateway).
func AsBadGateway(res *http.Response, name, why string) *http.Response {
	return asBadGateway(res, name, why)
}

// RetryMissingV1 hands on res, the answer to req; or, when res is a web
// page served in place of a reply (NotAnAPIReply's 502) and req went to
// p's OpenAI base without the /v1 a relay serves its API under
// (v1Missable), the answer to req asked again under /v1 when that is an
// API's reply, and p's base set there (setV1); the API's refusal there,
// with what to change added to its message; else res. A
// relay's site (new-api's, one-api's) answers any path its API isn't at
// with its own page, so a base typed without /v1 was answered 502 and
// nothing told the user why (sarosaka1023 on X). do sends a request as
// req was sent.
func RetryMissingV1(p Provider, proto Protocol, req *http.Request, res *http.Response, do func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	if res == nil || res.StatusCode != http.StatusBadGateway || res.Body == nil || req == nil {
		return res, nil
	}
	base := p.v1Missable(proto)
	u := *req.URL
	u.RawQuery, u.Fragment = "", ""
	rest, ok := strings.CutPrefix(u.String(), base+"/")
	if base == "" || !ok || strings.HasPrefix(rest, "v1/") {
		return res, nil
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(b))
	if !ServedPage(APIError(b, res.Status)) {
		return res, nil
	}
	if req.GetBody == nil {
		return res, nil
	}
	body, err := req.GetBody()
	if err != nil {
		return res, nil
	}
	again := req.Clone(req.Context())
	again.Body = body
	again.URL.Path = strings.TrimSuffix(again.URL.Path, "/"+rest) + "/v1/" + rest
	again.URL.RawPath = ""
	got, err := do(again)
	if err != nil {
		return res, nil
	}
	got = NotAnAPIReply(got, "")
	if got.StatusCode < 300 {
		p.setV1(base)
		return got, nil
	}
	gb, _ := io.ReadAll(io.LimitReader(got.Body, 64<<10))
	got.Body.Close()
	if got.StatusCode == http.StatusNotFound || ServedPage(APIError(gb, got.Status)) {
		return res, nil // no API there either
	}
	// the API is there, and refused (a key it doesn't take, a limit): its
	// refusal is the answer, rested for as any, and says where it is
	gb = withMessage(gb, APIError(gb, got.Status)+" — "+MissingV1Hint(base))
	got.Body = io.NopCloser(bytes.NewReader(gb))
	got.ContentLength = int64(len(gb))
	if got.Header == nil {
		got.Header = http.Header{}
	}
	got.Header.Set("Content-Length", strconv.Itoa(len(gb)))
	got.Header.Del("Content-Encoding")
	return got, nil
}
