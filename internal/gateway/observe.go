package gateway

import (
	"context"
	"net/http"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/usage"
)

// observed is a request the registered observers watch (usage.Observer,
// the gateway's observer point): what each one's Begin answered.
type observed struct {
	watches []usage.Watch
	routeID int64
}

type observedKey struct{}

// observeRequest asks each observer whether it watches the request. With
// none watching it is nil, and its calls do nothing.
func observeRequest(r *http.Request, kind string, body []byte) (*http.Request, *observed) {
	observers := usage.Observers()
	if len(observers) == 0 {
		return r, nil
	}
	// the agent it is recorded as, another magpie's own (callerFrom)
	agent := callerOf(r).agent
	q := usage.Request{Header: r.Header, Agent: agent, Session: observedSession(r.Header), Kind: observedKind(agent, kind, body),
		Plain: local(r) && access.Caller(r.Context()).KeyID == "" && callerOf(r).via == ""}
	o := &observed{}
	for _, ob := range observers {
		if w := ob.Begin(q); w != nil {
			o.watches = append(o.watches, w)
		}
	}
	if len(o.watches) == 0 {
		return r, nil
	}
	return r.WithContext(context.WithValue(r.Context(), observedKey{}, o)), o
}

// Prefer the native client session: routing overrides need not name its store.
func observedSession(h http.Header) string {
	if id := nativeSessionOf(h); id != "" {
		return id
	}
	return sessionOf(h)
}

// Claude's tool-less small tasks (titles, suggestions, haiku helpers) may
// reuse the parent session but aren't recorded in its transcript. Keep their
// gateway spans; a model name alone doesn't identify a helper or a subagent.
func observedKind(agent, kind string, body []byte) string {
	if kind == "" && (agent == "claude" || agent == "claude-desktop") && small(body) && !hasTools(body) {
		return "auxiliary"
	}
	return kind
}

func (o *observed) end(call Call, millis int64) {
	if o == nil {
		return
	}
	rec := usage.Record{RouteID: o.routeID, Time: call.Time, Agent: call.Agent, Model: call.Model, Status: call.Status, Millis: millis}
	canceledAs(&rec, call.Status, call.Error)
	for _, w := range o.watches {
		w.End(rec)
	}
}

// Called for every routing attempt, including unbilled failures and retries.
// Its timing is the attempt's, not the whole request's ledger duration.
func (o *observed) attempt(call Call, attempt Try, providerID, effort string, held *holdWriter, capture *captureResponseWriter) {
	if o == nil {
		return
	}
	u := call.Usage.billed()
	rec := usage.Record{RouteID: o.routeID, Time: attempt.Start, Agent: call.Agent,
		Provider: providerID, Model: attempt.Model, Requested: call.Model, Served: u.Served, Effort: effort,
		Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, CacheUnknown: u.cacheNotCounted(), Reasoning: u.Reasoning,
		Millis: time.Since(attempt.Start).Milliseconds(), Status: attempt.Status, TTFT: attempt.TTFT, FirstText: attempt.FirstText, FirstReasoning: attempt.FirstReasoning, ReasoningHidden: attempt.ReasoningHidden}
	canceledAs(&rec, attempt.Status, attempt.Error)
	if wanted := usage.ObservedBodies(); wanted != usage.NoBodies {
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
		if wanted == usage.WholeBodies {
			if !held.passing {
				bodies.wholeOut = held.held.Bytes()
				bodies.wholeOutCut = false
			} else if capture.whole != nil {
				bodies.wholeOutCut = capture.whole.cut()
				if data, err := capture.whole.read(); err == nil {
					bodies.wholeOut = data
				}
			}
		}
		withBodies(&rec, &bodies)
	}
	for _, w := range o.watches {
		w.Attempt(rec)
	}
}
