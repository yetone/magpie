package provider

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
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

// servedPage says what a page served in place of a reply was, by its
// title, and for a firewall's block page what to do about it.
func servedPage(res *http.Response, ct string) string {
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if ct == "" {
		ct = "no Content-Type"
	}
	why := "answered " + res.Status + " with a web page (" + strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]) + "), not an API reply"
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
