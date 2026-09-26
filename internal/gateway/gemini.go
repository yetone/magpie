package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ---- Google Gemini API (client side) ----------------------------------------
//
// Gemini CLI speaks only Google's own API, so the gateway serves it:
//   POST /v1beta/models/{model}:generateContent
//   POST /v1beta/models/{model}:streamGenerateContent?alt=sse
//   POST /v1beta/models/{model}:countTokens
//   GET  /v1beta/models
// The model is in the path, so the route handler folds it (and whether the
// call streams) into the body before the shared request path sees it.

type gPart struct {
	Text       string `json:"text,omitempty"`
	Thought    bool   `json:"thought,omitempty"`
	Signature  string `json:"thoughtSignature,omitempty"`
	InlineData *struct {
		MimeType string `json:"mimeType"`
		Data     string `json:"data"`
	} `json:"inlineData,omitempty"`
	FileData *struct {
		MimeType string `json:"mimeType"`
		FileURI  string `json:"fileUri"`
	} `json:"fileData,omitempty"`
	FunctionCall *struct {
		ID   string          `json:"id,omitempty"`
		Name string          `json:"name"`
		Args json.RawMessage `json:"args,omitempty"`
	} `json:"functionCall,omitempty"`
	FunctionResponse *struct {
		ID       string          `json:"id,omitempty"`
		Name     string          `json:"name"`
		Response json.RawMessage `json:"response,omitempty"`
	} `json:"functionResponse,omitempty"`
}

type gContent struct {
	Role  string  `json:"role,omitempty"`
	Parts []gPart `json:"parts"`
}

type gRequest struct {
	Model             string          `json:"model"`
	Stream            bool            `json:"stream"`
	Contents          []gContent      `json:"contents"`
	SystemInstruction json.RawMessage `json:"systemInstruction,omitempty"`
	Tools             []struct {
		FunctionDeclarations []struct {
			Name                 string          `json:"name"`
			Description          string          `json:"description,omitempty"`
			Parameters           json.RawMessage `json:"parameters,omitempty"`
			ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema,omitempty"`
		} `json:"functionDeclarations,omitempty"`
		GoogleSearch json.RawMessage `json:"googleSearch,omitempty"`
		GoogleSnake  json.RawMessage `json:"google_search,omitempty"`
	} `json:"tools,omitempty"`
	ToolConfig *struct {
		FunctionCallingConfig *struct {
			Mode                 string   `json:"mode"`
			AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
		} `json:"functionCallingConfig,omitempty"`
	} `json:"toolConfig,omitempty"`
	GenerationConfig *struct {
		MaxOutputTokens    int             `json:"maxOutputTokens,omitempty"`
		Temperature        *float64        `json:"temperature,omitempty"`
		TopP               *float64        `json:"topP,omitempty"`
		StopSequences      []string        `json:"stopSequences,omitempty"`
		ResponseMimeType   string          `json:"responseMimeType,omitempty"`
		ResponseSchema     json.RawMessage `json:"responseSchema,omitempty"`
		ResponseJSONSchema json.RawMessage `json:"responseJsonSchema,omitempty"`
		ThinkingConfig     *struct {
			ThinkingBudget  *int   `json:"thinkingBudget,omitempty"`
			ThinkingLevel   string `json:"thinkingLevel,omitempty"`
			IncludeThoughts bool   `json:"includeThoughts,omitempty"`
		} `json:"thinkingConfig,omitempty"`
	} `json:"generationConfig,omitempty"`
}

func parseGemini(body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Model: g.Model, Stream: g.Stream}
	r.System = geminiText(g.SystemInstruction)
	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens, r.Temp, r.TopP, r.Stop = gc.MaxOutputTokens, gc.Temperature, gc.TopP, gc.StopSequences
		if tc := gc.ThinkingConfig; tc != nil {
			switch {
			case tc.ThinkingLevel != "":
				r.Effort = effortOf(tc.ThinkingLevel)
			case tc.ThinkingBudget != nil && *tc.ThinkingBudget < 0:
				r.Effort = "medium" // -1: let the model decide
			case tc.ThinkingBudget != nil:
				r.Effort = effortOfBudget(*tc.ThinkingBudget)
			}
			r.Thinking = r.Effort != ""
		}
		// structured output has no seat in the other APIs; ask for it
		if strings.HasPrefix(gc.ResponseMimeType, "application/json") {
			ask := "Respond with a single JSON value and nothing else"
			if s := firstJSON(gc.ResponseJSONSchema, gc.ResponseSchema); s != "" {
				ask += ", matching this JSON schema:\n" + s
			}
			if r.System != "" {
				r.System += "\n\n"
			}
			r.System += ask + "."
		}
	}
	// function results carry the call's id when the client kept it; older
	// clients send only the name, matched to the latest call of that name
	names := map[string]string{}
	for _, c := range g.Contents {
		role := "user"
		if c.Role == "model" || c.Role == "assistant" {
			role = "assistant"
		}
		msg := Message{Role: role}
		for _, p := range c.Parts {
			switch {
			case p.FunctionCall != nil:
				fc := p.FunctionCall
				id := fc.ID
				if id == "" {
					id = "call_" + newID()
				}
				names[fc.Name] = id
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: fc.Name, Args: parseArgs(string(fc.Args))})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id == "" {
					id = names[fr.Name]
				}
				text, isErr := functionResponseText(fr.Response)
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Name: fr.Name, Text: text, IsError: isErr})
			case p.InlineData != nil:
				if strings.HasPrefix(p.InlineData.MimeType, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.InlineData.MimeType, Data: p.InlineData.Data})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: "[attachment " + p.InlineData.MimeType + "]"})
				}
			case p.FileData != nil:
				msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.FileData.MimeType, URL: p.FileData.FileURI})
			case p.Thought:
				msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: p.Text, Signature: p.Signature})
			case p.Text != "":
				msg.Parts = append(msg.Parts, Part{Kind: Text, Text: p.Text})
			}
		}
		if len(msg.Parts) > 0 {
			r.Messages = append(r.Messages, msg)
		}
	}
	for _, t := range g.Tools {
		if t.GoogleSearch != nil || t.GoogleSnake != nil {
			r.WebSearch = true
		}
		for _, f := range t.FunctionDeclarations {
			schema := f.ParametersJSONSchema
			if len(schema) == 0 {
				schema = jsonSchema(f.Parameters)
			}
			r.Tools = append(r.Tools, Tool{Name: f.Name, Description: f.Description, Schema: schema})
		}
	}
	if g.ToolConfig != nil && g.ToolConfig.FunctionCallingConfig != nil {
		fc := g.ToolConfig.FunctionCallingConfig
		switch strings.ToUpper(fc.Mode) {
		case "NONE":
			r.ToolChoice = "none"
		case "ANY":
			if len(fc.AllowedFunctionNames) == 1 {
				r.ToolChoice = "name:" + fc.AllowedFunctionNames[0]
			} else {
				r.ToolChoice = "required"
			}
		default:
			r.ToolChoice = "auto"
		}
	}
	return r, nil
}

// geminiText joins the text parts of a Content, which may also arrive as a
// bare string.
func geminiText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var c gContent
	if json.Unmarshal(raw, &c) != nil {
		var parts []gPart
		if json.Unmarshal(raw, &parts) != nil {
			return ""
		}
		c.Parts = parts
	}
	var b strings.Builder
	for _, p := range c.Parts {
		if !p.Thought && p.Text != "" {
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// functionResponseText is what a tool said, as the other APIs carry it:
// Gemini CLI wraps output in {output} or {error}; anything else is the
// response object as JSON.
func functionResponseText(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) == nil {
		if e, ok := m["error"]; ok && len(m) == 1 {
			var s string
			if json.Unmarshal(e, &s) == nil {
				return s, true
			}
			return string(e), true
		}
		if o, ok := m["output"]; ok && len(m) == 1 {
			var s string
			if json.Unmarshal(o, &s) == nil {
				return s, false
			}
			return string(o), false
		}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, false
	}
	return string(raw), false
}

func firstJSON(xs ...json.RawMessage) string {
	for _, x := range xs {
		if len(x) > 0 {
			return string(jsonSchema(x))
		}
	}
	return ""
}

// jsonSchema turns Gemini's OpenAPI-flavoured schema into JSON Schema:
// type names come lowercased and its own extras drop out.
func jsonSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			out := make(map[string]any, len(x))
			for k, val := range x {
				switch k {
				case "type":
					switch t := val.(type) {
					case string:
						out[k] = strings.ToLower(t)
					case []any:
						ts := make([]any, len(t))
						for i, e := range t {
							if s, ok := e.(string); ok {
								ts[i] = strings.ToLower(s)
							} else {
								ts[i] = e
							}
						}
						out[k] = ts
					default:
						out[k] = val
					}
				case "nullable":
					if b, ok := val.(bool); ok && b {
						out["x-nullable"] = true
					}
				case "propertyOrdering", "example":
				default:
					out[k] = walk(val)
				}
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, e := range x {
				out[i] = walk(e)
			}
			return out
		}
		return v
	}
	b, err := json.Marshal(walk(v))
	if err != nil {
		return raw
	}
	return b
}

func stopToGemini(s string) string {
	switch s {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// gemini is a Usage as usageMetadata: the prompt count includes cached
// tokens (see prompt), and thoughts are counted apart from the answer.
func (u Usage) gemini() map[string]any {
	out := map[string]any{"promptTokenCount": u.prompt(), "candidatesTokenCount": max(u.Output-u.Reasoning, 0),
		"totalTokenCount": u.prompt() + u.Output}
	if u.CacheRead > 0 {
		out["cachedContentTokenCount"] = u.CacheRead
	}
	if u.Reasoning > 0 {
		out["thoughtsTokenCount"] = u.Reasoning
	}
	return out
}

// geminiParts renders reply parts. Function calls go out whole, so a
// streaming encoder holds one back until its arguments have all arrived.
func geminiParts(parts []Part) []map[string]any {
	out := []map[string]any{}
	for _, p := range parts {
		switch p.Kind {
		case Text:
			if p.Text != "" {
				out = append(out, map[string]any{"text": p.Text})
			}
		case Thinking:
			if p.Text != "" {
				out = append(out, map[string]any{"text": p.Text, "thought": true})
			}
		case ToolCall:
			id := p.ID
			if id == "" {
				id = "call_" + newID()
			}
			out = append(out, map[string]any{"functionCall": map[string]any{"id": id, "name": p.Name, "args": argsOf(p)}})
		}
	}
	return out
}

// geminiEncoder writes events as a Gemini streamGenerateContent stream.
type geminiEncoder struct {
	w       *sseWriter
	model   string
	id      string
	started bool
	tool    *Part // function call whose arguments are still arriving
	args    strings.Builder
	col     collector
}

func (e *geminiEncoder) chunk(parts []map[string]any, finish string, usage map[string]any) {
	if len(parts) == 0 && finish == "" && usage == nil {
		return
	}
	if parts == nil {
		parts = []map[string]any{}
	}
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "index": 0}
	if finish != "" {
		cand["finishReason"] = finish
	}
	msg := map[string]any{"candidates": []map[string]any{cand}, "modelVersion": e.model, "responseId": e.id}
	if usage != nil {
		msg["usageMetadata"] = usage
	}
	e.w.event("", msg)
}

func (e *geminiEncoder) start(ev Event) {
	if e.started {
		return
	}
	e.started = true
	e.id = ev.MsgID
	if e.id == "" {
		e.id = newID()
	}
	if ev.Model != "" {
		e.model = ev.Model
	}
	e.w.begin()
}

// flushTool sends the function call that was being assembled.
func (e *geminiEncoder) flushTool() {
	if e.tool == nil {
		return
	}
	e.tool.Args = parseArgs(e.args.String())
	e.chunk(geminiParts([]Part{*e.tool}), "", nil)
	e.tool = nil
	e.args.Reset()
}

func (e *geminiEncoder) event(ev Event) {
	if ev.Kind != KStart && !e.started {
		e.start(Event{})
	}
	switch ev.Kind {
	case KStart:
		e.start(ev)
	case KText:
		e.flushTool()
		e.chunk(geminiParts([]Part{{Kind: Text, Text: ev.Text}}), "", nil)
	case KThink:
		e.flushTool()
		e.chunk(geminiParts([]Part{{Kind: Thinking, Text: ev.Text}}), "", nil)
	case KToolStart:
		e.flushTool()
		e.tool = &Part{Kind: ToolCall, ID: ev.ID, Name: ev.Name}
	case KToolArgs:
		if e.tool != nil {
			e.args.WriteString(ev.Text)
		}
	case KError:
		e.w.event("", map[string]any{"error": map[string]any{"code": 502, "message": ev.Text, "status": "UNAVAILABLE"}})
	}
	e.col.add(ev)
}

func (e *geminiEncoder) finish() {
	if !e.started {
		e.start(Event{})
	}
	e.flushTool()
	res := e.col.finish()
	e.chunk(nil, stopToGemini(res.Stop), res.Usage.gemini())
}

// renderGemini is the non-streaming reply.
func renderGemini(res Result, model string) []byte {
	if res.Model != "" {
		model = res.Model
	}
	id := res.ID
	if id == "" {
		id = newID()
	}
	b, _ := json.Marshal(map[string]any{
		"candidates": []map[string]any{{"content": map[string]any{"role": "model", "parts": geminiParts(res.Parts)},
			"finishReason": stopToGemini(res.Stop), "index": 0}},
		"usageMetadata": res.Usage.gemini(), "modelVersion": model, "responseId": id,
	})
	return b
}

// geminiModels is GET /v1beta/models: the catalog in Google's shape.
func geminiModel(id, name string) map[string]any {
	return map[string]any{"name": "models/" + id, "displayName": name, "description": name + " via magpie",
		"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent", "countTokens"}}
}
