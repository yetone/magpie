package gateway

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

// ---- Google Vertex AI (provider side) ----------------------------------------
//
// Vertex AI serves Gemini at streamGenerateContent, with the model in the
// path (provider.VertexPath) and Gemini's own request in the body: the one
// Code Assist carries in its envelope. Where it parts from Code Assist is in
// what it turns away. A thinking level the model doesn't have, or a level
// at all on 2.5, is a 400 (vertexEffort, vertexThinking). A call's thought
// signature goes back as Vertex AI gave it, as on every Gemini path
// (gemini_signature.go): a call made with none on a step it signed is a 400.

// buildVertex is a streamGenerateContent body for Vertex AI, which names the
// model in its path and has none in here.
func buildVertex(r *Request, model string) ([]byte, error) {
	req, err := geminiRequest(r, model, "vertex")
	if err != nil {
		return nil, err
	}
	return json.Marshal(req)
}

// vertexThinking is the thinkingConfig a request asks Vertex AI for, which
// turns away what a model can't do. Gemini 3 is sent its level, fitted to
// the model's own by vertexEffort, with no thoughts given back when the
// client turned reasoning off: the level is then the model's lowest, as
// Gemini 3 has no way to stop thinking altogether. 2.5 takes no level
// ("thinking_level is not supported by this model"): it is sent a budget
// in its own range. Turned off, Flash and Flash-Lite are sent 0, without
// thoughts asked for ("include_thoughts is only enabled when thinking is
// enabled"), and Pro, which can't stop ("does not support setting
// thinking_budget to 0"), its least. A Gemini before 2.5 doesn't think.
func vertexThinking(r *Request, model string) map[string]any {
	m := strings.ToLower(model)
	if strings.HasPrefix(m, "gemini-1") || strings.HasPrefix(m, "gemini-2.0") {
		return nil
	}
	if r.Effort == "" && !r.Thinking && !r.ThinkOff {
		return nil
	}
	if off, least, most, ok := vertexBudgets(m); ok {
		if r.ThinkOff {
			return map[string]any{"thinkingBudget": off}
		}
		tc := map[string]any{"includeThoughts": true}
		if r.Effort != "" {
			tc["thinkingBudget"] = min(max(budgetOf(r.Effort), least), most)
		}
		return tc
	}
	tc := map[string]any{"includeThoughts": !r.ThinkOff}
	if r.Effort != "" {
		tc["thinkingLevel"] = r.Effort
	}
	return tc
}

// vertexBudgets are the thinking budgets a Gemini 2.5 takes, as Google's
// model reference gives them: the one that turns its thinking off, or its
// least where it can't be, and its range. Not 2.5: false.
func vertexBudgets(model string) (off, least, most int, ok bool) {
	switch {
	case strings.HasPrefix(model, "gemini-2.5-pro"):
		return 128, 128, 32768, true
	case strings.HasPrefix(model, "gemini-2.5-flash-lite"):
		return 0, 512, 24576, true
	case strings.HasPrefix(model, "gemini-2.5-flash"):
		return 0, 1, 24576, true
	}
	return 0, 0, 0, false
}

// vertexEffort is req at the level it goes to Vertex AI at. Vertex AI turns
// away a level the model doesn't have (gemini-3.8-flash: 400 "Thinking
// level is unsupported: THINKING_LEVEL_MINIMAL"; every Gemini 3 for xhigh),
// so the effort is the model's nearest of its own, and reasoning turned off
// is its lowest. A model whose levels aren't known is asked for no less
// than low and no more than high, which every Gemini 3 takes there.
func vertexEffort(req *Request, levels []string) *Request {
	e := req.Effort
	if req.ThinkOff {
		e = "minimal"
	}
	if e == "" {
		return req
	}
	if len(levels) > 0 {
		e = fitEffort(e, levels)
	} else if at := slices.Index(effortRank, e); at < slices.Index(effortRank, "low") {
		e = "low"
	} else if at > slices.Index(effortRank, "high") {
		e = "high"
	}
	if e == req.Effort {
		return req
	}
	r := *req
	r.Effort = e
	return &r
}

// vertexSignatureRefused is Vertex AI turning a call's thought signature
// away as one it didn't give: "Invalid thought signature.".
var vertexSignatureRefused = regexp.MustCompile(`(?i)invalid thought signature`)

// withoutSignatures is req with no thought signature on its calls, each of
// which then goes to Vertex AI waved through (skipSignature); false when it
// had none.
func withoutSignatures(req *Request) (*Request, bool) {
	had := false
	msgs := slices.Clone(req.Messages)
	for i, m := range msgs {
		if !slices.ContainsFunc(m.Parts, func(p Part) bool { return p.Kind == ToolCall && p.Signature != "" }) {
			continue
		}
		parts := slices.Clone(m.Parts)
		for j := range parts {
			if parts[j].Kind == ToolCall {
				parts[j].Signature = ""
			}
		}
		msgs[i].Parts, had = parts, true
	}
	if !had {
		return req, false
	}
	r := *req
	r.Messages = msgs
	return &r, true
}
