package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/usage"
)

type otelRequestKey struct{}

type otelRequest struct {
	root                                  usage.OTelSpan
	routeID                               int64
	agent, session                        string
	sessionCandidate, decided, suppressed bool
}

// Decide once, before exporting any attempt, so a transcript discovered
// during the request cannot leave an exported attempt without its root.
func (t *otelRequest) skip() bool {
	if t == nil {
		return true
	}
	if !t.decided {
		t.suppressed = t.sessionCandidate && usage.OTelSessionFresh(t.agent, t.session)
		t.decided = true
	}
	return t.suppressed
}

func otelID(size int) string {
	b := make([]byte, size)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Accept W3C version 00 only; malformed or zero IDs start a fresh trace.
// Context is used locally and never forwarded with provider credentials.
func otelParent(value string) (traceID, parentID string) {
	parts := strings.Split(value, "-")
	if len(parts) != 4 || parts[0] != "00" || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		return "", ""
	}
	for _, part := range parts[1:] {
		if strings.ToLower(part) != part {
			return "", ""
		}
		if _, err := hex.DecodeString(part); err != nil {
			return "", ""
		}
	}
	if parts[1] == strings.Repeat("0", 32) || parts[2] == strings.Repeat("0", 16) {
		return "", ""
	}
	return parts[1], parts[2]
}

// Prefer the native client session: routing overrides need not name its store.
func otelSessionOf(h http.Header) string {
	if id := nativeSessionOf(h); id != "" {
		return id
	}
	return sessionOf(h)
}

// Claude's tool-less small tasks (titles, suggestions, haiku helpers) may
// reuse the parent session but aren't recorded in its transcript. Keep their
// gateway spans; a model name alone doesn't identify a helper or a subagent.
func otelCallKind(agent, kind string, body []byte) string {
	if kind == "" && (agent == "claude" || agent == "claude-desktop") && small(body) && !hasTools(body) {
		return "auxiliary"
	}
	return kind
}

func beginOTelRequest(r *http.Request, kind string, body []byte) (*http.Request, *otelRequest) {
	if !usage.OTelEnabled() {
		return r, nil
	}
	agent, session := agentOf(r), otelSessionOf(r.Header)
	candidate := kind == "" && local(r) && access.Caller(r.Context()).KeyID == "" && callerOf(r).via == "" && otelCallKind(agent, kind, body) == ""
	if candidate && usage.OTelSession(agent, session) {
		return r, nil
	}
	traceID, parentID := otelParent(r.Header.Get("traceparent"))
	if traceID == "" {
		traceID = otelID(16)
	}
	t := &otelRequest{root: usage.OTelSpan{TraceID: traceID, SpanID: otelID(8), ParentID: parentID, Root: true}, agent: agent, session: session, sessionCandidate: candidate}
	return r.WithContext(context.WithValue(r.Context(), otelRequestKey{}, t)), t
}

func (t *otelRequest) end(call Call, millis int64) {
	if t.skip() {
		return
	}
	t.root.End = time.Now()
	usage.ExportOTel(usage.Record{OTel: &t.root, RouteID: t.routeID, Time: call.Time, Agent: call.Agent, Model: call.Model, Status: call.Status, Millis: millis})
}

// Called for every routing attempt, including unbilled failures and retries.
// Its timing is the attempt's, not the whole request's ledger duration.
func (t *otelRequest) attempt(call Call, attempt Try, providerID, effort string, held *holdWriter, capture *captureResponseWriter) {
	if t.skip() {
		return
	}
	span := &usage.OTelSpan{TraceID: t.root.TraceID, SpanID: otelID(8), ParentID: t.root.SpanID, End: time.Now()}
	u := call.Usage
	rec := usage.Record{OTel: span, RouteID: t.routeID, Time: attempt.Start, Agent: call.Agent,
		Provider: providerID, Model: attempt.Model, Requested: call.Model, Served: u.Served, Effort: effort,
		Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, Reasoning: u.Reasoning,
		Millis: time.Since(attempt.Start).Milliseconds(), Status: attempt.Status, TTFT: attempt.TTFT, FirstText: attempt.FirstText}
	if usage.OTelBodies() {
		reply, cut := capture.body.text(), capture.body.truncated
		if !held.passing {
			data := held.held.Bytes()
			cut = len(data) > callBodyLimit
			reply = string(data[:min(len(data), callBodyLimit)])
		}
		// Apply the same capture limit and masking to held responses.
		var body capturedBody
		body.add([]byte(reply))
		bodies := call
		bodies.ResponseBody, bodies.ResponseTruncated = body.text(), cut || body.truncated
		if usage.OTelWhole() {
			if !held.passing {
				bodies.otelOut = held.held.Bytes()
				bodies.otelOutCut = false
			} else if capture.otel != nil {
				bodies.otelOutCut = capture.otel.cut()
				if data, err := capture.otel.read(); err == nil {
					bodies.otelOut = data
				}
			}
		}
		withBodies(&rec, &bodies)
	}
	usage.ExportOTel(rec)
}
