package gateway

import (
	"bytes"
	"regexp"
	"slices"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// Claude Code's auto mode checks each action with a classifier (#250).
// Behind a gateway, its first requests ask the API to do it: the
// dangerous-tool-use beta and a `safeguards` field, answered by
// `safeguard_results` in the reply: a whole reply's own, a stream's in its
// message_delta. Anthropic's own API (and a relay in front of it) gets them
// as they were sent, relayed or built (buildAnthropic), and the results
// come back as it gave them, on a stream made of a whole reply too
// (anthropicWholeEvents). Any other model neither sees them nor answers
// them (serverReview), and magpie says nothing on the API's behalf. What
// Claude Code does then is Anthropic's choice, made per account by a flag
// (tengu_bright_dragonfly, read in 2.1.312 to 2.1.316): with it off, it
// classifies locally for the rest of the session, naming the gateway in a
// notice; with it on, it has no local classifier behind a gateway at all,
// and every action the check would have judged asks for approval ("Auto
// mode can't check actions in this session … your requests go through
// 127.0.0.1:3425, which doesn't pass it through", chenxiang0010 on
// Discord). Only Anthropic can answer the check, so no route here other
// than those AutoModeReviewed names can make auto mode work there. Where no
// model Claude Code can reach through magpie is reviewed, the agent's
// wiring turns the review off in Claude Code's settings
// (CLAUDE_CODE_AUTO_MODE_SERVER=0, as Anthropic's page on the notice says to
// for a gateway that can't run the checks): with the flag off it
// classifies locally from the start and says nothing about the gateway
// (1EFLOPS on X); with it on, 2.1.314 says it has no check of its own to
// use instead. Where one is, it is left on: /model can take the same
// session to that model, which reviews it.
//
// Locally, the classifier is a request of its own on the session's model:
// the transcript in <transcript> blocks, a system prompt asking for
// <block>yes</block> or <block>no</block>, thinking turned off, and for its
// first stage 64 tokens to answer in (stopping at </block>). A model that
// reasons whatever it is told spends those 64 tokens reasoning and answers
// nothing, which Claude Code reads as no verdict and blocks the action.

// serverReviewBeta begins the beta Claude Code asks the review by
// (dangerous-tool-use-2026-09-03).
const serverReviewBeta = "dangerous-tool-use-"

func isServerReviewBeta(b string) bool { return strings.HasPrefix(b, serverReviewBeta) }

// serverReview fits auto mode's review to what a request to an Anthropic
// endpoint reaches: the betas asked, fitted to p (betas), and the body.
// Only Claude reviews actions, so a body naming another vendor's model
// (DeepSeek's, Kimi's, behind their Anthropic APIs) goes without the beta
// and the safeguards: a vendor that checks the fields it is sent turns the
// whole request away over them. Neither does a body p is no longer asked
// the beta for, having turned it away (Bedrock's "Unexpected value(s)"):
// the safeguards ask for a review that the beta turns on. A body that names
// no model (the model in Bedrock's path), or goes to Anthropic's own API,
// is Claude's.
func (s *Server) serverReview(p provider.Provider, asked []string, body []byte) ([]string, []byte) {
	model := gjson.GetBytes(body, "model").String()
	claude := model == "" || AutoModeReviewed(p, model)
	if !claude {
		asked = slices.DeleteFunc(slices.Clone(asked), isServerReviewBeta)
	}
	betas := s.betas(p, asked)
	if !claude || slices.ContainsFunc(asked, isServerReviewBeta) && !slices.ContainsFunc(betas, isServerReviewBeta) {
		if gjson.GetBytes(body, "safeguards").Exists() {
			body = withoutFields(body, "safeguards")
		}
	}
	return betas, body
}

// AutoModeReviewed reports whether a request for model on p can have its
// actions reviewed by Anthropic for Claude Code's auto mode: the Claude
// subscription (the bridge hands the review to Claude Code's own binary),
// a Claude model (Anthropic's API, a relay in front of it, Bedrock's or
// Vertex's names for it), or anything on Anthropic's own API. No other
// vendor runs Anthropic's classifier, and magpie answers none on its
// behalf: another subscription's or a plugin's Claude model (Copilot,
// Kiro, Cursor…) is that vendor's service, which turns the safeguards
// away (#1756) or drops them, so it isn't reviewed by its name alone.
func AutoModeReviewed(p provider.Provider, model string) bool {
	if provider.HostOf(p.Base(provider.Anthropic)) == "api.anthropic.com" {
		return true
	}
	if p.Account != nil {
		return p.Account.Agent == "claude"
	}
	return anthropicModel.MatchString(model)
}

// AutoModeReviewable reports whether Claude Code, asking for model at p
// through magpie, can have its actions reviewed: AutoModeReviewed, on a
// route that reaches the Messages API. A Claude model p serves only on
// Chat or Responses (a relay with no Anthropic endpoint, or one the user
// set to be asked on another API) is asked translated, and a translation
// carries neither the safeguards nor their results (nexnull on Discord).
// The agent's wiring asks this before a request is made; serverReview,
// on the Messages route already, asks AutoModeReviewed.
func AutoModeReviewable(p provider.Provider, model string) bool {
	if !AutoModeReviewed(p, model) {
		return false
	}
	if p.Account != nil {
		return true // the Claude subscription, through Claude Code's own binary
	}
	if p.Base(provider.Anthropic) == "" {
		return false
	}
	apis := p.APIs(model)
	return apis == nil || slices.Contains(apis, provider.Anthropic)
}

// classifierRoom is what a model that reasons is given on top of the
// classifier's own budget, as Claude Code gives a Claude model that can't
// turn its thinking off.
const classifierRoom = 2048

// autoModeClassifier reports whether req is Claude Code's auto mode
// classifier asking about an action.
func autoModeClassifier(req *Request) bool {
	if len(req.Tools) > 0 || !strings.Contains(req.System, "<block>") && !strings.Contains(req.System, "<severity>") {
		return false
	}
	for _, m := range req.Messages {
		if m.Role != "user" {
			continue
		}
		for _, p := range m.Parts {
			if p.Kind == Text && strings.HasPrefix(strings.TrimSpace(p.Text), "<transcript>") {
				return true
			}
		}
	}
	return false
}

// hasReply reports whether msgs hold a reply: without one, a request is
// a one-off ask however many user messages it comes in.
func hasReply(msgs []Message) bool {
	for _, m := range msgs {
		if m.Role == "assistant" {
			return true
		}
	}
	return false
}

// fitAutoModeClassifier asks a model that isn't Claude, for Claude Code's
// auto mode classifier, to reason least — none when it can go without, its
// lowest level otherwise — with room for that reasoning and the verdict.
// A model whose levels aren't known (one on a relay the user added, under
// the relay's own name for it) is asked for none too, as magpie's own ask
// (OwnEffort): left to the vendor, a model that reasons did so at its
// default over the whole policy and transcript, and Claude Code, which
// waits 60 seconds an attempt for the verdict (2.1.310), closed the
// request first (CyberCaius on Discord: every classifier request 499 on
// a new-api relay's gpt6.1-sol). One that turns none away is asked at
// low, and one that takes no effort at all without it (forwardTranslated).
func fitAutoModeClassifier(p provider.Provider, model string, req *Request) {
	if anthropicModel.MatchString(model) || !autoModeClassifier(req) {
		return
	}
	if req.MaxTokens > 0 && req.MaxTokens < classifierRoom {
		req.MaxTokens += classifierRoom
	}
	if req.Effort == "" {
		if levels := p.Efforts(model); len(levels) > 0 {
			req.Effort = fitEffort("none", levels)
		} else {
			req.Effort, req.OwnEffort = "none", true
		}
	}
}

// ownEffortField is the effort magpie added (OwnEffort), remembered in
// unfit for a model whose upstream turned the field away.
func ownEffortField(model string) string { return "own effort\x00" + model }

var (
	// effortFieldUnknown is an upstream that doesn't know the effort's
	// field: OpenAI's "Unrecognized request argument supplied:
	// reasoning_effort", a validator's "Extra inputs are not permitted"
	effortFieldUnknown = regexp.MustCompile(`(?i)(?:unrecogni[sz]ed|unknown|unsupported|unexpected|extra)\s+(?:request\s+)?(?:argument|parameter|field|property|name|key)s?(?:\s+supplied)?\W{0,4}reasoning(?:[._]effort)?\b|\breasoning(?:[._]effort)?['"\x60]?\W{0,4}(?:extra inputs are not permitted|is not (?:supported|allowed|permitted)|unsupported)`)
)

// ownEffortRefused reads a 400 to the effort magpie added (OwnEffort) as
// turned away, and whether it was the level sent (value) rather than the
// field. Anything else is the request's own fault and reaches the client.
func ownEffortRefused(status int, body []byte, sent string) (refused, value bool) {
	if !badRequest(status) || sent == "" {
		return false, false
	}
	_, named := effortRefused(body, sent)
	if !named && !effortFieldUnknown.Match(body) {
		return false, false
	}
	low := strings.ToLower(string(body))
	for _, q := range []string{"'", `"`, "`", `\"`} {
		if strings.Contains(low, q+sent+q) {
			return true, true
		}
	}
	return true, false
}

// autoModeClassifierBody is fitAutoModeClassifier for a request relayed to
// an Anthropic endpoint as it was sent: a model that isn't Claude gets the
// room to reason in. The rest of the body is left alone.
func autoModeClassifierBody(model string, body []byte) []byte {
	if anthropicModel.MatchString(model) || !autoModeClassifierAsk(body) {
		return body
	}
	asked := gjson.GetBytes(body, "max_tokens").Int()
	if asked <= 0 || asked >= classifierRoom {
		return body
	}
	return withFields(body, map[string]any{"max_tokens": asked + classifierRoom})
}

// autoModeClassifierAsk reports whether body, a Messages API request, is
// Claude Code's auto mode classifier asking about an action. The bytes
// are looked at before the body is parsed: every request passes here.
func autoModeClassifierAsk(body []byte) bool {
	if !bytes.Contains(body, []byte("<transcript>")) || !bytes.Contains(body, []byte("<block>")) && !bytes.Contains(body, []byte("<severity>")) {
		return false
	}
	req, err := parseAnthropic(body)
	return err == nil && autoModeClassifier(req)
}

// ClassifierStandIn is the model Claude Code's auto mode classifier goes
// to in place of the tier's it named, while that tier follows a main model
// /model has moved since magpie wrote it; "" leaves it as asked. Set by
// main (agent.ClassifierStandIn).
var ClassifierStandIn func(agent, model string) string

// classifierStandIn is ClassifierStandIn for a request Claude Code sent,
// when it is the auto mode classifier: Claude Code names its sonnet tier
// for it, not the session's model, so the first one after a switch in
// /model went to the model before it until magpie moved the tier.
func classifierStandIn(agent string, from provider.Protocol, asked string, body []byte) string {
	if ClassifierStandIn == nil || agent != "claude" || from != provider.Anthropic || !autoModeClassifierAsk(body) {
		return ""
	}
	if m := ClassifierStandIn(agent, asked); m != asked {
		return m
	}
	return ""
}
