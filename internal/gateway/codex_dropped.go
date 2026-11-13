package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/yetone/magpie/internal/lazyre"
)

// edgeReset is the 503 ChatGPT's front (Cloudflare, then Envoy) answers,
// as text, when the backend behind it closed the connection before any
// reply.
var edgeReset = lazyre.New(`(?i)upstream connect error or disconnect/reset before headers`)

// edgeUnbuffered is the 507 the same Envoy answers when its first try at
// the backend failed and the request is more than it buffers to try again
// (Envoy's router: "exceeded request buffer limit while retrying
// upstream"). The backend's failure behind it is the reset above, which
// Envoy would have retried for a smaller body: so it is read as that
// reset, at any size, since the 507 itself says the body was large.
// sparrowu93 (#2034, pi) was told the 507 as it came, which pi doesn't
// ask again for.
var edgeUnbuffered = lazyre.New(`(?i)exceeded request buffer limit while retrying upstream`)

// codexDropBody is the request size from which that 503 may be about the
// request. ylorn (Discord, v0.1.1179) got it for every Codex turn a PNG
// screenshot took past about 1 MiB, also without magpie. It isn't a limit:
// bodies of 0.5 to 20 MiB were answered from here (a2b96139c), and
// ScarfQian's (#1628) came back 200 on the ninth try after eight of these
// 503s through another tool. "remote connection failure" is Envoy failing
// to reach the backend, which comes and goes.
const codexDropBody = 1 << 20

// codexResetSaid is in what a ChatGPT account's reply says once
// codexDropped has read it as that 503 to a request this large: what the
// accounts loop tells it by, through its own error shape, to ask again
// without resting anybody.
const codexResetSaid = "This usually passes, so magpie asked again without resting the account."

// codexDropped is the reply, still the 503 it is, that a ChatGPT Codex
// backend gave a request of size bytes when it closed the connection
// before answering one this large. It says what happened and, should it
// keep happening, what makes the request smaller. It stays a 503 so
// magpie and then Codex ask again: told 413, Codex never does, and the
// next turn carries the same images, so the conversation was stuck on a
// drop the next try may not get (#1628). A smaller request's reset is left
// as it came.
func codexDropped(res *http.Response, size int) *http.Response {
	if res == nil {
		return res
	}
	if !edgeMayDrop(res.StatusCode, size) {
		return res
	}
	unbuffered := res.StatusCode == http.StatusInsufficientStorage
	head, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
	if !edgeSaidDrop(res.StatusCode, head) {
		res.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(head), res.Body), res.Body}
		return res
	}
	res.Body.Close()
	what := "ChatGPT's Codex backend closed the connection before answering this %.1f MiB request (503: %s). "
	if unbuffered {
		// a 503 from here on: Codex, pi and magpie's own loop ask again
		// for a 503, and none of them for a 507
		what = "ChatGPT's Codex backend failed this %.1f MiB request, and its front couldn't try again, as the request is more than it holds to retry (507: %s). "
	}
	msg := fmt.Sprintf(what+
		codexResetSaid+" "+
		"If it keeps happening in this conversation, its images may be why: run /compact, or remove or shrink the images (a JPEG in place of a large PNG screenshot).",
		float64(size)/(1<<20), bytes.TrimSpace(head))
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "server_error"}})
	h := res.Header.Clone()
	for _, k := range []string{"Content-Type", "Content-Length", "Content-Encoding", "Transfer-Encoding"} {
		h.Del(k)
	}
	h.Set("Content-Type", "application/json")
	out := *res
	out.StatusCode, out.Status = http.StatusServiceUnavailable, "503 Service Unavailable"
	out.Header, out.Body, out.ContentLength = h, io.NopCloser(bytes.NewReader(b)), int64(len(b))
	return &out
}

// edgeMayDrop says a reply of status to a request of size bytes may be
// ChatGPT's front dropping it for its size: a 503 to one of at least
// codexDropBody, or a 507 to any (the 507 says the body was large).
func edgeMayDrop(status, size int) bool {
	return status == http.StatusInsufficientStorage || (status == http.StatusServiceUnavailable && size >= codexDropBody)
}

// edgeSaidDrop says the words of such a reply, head, are the front's own:
// Envoy's reset for a 503, its buffer limit for a 507.
func edgeSaidDrop(status int, head []byte) bool {
	if status == http.StatusInsufficientStorage {
		return edgeUnbuffered.Match(head)
	}
	return edgeReset.Match(head)
}

// codexReset says a failure is a large request's reset codexDropped read:
// no account's fault, and asked again.
func codexReset(status int, body []byte) bool {
	return status == http.StatusServiceUnavailable && bodyHas(body, codexResetSaid)
}
