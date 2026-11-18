package provider

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
)

// DecideAsk is a System One request (body) as via takes it: Vercel's
// TypeSafe API takes it as it is; its evaluation models name the model in
// a header and ask a noul as a boolean; Workers AI takes the state and
// questions as the input of a run of the model, and a Clef there takes
// the request as it is, its model named as Clef names itself; Google's
// Decisions API takes it as googleAsk makes it.
func DecideAsk(via, model string, body []byte) []byte {
	var q map[string]any
	if via == ViaSystemOne || via == ViaVercel || json.Unmarshal(body, &q) != nil {
		return body
	}
	if via == ViaGoogle {
		return googleAsk(q, body)
	}
	delete(q, "model")
	switch via {
	case ViaVercelEval:
		qs, _ := q["questions"].(map[string]any)
		for _, v := range qs {
			if x, ok := v.(map[string]any); ok && x["type"] == "noul" {
				x["type"] = "boolean"
			}
		}
		b, _ := json.Marshal(q)
		return b
	case ViaCloudflare:
		if CloudflareClef(model) {
			q["model"] = strings.TrimPrefix(model, "@cf/cloudflare/")
			b, _ := json.Marshal(q)
			return b
		}
		b, _ := json.Marshal(map[string]any{"model": model, "input": q})
		return b
	}
	return body
}

// DecideAnswer is a gateway's answer as System One gives it: out of
// Cloudflare's envelope, or, from Vercel's evaluation models (its TypeSafe
// API answers as System One does), a boolean's probability as a
// noul, a confidence from the probabilities where there is none, and its
// usage named as System One names it.
func DecideAnswer(via string, b []byte) []byte {
	switch via {
	case ViaCloudflare:
		var env struct {
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(b, &env) == nil && len(env.Result) > 0 && env.Result[0] == '{' {
			return env.Result
		}
	case ViaGoogle:
		// Google's usage is total_input_tokens and total_output_tokens
		// (always 0: "Only input tokens are billed"); its answers are
		// System One's, noul, choice and score alike
		var g struct {
			Model   string          `json:"model"`
			Answers json.RawMessage `json:"answers"`
			Usage   struct {
				Input  int `json:"total_input_tokens"`
				Output int `json:"total_output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(b, &g) != nil || len(g.Answers) == 0 || g.Answers[0] != '{' {
			return b
		}
		out := map[string]any{"answers": g.Answers,
			"usage": map[string]int{"input_tokens": g.Usage.Input, "output_tokens": g.Usage.Output}}
		if g.Model != "" {
			out["model"] = g.Model
		}
		b, _ := json.Marshal(out)
		return b
	case ViaVercelEval:
		var v struct {
			Model   string                    `json:"model"`
			Answers map[string]map[string]any `json:"answers"`
			Usage   struct {
				Input  int `json:"inputTokens"`
				Output int `json:"outputTokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(b, &v) != nil || v.Answers == nil {
			return b
		}
		for _, a := range v.Answers {
			if a["type"] == "boolean" {
				a["type"], a["noul"] = "noul", a["probability"]
				continue
			}
			if _, ok := a["confidence"]; !ok {
				if ps, ok := a["probabilities"].(map[string]any); ok {
					a["confidence"] = Confidence(ps)
				}
			}
		}
		out, _ := json.Marshal(map[string]any{"model": v.Model, "answers": v.Answers,
			"usage": map[string]int{"input_tokens": v.Usage.Input, "output_tokens": v.Usage.Output}})
		return out
	}
	return b
}

// googleAsk is a System One request (q, as body) as Google's Decisions
// API takes it (aistudio.google.com/docs/decisions-api): its state is the
// input, text as it is and an object or list as its JSON text ("Serialize
// JSON objects and arrays to a string and pass them as text"), where an
// object would be read as a content block and refused (400 "type
// parameter is required at input[0]"); its images, data: URLs, are image
// blocks after it, and any other URL a block at that uri. Its questions
// are System One's, noul, choice and score, but a choice's option with no
// description (null, as magpie's "none of these" goes) has an empty one,
// which Google takes. Everything else goes as it is, for Google to refuse
// what it has no place for with a 400 naming it.
func googleAsk(q map[string]any, body []byte) []byte {
	state, ok := q["state"]
	if !ok {
		return body
	}
	delete(q, "state")
	text, isText := state.(string)
	if !isText {
		b, _ := json.Marshal(state)
		text = string(b)
	}
	var input any = text
	if ims, ok := q["images"].([]any); ok {
		delete(q, "images")
		blocks := []any{map[string]any{"type": "text", "text": text}}
		for _, im := range ims {
			u, _ := im.(string)
			if u == "" {
				continue
			}
			block := map[string]any{"type": "image", "uri": u}
			if meta, data, ok := strings.Cut(strings.TrimPrefix(u, "data:"), ","); ok && strings.HasPrefix(u, "data:") && strings.HasSuffix(meta, ";base64") {
				block = map[string]any{"type": "image", "data": data, "mime_type": strings.TrimSuffix(meta, ";base64")}
			}
			blocks = append(blocks, block)
		}
		if len(blocks) > 1 {
			input = blocks
		}
	}
	q["input"] = input
	if qs, ok := q["questions"].(map[string]any); ok {
		for _, v := range qs {
			x, _ := v.(map[string]any)
			if c, ok := x["criteria"].(map[string]any); ok && x["type"] == "choice" {
				for k, d := range c {
					if d == nil {
						c[k] = ""
					}
				}
			}
		}
	}
	b, _ := json.Marshal(q)
	return b
}

// DecideResult is a decision API's answer, status and body, as System
// One gives it: DecideAnswer's translation of one that answered, and,
// from Google, an input its filters blocked (prompt_feedback.block_reason,
// "no decisions were evaluated, and answers is empty") as the 400 Google
// answers a blocked input with otherwise, its reason in the message.
func DecideResult(via string, status int, b []byte) (int, []byte) {
	if status >= 300 {
		return status, b
	}
	if via == ViaGoogle {
		var g struct {
			Answers  map[string]json.RawMessage `json:"answers"`
			Feedback struct {
				Reason  string `json:"block_reason"`
				Message string `json:"block_reason_message"`
			} `json:"prompt_feedback"`
		}
		if json.Unmarshal(b, &g) == nil && g.Feedback.Reason != "" && len(g.Answers) == 0 {
			msg := "Google blocked the input (" + g.Feedback.Reason + ")"
			if g.Feedback.Message != "" {
				msg += ": " + g.Feedback.Message
			}
			out, _ := json.Marshal(map[string]any{"error": map[string]any{"code": g.Feedback.Reason, "message": msg}})
			return http.StatusBadRequest, out
		}
	}
	return status, DecideAnswer(via, b)
}

// Confidence is how sure a set of probabilities is of its likeliest, as
// System One gives it: 0 with every option as likely, 1 with one certain
// (its 0.87 of three options is 0.8).
func Confidence(ps map[string]any) float64 {
	top, n := 0.0, 0
	for _, v := range ps {
		if f, ok := v.(float64); ok {
			top, n = math.Max(top, f), n+1
		}
	}
	if n < 2 {
		return 1
	}
	return max(0, (top-1/float64(n))/(1-1/float64(n)))
}
