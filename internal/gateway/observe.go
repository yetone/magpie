package gateway

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// observed is a request the registered observers watch (usage.Observer,
// the gateway's observer point): what each one's Begin answered.
type observed struct {
	watches []usage.Watch
	routeID int64
	// keyName is the gateway key the request came with, by the name the
	// user gave it (access.Identity.KeyName), never its secret; "" without
	// one. Each span carries it as the ledger's row does (CallerKeyName).
	keyName string
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
	who := access.Caller(r.Context())
	q := usage.Request{Header: r.Header, Agent: agent, Session: observedSession(r.Header), Kind: observedKind(agent, kind, body),
		Plain: local(r) && who.KeyID == "" && callerOf(r).via == ""}
	o := &observed{keyName: who.KeyName}
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

// inferredKind is the kind of Claude Code's helper requests it names
// nowhere in the request: its auto mode classifier and its AgentSummary,
// told by their content. They are recorded as inferred
// (usage.InferredSuffix), never as the client's word; "" for any other.
func inferredKind(in http.Header, from provider.Protocol, body []byte) string {
	if !fromClaudeCode(in) || from != provider.Anthropic {
		return ""
	}
	switch {
	case autoModeClassifierAsk(body):
		return usage.KindAutoMode + usage.InferredSuffix
	case agentSummaryAsk(body):
		return usage.KindAgentSummary + usage.InferredSuffix
	}
	return ""
}

// agentSummaryAsk reports whether body, a Messages API request, is Claude
// Code's AgentSummary: the sub-agent's conversation with a last user
// message that ends in the ask for a few words of progress ("Describe
// your most recent action in 3-5 words."). The bytes are looked at first:
// every request passes here.
func agentSummaryAsk(body []byte) bool {
	if !bodyHas(body, "3-5 words") {
		return false
	}
	req, err := parseAnthropic(body)
	if err != nil || len(req.Messages) == 0 {
		return false
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != "user" || len(last.Parts) == 0 {
		return false
	}
	p := last.Parts[len(last.Parts)-1]
	if p.Kind != Text {
		return false
	}
	ask := strings.TrimSpace(p.Text)
	return strings.HasPrefix(ask, "Describe your ") && strings.Contains(ask, " in 3-5 words")
}

func (o *observed) end(call Call, millis int64) {
	if o == nil {
		return
	}
	rec := usage.Record{RouteID: o.routeID, Time: call.Time, Agent: call.Agent, Model: call.Model, Status: call.Status, Millis: millis, CallerKeyName: o.keyName}
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
		Millis: time.Since(attempt.Start).Milliseconds(), Status: attempt.Status, TTFT: attempt.TTFT, FirstText: attempt.FirstText, FirstReasoning: attempt.FirstReasoning, ReasoningHidden: attempt.ReasoningHidden, CallerKeyName: o.keyName}
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
