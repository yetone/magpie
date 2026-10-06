package gateway

import (
	"bytes"
	"net/http"
	"strings"
)

// Claude Code signed in to claude.ai, its base URL magpie's and no key
// set, sends its own sign-in as its key (Authorization: Bearer
// sk-ant-oat…), which magpie never passes on. A 401 it reads as that
// sign-in refused: it renews the sign-in before each of its retries, ten
// of them for one request, and then asks to /login; a 403 saying "OAuth
// token has been revoked" the same. Whatever magpie answers such a request
// with is never about that sign-in — a provider refused magpie's
// credential, or one of magpie's own Claude accounts lapsed — so it is
// told as the 502 it is: the provider's message as it was, and the usage
// log and Recent calls keeping the provider's status.

// claudeSignIn is a request whose key is a Claude sign-in: an Anthropic
// OAuth access token.
func claudeSignIn(r *http.Request) bool {
	a := strings.TrimSpace(r.Header.Get("Authorization"))
	return len(a) > 7 && strings.EqualFold(a[:7], "Bearer ") && strings.HasPrefix(strings.TrimSpace(a[7:]), "sk-ant-oat")
}

// keepsSignIn is h, a request with a Claude sign-in (claudeSignIn) told a
// 401 as a 502, and a 403 too where it says the OAuth token was revoked.
func keepsSignIn(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !claudeSignIn(r) {
			h(w, r)
			return
		}
		sw := &signInWriter{ResponseWriter: w}
		defer sw.release()
		h(sw, r)
	}
}

// signInWriter is keepsSignIn's: a 403 is held until its body, which says
// whether it was the revoked token — magpie writes an error whole, in one
// write.
type signInWriter struct {
	http.ResponseWriter
	held  bool // a 403, waiting for its body
	wrote bool // the status went
}

func (w *signInWriter) WriteHeader(code int) {
	if w.wrote || w.held {
		return
	}
	switch code {
	case http.StatusUnauthorized:
		code = http.StatusBadGateway
	case http.StatusForbidden:
		w.held = true
		return
	}
	w.wrote = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *signInWriter) Write(b []byte) (int, error) {
	if w.held {
		if len(b) == 0 {
			return 0, nil
		}
		code := http.StatusForbidden
		if bytes.Contains(b, []byte("OAuth token has been revoked")) {
			code = http.StatusBadGateway
		}
		w.held = false
		w.ResponseWriter.WriteHeader(code)
	}
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

func (w *signInWriter) Flush() {
	w.release()
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap is for http.ResponseController.
func (w *signInWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// release sends a 403 held with no body after all.
func (w *signInWriter) release() {
	if w.held {
		w.held, w.wrote = false, true
		w.ResponseWriter.WriteHeader(http.StatusForbidden)
	}
}
