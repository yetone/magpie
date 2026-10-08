package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// Claude Code signed in to claude.ai, its base URL magpie's and no key
// set, sends its own sign-in as its key (Authorization: Bearer
// sk-ant-oat…), which magpie never passes on. A 401 it reads as that
// sign-in refused: it renews the sign-in before each of its retries, ten
// of them for one request, and then asks to /login; a 403 saying "OAuth
// token has been revoked" the same. Any error whose message mentions
// x-api-key, whatever its status, it tells as "Not logged in · Please run
// /login" (Claude Code 2.1.293). Whatever magpie answers such a request
// with is never about that sign-in — a provider refused magpie's
// credential, or one of magpie's own Claude accounts lapsed — so it is
// told as the 502 it is, in magpie's words: "<provider> refused magpie's
// credential (HTTP 401): …", the provider's reason after it with x-api-key
// said as "API key". Any other error keeps its status, x-api-key said as
// "API key" in it too. The usage log and Recent calls keep the provider's
// status and message.

// claudeSignIn is a request whose key is a Claude sign-in: an Anthropic
// OAuth access token.
func claudeSignIn(r *http.Request) bool {
	a := strings.TrimSpace(r.Header.Get("Authorization"))
	return len(a) > 7 && strings.EqualFold(a[:7], "Bearer ") && strings.HasPrefix(strings.TrimSpace(a[7:]), "sk-ant-oat")
}

// keepsSignIn is h, a request with a Claude sign-in (claudeSignIn) told a
// 401 as a 502 in magpie's words, and a 403 too where it says the OAuth
// token was revoked; no error it is told mentions x-api-key.
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

// signInWriter is keepsSignIn's: an error's status is held until its
// body, which says whose refusal it was and is said without x-api-key —
// magpie writes an error whole, in one write, and a stream's event the
// same.
type signInWriter struct {
	http.ResponseWriter
	held  int  // an error's status, waiting for its body
	wrote bool // the status went
	told  bool // the error went in magpie's words: the rest of it is dropped
}

func (w *signInWriter) WriteHeader(code int) {
	if w.wrote || w.held != 0 {
		return
	}
	if code >= http.StatusBadRequest {
		w.held = code
		return
	}
	w.wrote = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *signInWriter) Write(b []byte) (int, error) {
	n := len(b)
	switch {
	case w.told:
		return n, nil
	case w.held != 0:
		if n == 0 {
			return 0, nil
		}
		code := w.held
		w.held = 0
		if code != http.StatusUnauthorized && (code != http.StatusForbidden || !bytes.Contains(b, []byte("OAuth token has been revoked"))) {
			// any other error, at its status
			if xAPIKey.Match(b) {
				b = xAPIKey.ReplaceAll(b, []byte("API key"))
				w.Header().Del("Content-Length")
			}
			w.ResponseWriter.WriteHeader(code)
			break
		}
		w.told = true
		h := w.Header()
		h.Del("Content-Length")
		h.Set("Content-Type", "application/json")
		w.ResponseWriter.WriteHeader(http.StatusBadGateway)
		msg := refusedMessage(b, h.Get("X-Magpie-Provider"), code)
		body, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": msg}})
		w.wrote = true
		_, err := w.ResponseWriter.Write(body)
		return n, err
	case bytes.HasPrefix(b, []byte("event: error\n")) && bytes.Contains(bytes.ToLower(b), []byte("x-api-key")):
		// a stream's error, after its 200 went
		b = xAPIKey.ReplaceAll(b, []byte("API key"))
	}
	w.wrote = true
	if _, err := w.ResponseWriter.Write(b); err != nil {
		return 0, err
	}
	return n, nil
}

var xAPIKey = regexp.MustCompile(`(?i)x-api-key`)

// refusedMessage is a provider's refusal of magpie's credential, as a
// Claude Code signed in to claude.ai is told it: b is magpie's error,
// "<provider>: <reason>".
func refusedMessage(b []byte, id string, code int) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(b, &e)
	name, reason := id, strings.TrimSpace(e.Error.Message)
	if i := strings.Index(reason, ": "); i > 0 && i <= 64 && !strings.ContainsAny(reason[:i], "\n{") {
		name, reason = reason[:i], strings.TrimSpace(reason[i+2:])
	}
	if name == "" {
		name = "The provider"
	}
	msg := fmt.Sprintf("%s refused magpie's credential (HTTP %d)", name, code)
	if reason != "" {
		msg += ": " + xAPIKey.ReplaceAllString(reason, "API key")
	}
	return msg
}

func (w *signInWriter) Flush() {
	w.release()
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap is for http.ResponseController.
func (w *signInWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// release sends a status held with no body after all: a 401 as the 502
// it is, any other as it was.
func (w *signInWriter) release() {
	if w.held != 0 {
		code := w.held
		if code == http.StatusUnauthorized {
			code = http.StatusBadGateway
		}
		w.held, w.wrote = 0, true
		w.ResponseWriter.WriteHeader(code)
	}
}
