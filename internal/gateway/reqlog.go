package gateway

import (
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// What the usage log keeps of a call besides its tokens: the path it came
// in on, the id its vendor gave it, and, when it failed, what was said.

// errKeep is how much of a failure's message the log keeps.
const errKeep = 500

// keepMsg is a message cut to what the log keeps of it.
func keepMsg(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > errKeep {
		return string(r[:errKeep]) + "…"
	}
	return s
}

// requestID is the id a vendor's reply gives the request, by the header
// Anthropic (request-id) and everyone else (x-request-id) send it in.
func requestID(h http.Header) string {
	for _, k := range []string{"Request-Id", "X-Request-Id"} {
		if v := strings.TrimSpace(h.Get(k)); v != "" {
			return v
		}
	}
	return ""
}

// endpointOf is the path a call came in on, and the one it went out on to
// model at p when that is another protocol's, as the vendors' own docs
// write them (the vendor's base URL, which decides the rest, isn't part of
// it).
func endpointOf(r *http.Request, from, to provider.Protocol, p provider.Provider, model string) string {
	ep := r.URL.Path
	if to != "" && to != from {
		switch to {
		case provider.Chat:
			ep += " → /v1/chat/completions"
		case provider.Responses:
			ep += " → /v1/responses"
		case provider.Anthropic:
			ep += " → /v1/messages"
		default:
			if p.IsVertex() { // its path names the model as Vertex AI was asked for it
				model = provider.UpstreamNameIn(wiresOf(r.Context()), p.ID, model)
			}
			ep += " → " + upstreamPath(p, to, model)
		}
	}
	return ep
}

// failedWith fills in what a call that failed said, on a usage record. A call
// its agent canceled says so in its error type too (usage.CanceledErrType),
// the record's own word for it, which no rewording of the message changes.
func failedWith(rec *usage.Record, status int, msg, errType string) {
	if status >= 400 || msg != "" {
		if usage.CanceledBy(status, msg) {
			errType = usage.CanceledErrType
		}
		rec.Error, rec.ErrType = keepMsg(msg), errType
	}
}

// canceledAs marks the record an observer is given of a call or a try its
// agent canceled, which carries the status and not the message, as the
// agent's cancel, so an observer (the OTLP export) tells it from a failure.
func canceledAs(rec *usage.Record, status int, msg string) {
	if usage.CanceledBy(status, msg) {
		rec.ErrType = usage.CanceledErrType
	}
}
