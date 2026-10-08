package gateway

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// limitClock is the time a gateway key's limit is checked at: the window
// a request counts in and when its 429 says the key resets. A variable for
// the tests.
var limitClock = time.Now

// keyLimited holds a gateway key's requests to its limit (#585, see
// package budget for the rules): one that would go over is refused with a
// 429 before any provider is asked, saying the key, the limit and when it
// resets; one let in holds a reservation until it ends. Reads, the
// bridge's tool callbacks and counting tokens cost nothing and pass.
func keyLimited(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who := access.Caller(r.Context())
		if !who.Limit.Limited() || r.Method != http.MethodPost || strings.HasPrefix(r.URL.Path, "/_magpie/") || strings.HasPrefix(r.URL.Path, "/mcp/") || strings.HasSuffix(r.URL.Path, "/count_tokens") {
			next.ServeHTTP(w, r)
			return
		}
		size, model := r.ContentLength, ""
		// a compressed body (#1223) is reserved at its decoded size and model
		if enc := r.Header.Get("Content-Encoding"); who.Limit.Cost > 0 || size < 0 || (enc != "" && !strings.EqualFold(enc, "identity")) {
			b, status, err := readBoundedRequestBody(w, r, requestLimits{})
			if err != nil {
				writeError(w, protoOfPath(r.URL.Path), status, err.Error())
				return
			}
			r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(b)), int64(len(b))
			size, model = int64(len(b)), budget.ModelOf(b)
		}
		now := limitClock()
		release, refused := budget.Reserve(who, size, model, now)
		if refused != nil {
			msg := refused.Error()
			w.Header().Set("Retry-After", strconv.Itoa(refused.RetryAfter(now)))
			// the Anthropic and OpenAI SDKs would otherwise retry a 429
			w.Header().Set("X-Should-Retry", "false")
			w.Header().Set("X-Magpie-Limit-Reset", refused.Reset.Format(time.RFC3339))
			writeError(w, protoOfPath(r.URL.Path), http.StatusTooManyRequests, msg)
			c := callerOf(r)
			appendUsage(r, usage.Record{Time: now, Agent: c.agent, Via: c.via, Requested: model, Status: http.StatusTooManyRequests, Rejected: true,
				Error: msg, ErrType: "gateway_key_limit", Endpoint: r.URL.Path, Session: sessionOf(r.Header), NativeSession: nativeSessionOf(r.Header)})
			return
		}
		defer release()
		next.ServeHTTP(w, r)
	})
}

// protoOfPath is the API a path is of, for an error said its way.
func protoOfPath(path string) provider.Protocol {
	switch {
	case strings.HasSuffix(path, "/messages"):
		return provider.Anthropic
	case strings.HasPrefix(path, "/v1beta/"):
		return provider.Gemini
	case strings.HasSuffix(path, "/responses"):
		return provider.Responses
	}
	return provider.Chat
}

// keyLimit answers GET /v1/magpie/limit: the calling gateway key's limit,
// what it has used and when it resets; {"limited":false} for a key with
// none or a request without one.
func (s *Server) keyLimit(w http.ResponseWriter, r *http.Request) {
	who := access.Caller(r.Context())
	if who.KeyID == "" || !who.Limit.Limited() {
		writeJSON(w, 200, map[string]any{"limited": false, "key": who.KeyName})
		return
	}
	st := budget.Of(access.Key{ID: who.KeyID, Name: who.KeyName, Limit: who.Limit}, limitClock())
	writeJSON(w, 200, map[string]any{"limited": true, "key": who.KeyName, "limit": st})
}
