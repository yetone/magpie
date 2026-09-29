package gateway

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// ---- Google Code Assist (provider side) --------------------------------------
//
// Gemini CLI and Antigravity sign in with Google and talk to Code Assist
// (cloudcode-pa.googleapis.com), which takes a Gemini request wrapped in an
// envelope — {model, project, request} — and streams replies wrapped the
// same way, {response}. The gateway builds {model, request}; the account
// adds the project and the ids its app sends when it signs the request.

// skipSignature stands in for a thought signature on a function call the
// model didn't make here: Google checks the ones Gemini 3 hands out, and
// this one tells it not to.
const skipSignature = "skip_thought_signature_validator"

var unsafeToolID = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// antigravityVariants are the ids of Antigravity's a model magpie offers
// stands for, by level, and antigravityBaseOf the model and level one of
// those ids is; vars so tests can stand in for them.
var (
	antigravityVariants = provider.AntigravityVariants
	antigravityBaseOf   = provider.AntigravityBase
)

// antigravityModelID is Antigravity's id for a model magpie offers, at
// the effort asked for: the family's variant at it, or at the nearest
// level it has; with none asked, the family's default. One of
// Antigravity's own ids at a level (gemini-3.7-flash-low, one an agent
// was set to before) goes as it is when no effort is asked, and else is
// the family's variant at the effort asked for: the effort the client
// picks wins, as it does for the family. Any other id goes as it is.
func antigravityModelID(model, effort string) string {
	if base, _, ok := antigravityBaseOf(model); ok {
		if effort == "" {
			return model
		}
		model = base
	}
	vs, ok := antigravityVariants(model)
	if !ok {
		return model
	}
	if effort == "" {
		return vs[""]
	}
	var levels []string
	for _, l := range effortRank {
		if vs[l] != "" {
			levels = append(levels, l)
		}
	}
	if id := vs[fitEffort(effort, levels)]; id != "" {
		return id
	}
	return vs[""]
}

// buildCodeAssist builds the Code Assist envelope for a request, for the
// app the account belongs to: "gemini" or "antigravity". On Antigravity a
// model that is a family of levels is asked for as the variant the effort
// picks (antigravityModelID).
func buildCodeAssist(r *Request, model, agent string) []byte {
	ag := agent == "antigravity"
	fixed := false // the id says the level it thinks at
	if ag {
		model = antigravityModelID(model, r.Effort)
		_, _, fixed = antigravityBaseOf(model)
	}
	claude := strings.Contains(strings.ToLower(model), "claude")
	toolID := func(id string) string {
		if !ag || id == "" {
			return id
		}
		return unsafeToolID.ReplaceAllString(id, "_")
	}
	names := map[string]string{}
	var contents []map[string]any
	for _, m := range r.Messages {
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		}
		var parts []map[string]any
		for _, p := range m.Parts {
			switch p.Kind {
			case Text:
				if p.Text != "" {
					parts = append(parts, map[string]any{"text": p.Text})
				}
			case Image, File:
				if p.Data != "" {
					parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": p.MediaType, "data": p.Data}})
				} else if p.URL != "" {
					parts = append(parts, map[string]any{"fileData": map[string]any{"mimeType": p.MediaType, "fileUri": p.URL}})
				}
			case ToolCall:
				names[p.ID] = p.Name
				call := map[string]any{"name": p.Name, "args": json.RawMessage(argsOf(p))}
				if id := toolID(p.ID); id != "" {
					call["id"] = id
				}
				parts = append(parts, map[string]any{"functionCall": call, "thoughtSignature": skipSignature})
			case ToolResult:
				name := names[p.CallID]
				if name == "" {
					name = "tool"
				}
				key := "output"
				switch {
				case ag:
					key = "result"
				case p.IsError:
					key = "error"
				}
				res := map[string]any{"name": name, "response": map[string]any{key: p.Text}}
				if id := toolID(p.CallID); id != "" {
					res["id"] = id
				}
				parts = append(parts, map[string]any{"functionResponse": res})
				// the images a tool returned follow its response, as Gemini
				// CLI sends a file it read
				for _, im := range p.Images {
					if im.Data != "" {
						parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": im.MediaType, "data": im.Data}})
					} else if im.URL != "" {
						parts = append(parts, map[string]any{"fileData": map[string]any{"mimeType": im.MediaType, "fileUri": im.URL}})
					}
				}
			}
			// thinking isn't sent back: its signatures belong to whoever
			// made them, and Google turns away ones it didn't
		}
		if len(parts) == 0 {
			continue
		}
		if n := len(contents); n > 0 && contents[n-1]["role"] == role {
			contents[n-1]["parts"] = append(contents[n-1]["parts"].([]map[string]any), parts...)
			continue
		}
		contents = append(contents, map[string]any{"role": role, "parts": parts})
	}
	req := map[string]any{"contents": contents}
	if r.System != "" {
		req["systemInstruction"] = map[string]any{"role": "user", "parts": []map[string]any{{"text": r.System}}}
	}

	if len(r.Tools) > 0 && r.ToolChoice != "none" {
		var decls []map[string]any
		for _, t := range r.Tools {
			d := map[string]any{"name": t.Name}
			if t.Description != "" {
				d["description"] = t.Description
			}
			schema := t.Schema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			if ag {
				d["parameters"] = plainSchema(schema)
			} else {
				d["parametersJsonSchema"] = schema
			}
			decls = append(decls, d)
		}
		req["tools"] = []map[string]any{{"functionDeclarations": decls}}
		mode := "AUTO"
		fc := map[string]any{}
		switch {
		case r.ToolChoice == "required":
			mode = "ANY"
		case strings.HasPrefix(r.ToolChoice, "name:"):
			mode = "ANY"
			fc["allowedFunctionNames"] = []string{strings.TrimPrefix(r.ToolChoice, "name:")}
		case ag && claude:
			// Antigravity's Claude wants its calls checked against the schema
			mode = "VALIDATED"
		}
		fc["mode"] = mode
		req["toolConfig"] = map[string]any{"functionCallingConfig": fc}
	}

	gen := map[string]any{}
	if r.MaxTokens > 0 && (!ag || claude) {
		gen["maxOutputTokens"] = r.MaxTokens
	}
	if r.Temp != nil {
		gen["temperature"] = *r.Temp
	}
	if r.TopP != nil {
		gen["topP"] = *r.TopP
	}
	if len(r.Stop) > 0 {
		gen["stopSequences"] = r.Stop
	}
	if tc := thinkingConfig(r, model, claude, fixed); tc != nil {
		gen["thinkingConfig"] = tc
		// Claude's answer has to have room past its thinking
		if b, ok := tc["thinkingBudget"].(int); ok && claude && gen["maxOutputTokens"] == nil {
			gen["maxOutputTokens"] = b + 32000
		}
	}
	if len(gen) > 0 {
		req["generationConfig"] = gen
	}
	b, _ := json.Marshal(map[string]any{"model": model, "request": req})
	return b
}

// thinkingConfig says how hard the model should think: a level for
// Gemini 3, a budget for the rest. A model that doesn't think gets none,
// and one whose id already says its level (fixed: Antigravity's
// gemini-3.7-flash-low) gets no level that could say otherwise — the
// effort asked for picked that id.
//
// Gemini 3's levels: Flash takes minimal, low, medium and high, so medium
// goes as medium; Pro takes low and high only, so medium goes up to high.
func thinkingConfig(r *Request, model string, claude, fixed bool) map[string]any {
	m := strings.ToLower(model)
	if strings.HasPrefix(m, "gpt-oss") || claude && !strings.Contains(m, "thinking") {
		return nil
	}
	if r.Effort == "" && !r.Thinking {
		// Claude asked to think still has to be told how much
		if !claude {
			return nil
		}
	}
	tc := map[string]any{"includeThoughts": true}
	if strings.HasPrefix(m, "gemini-3") || strings.HasPrefix(m, "gemini-pro-agent") {
		if r.Effort != "" && !fixed {
			level := "high"
			switch {
			case r.Effort == "low":
				level = "low"
			case r.Effort == "medium" && strings.Contains(m, "flash"):
				level = "medium"
			}
			tc["thinkingLevel"] = level
		}
		return tc
	}
	budget := budgetOf(effortOf(r.Effort))
	if claude && r.MaxTokens > 0 && budget >= r.MaxTokens {
		budget = r.MaxTokens - 1
		if budget < 1024 {
			return nil
		}
	}
	tc["thinkingBudget"] = budget
	return tc
}

// plainSchema is a JSON schema cut down to what Antigravity's function
// declarations take: an OpenAPI-style subset with no references, unions
// or keywords outside it.
func plainSchema(raw json.RawMessage) json.RawMessage {
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil {
		return raw
	}
	defs := map[string]any{}
	for _, k := range []string{"$defs", "definitions"} {
		if d, ok := root[k].(map[string]any); ok {
			for n, v := range d {
				defs[n] = v
			}
		}
	}
	var walk func(v any, depth int) any
	walk = func(v any, depth int) any {
		switch x := v.(type) {
		case []any:
			out := make([]any, len(x))
			for i, e := range x {
				out[i] = walk(e, depth)
			}
			return out
		case map[string]any:
			if depth > 32 {
				return map[string]any{"type": "object"}
			}
			if ref, ok := x["$ref"].(string); ok {
				name := ref[strings.LastIndex(ref, "/")+1:]
				if d, ok := defs[name]; ok {
					return walk(d, depth+1)
				}
				return map[string]any{"type": "object"}
			}
			// allOf: one schema with all the members' fields
			if all, ok := x["allOf"].([]any); ok {
				merged := map[string]any{}
				for k, v := range x {
					if k != "allOf" {
						merged[k] = v
					}
				}
				for _, e := range all {
					if m, ok := walk(e, depth+1).(map[string]any); ok {
						for k, v := range m {
							if k == "properties" {
								props, _ := merged["properties"].(map[string]any)
								if props == nil {
									props = map[string]any{}
								}
								for pk, pv := range v.(map[string]any) {
									props[pk] = pv
								}
								merged["properties"] = props
							} else if _, has := merged[k]; !has {
								merged[k] = v
							}
						}
					}
				}
				return walk(merged, depth+1)
			}
			// anyOf / oneOf: the first member that isn't null, nullable
			for _, k := range []string{"anyOf", "oneOf"} {
				if alts, ok := x[k].([]any); ok {
					var pick map[string]any
					nullable := false
					for _, a := range alts {
						m, _ := a.(map[string]any)
						if m["type"] == "null" {
							nullable = true
						} else if pick == nil && m != nil {
							pick = m
						}
					}
					out := map[string]any{}
					if pick != nil {
						if m, ok := walk(pick, depth+1).(map[string]any); ok {
							out = m
						}
					}
					if d, ok := x["description"].(string); ok && out["description"] == nil {
						out["description"] = d
					}
					if nullable {
						out["nullable"] = true
					}
					if out["type"] == nil {
						out["type"] = "string"
					}
					return out
				}
			}
			out := map[string]any{}
			for k, v := range x {
				switch k {
				case "description", "nullable", "required", "pattern", "minimum", "maximum", "minLength", "maxLength",
					"minItems", "maxItems", "minProperties", "maxProperties", "propertyOrdering":
					out[k] = v
				case "exclusiveMinimum", "exclusiveMaximum":
					// a bound it takes only as inclusive (draft 6 on: a number)
					if n, ok := v.(float64); ok {
						if b := "m" + strings.TrimPrefix(k, "exclusiveM"); x[b] == nil {
							out[b] = n
						}
					}
				case "type":
					if ts, ok := v.([]any); ok {
						for _, t := range ts {
							if t == "null" {
								out["nullable"] = true
							} else if out["type"] == nil {
								out["type"] = t
							}
						}
					} else {
						out["type"] = v
					}
				case "properties":
					props := map[string]any{}
					if m, ok := v.(map[string]any); ok {
						for pk, pv := range m {
							props[pk] = walk(pv, depth+1)
						}
					}
					out[k] = props
				case "items":
					out[k] = walk(v, depth+1)
				case "enum":
					// Google takes string enums only
					if es, ok := v.([]any); ok {
						strs := make([]any, 0, len(es))
						for _, e := range es {
							if s, ok := e.(string); ok {
								strs = append(strs, s)
							}
						}
						if len(strs) == len(es) {
							out[k] = strs
						}
					}
				default:
					// a keyword outside its subset (format, default, title,
					// additionalProperties, const…): Antigravity refuses the
					// request over any it doesn't know (#187)
				}
			}
			if c, ok := x["const"]; ok {
				if s, ok := c.(string); ok {
					out["enum"] = []any{s}
				}
			}
			if out["type"] == nil {
				switch {
				case out["properties"] != nil:
					out["type"] = "object"
				case out["items"] != nil:
					out["type"] = "array"
				}
			}
			if req, ok := out["required"].([]any); ok {
				// required names only the properties there are
				props, _ := out["properties"].(map[string]any)
				keep := []any{}
				for _, n := range req {
					if s, ok := n.(string); ok && props[s] != nil {
						keep = append(keep, s)
					}
				}
				if len(keep) == 0 {
					delete(out, "required")
				} else {
					out["required"] = keep
				}
			}
			return out
		}
		return v
	}
	b, err := json.Marshal(walk(root, 0))
	if err != nil {
		return raw
	}
	return b
}

// codeAssistDecoder reads Code Assist's stream: Gemini chunks, each wrapped
// in {response}.
type codeAssistDecoder struct {
	started bool
	tools   bool
	stopped bool
	usage   *Usage
}

func (d *codeAssistDecoder) decode(data string, emit func(Event)) error {
	var ch struct {
		Response *geminiChunk `json:"response"`
		Error    *struct {
			Message string `json:"message"`
		} `json:"error"`
		geminiChunk
	}
	if err := json.Unmarshal([]byte(data), &ch); err != nil {
		return nil
	}
	if ch.Error != nil {
		emit(Event{Kind: KError, Text: ch.Error.Message})
		return nil
	}
	c := ch.geminiChunk
	if ch.Response != nil {
		c = *ch.Response
	}
	if !d.started {
		d.started = true
		emit(Event{Kind: KStart, MsgID: c.ResponseID, Model: c.ModelVersion})
	}
	if c.UsageMetadata != nil {
		u := c.UsageMetadata
		d.usage = &Usage{Input: max(u.Prompt-u.Cached, 0), CacheRead: u.Cached,
			Output: u.Candidates + u.Thoughts, Reasoning: u.Thoughts}
	}
	for _, cand := range c.Candidates {
		for _, p := range cand.Content.Parts {
			switch {
			case p.FunctionCall != nil:
				id := p.FunctionCall.ID
				if id == "" {
					id = "call_" + newID()
				}
				args := p.FunctionCall.Args
				if len(args) == 0 || string(args) == "null" {
					args = json.RawMessage("{}")
				}
				d.tools = true
				emit(Event{Kind: KToolStart, ID: id, Name: p.FunctionCall.Name})
				emit(Event{Kind: KToolArgs, Text: string(args)})
			case p.Thought:
				if p.Text != "" {
					emit(Event{Kind: KThink, Text: p.Text})
				}
				if p.Signature != "" {
					emit(Event{Kind: KSig, Text: p.Signature})
				}
			case p.Text != "":
				emit(Event{Kind: KText, Text: p.Text})
			}
		}
		if cand.FinishReason != "" && !d.stopped {
			d.stopped = true
			stop := stopFromGemini(cand.FinishReason)
			if d.tools && stop == "stop" {
				stop = "tool"
			}
			emit(Event{Kind: KStop, Stop: stop})
		}
	}
	if d.stopped && d.usage != nil {
		emit(Event{Kind: KUsage, Usage: *d.usage})
		d.usage = nil
	}
	return nil
}

type geminiChunk struct {
	Candidates []struct {
		Content struct {
			Parts []gPart `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *struct {
		Prompt     int `json:"promptTokenCount"`
		Candidates int `json:"candidatesTokenCount"`
		Thoughts   int `json:"thoughtsTokenCount"`
		Cached     int `json:"cachedContentTokenCount"`
	} `json:"usageMetadata"`
	ModelVersion string `json:"modelVersion"`
	ResponseID   string `json:"responseId"`
}

func stopFromGemini(s string) string {
	switch s {
	case "MAX_TOKENS":
		return "length"
	case "STOP", "FINISH_REASON_UNSPECIFIED", "OTHER":
		return "stop"
	case "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL":
		return "stop"
	}
	// SAFETY, RECITATION, BLOCKLIST, PROHIBITED_CONTENT, SPII, ...
	return "filter"
}
