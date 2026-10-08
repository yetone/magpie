package gateway

import (
	"bytes"
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
		// Parts is what the function gave back beside its response: the
		// image agy's view_file read, as inlineData (#1038)
		Parts []gPart `json:"parts,omitempty"`
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

// buildGemini is a generateContent body for an upstream that speaks Gemini
// itself (Factory's /api/llm/g). The contents are the ones Code Assist
// sends inside its envelope. systemInstruction has no role: droid sends
// {parts:[{text}]}. droid sends no stream field; Factory answers SSE either
// way, and a client that asked for JSON is given it after the fact.
func buildGemini(r *Request, model string) ([]byte, error) {
	var wrap struct {
		Request map[string]any `json:"request"`
	}
	if err := json.Unmarshal(buildCodeAssistSent(r, model, "gemini"), &wrap); err != nil {
		return nil, err
	}
	if wrap.Request == nil {
		return nil, fmt.Errorf("empty Gemini request")
	}
	if si, ok := wrap.Request["systemInstruction"].(map[string]any); ok {
		delete(si, "role")
	}
	wrap.Request["model"] = model
	delete(wrap.Request, "stream")
	return json.Marshal(wrap.Request)
}

// geminiCamel spells a Gemini request's fields in camelCase. Google's API
// reads proto JSON, which takes a field by either name — inline_data,
// mime_type, function_call as well as inlineData — and clients (agy, the
// Python SDK's raw calls) send the snake_case ones; read only as camelCase
// an image or a PDF was left out, and the model made up what it held
// (#934). The values that are the caller's own — a call's args, a
// result's response, the schemas — keep their keys as they are.
func geminiCamel(body []byte) []byte {
	if !bytes.Contains(body, []byte("_")) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return body
	}
	changed := false
	v = camelKeys(v, &changed)
	if !changed {
		return body
	}
	out, err := json.Marshal(v)
	if err != nil {
		return body
	}
	return out
}

// geminiOwn are the fields whose values are the caller's, not Google's:
// their keys are kept as they are.
var geminiOwn = map[string]bool{"args": true, "response": true, "parameters": true, "parametersJsonSchema": true,
	"responseSchema": true, "responseJsonSchema": true, "labels": true}

func camelKeys(v any, changed *bool) any {
	switch t := v.(type) {
	case []any:
		for i := range t {
			t[i] = camelKeys(t[i], changed)
		}
	case map[string]any:
		for k, val := range t {
			nk := k
			if strings.Contains(k, "_") {
				nk = snakeToCamel(k)
			}
			if !geminiOwn[nk] {
				val = camelKeys(val, changed)
			}
			if nk == k {
				t[k] = val
				continue
			}
			*changed = true
			delete(t, k)
			if _, both := t[nk]; !both { // sent both ways, the camelCase one is kept
				t[nk] = val
			}
		}
	}
	return v
}

func snakeToCamel(s string) string {
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		if p := parts[i]; p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

func parseGemini(body []byte) (*Request, error) {
	body = geminiCamel(body)
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
		// a function's result is the user's side of the turn whatever role
		// it came under: Antigravity CLI sends its tools' results as
		// "model" (#650), and kept there the request ends with the model's
		// turn, which Antigravity turns away
		turn := func(role string) {
			if msg.Role != role {
				if len(msg.Parts) > 0 {
					r.Messages = append(r.Messages, msg)
				}
				msg = Message{Role: role}
			}
		}
		for _, p := range c.Parts {
			if p.FunctionResponse != nil {
				turn("user")
			} else {
				turn(role)
			}
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
				images, files := functionResponseParts(fr.Parts, &text)
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Name: fr.Name, Text: text, Images: images, IsError: isErr})
				// a file no tool result can hold follows it in the same turn
				msg.Parts = append(msg.Parts, files...)
			case p.InlineData != nil:
				if strings.HasPrefix(p.InlineData.MimeType, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.InlineData.MimeType, Data: p.InlineData.Data})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: File, MediaType: p.InlineData.MimeType, Data: p.InlineData.Data})
				}
			case p.FileData != nil:
				if strings.HasPrefix(p.FileData.MimeType, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.FileData.MimeType, URL: p.FileData.FileURI})
				} else {
					// Other protocols cannot carry Gemini file references as image URLs.
					msg.Parts = append(msg.Parts, Part{Kind: File, MediaType: p.FileData.MimeType, URL: p.FileData.FileURI})
				}
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
	// ANY and VALIDATED can limit declarations to named functions. Keep
	// VALIDATED's auto choice, while ANY still requires a call.
	var allowed map[string]bool
	if g.ToolConfig != nil && g.ToolConfig.FunctionCallingConfig != nil {
		fc := g.ToolConfig.FunctionCallingConfig
		if (strings.EqualFold(fc.Mode, "ANY") || strings.EqualFold(fc.Mode, "VALIDATED")) && len(fc.AllowedFunctionNames) > 0 {
			allowed = make(map[string]bool, len(fc.AllowedFunctionNames))
			for _, name := range fc.AllowedFunctionNames {
				allowed[name] = true
			}
		}
	}
	for _, t := range g.Tools {
		if t.GoogleSearch != nil || t.GoogleSnake != nil {
			r.WebSearch = true
		}
		for _, f := range t.FunctionDeclarations {
			if allowed != nil && !allowed[f.Name] {
				continue
			}
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
			// A name is only forcible when its declaration survived filtering.
			// Otherwise retain required so translated routes reject an empty
			// callable set instead of silently returning plain text.
			if len(fc.AllowedFunctionNames) == 1 && len(r.Tools) == 1 && r.Tools[0].Name == fc.AllowedFunctionNames[0] {
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

// functionResponseParts reads a functionResponse's own parts (Gemini 3's
// multimodal function responses): its images go with the result, as the
// other APIs' tool results carry them, and any other file (a PDF, audio)
// is given back as a part of the turn, since a tool result holds only
// text and images. Left unread, the model never saw what the tool read and
// made it up (#1038).
func functionResponseParts(parts []gPart, text *string) (images, files []Part) {
	for _, p := range parts {
		var part Part
		switch {
		case p.InlineData != nil:
			part = Part{MediaType: p.InlineData.MimeType, Data: p.InlineData.Data}
		case p.FileData != nil:
			part = Part{MediaType: p.FileData.MimeType, URL: p.FileData.FileURI}
		case p.Text != "" && !p.Thought:
			if strings.TrimSpace(*text) != "" {
				*text += "\n\n"
			}
			*text += p.Text
			continue
		default:
			continue
		}
		if strings.HasPrefix(part.MediaType, "image/") {
			part.Kind = Image
			images = append(images, part)
		} else {
			part.Kind = File
			files = append(files, part)
		}
	}
	return images, files
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
		case Image:
			if p.Data != "" {
				out = append(out, map[string]any{"inlineData": map[string]any{"mimeType": p.MediaType, "data": p.Data}})
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

// keepalive sends nothing: Google's SDK takes a stream's "data: " lines
// one after another from the front and gets stuck on anything else, a
// comment too, and there is no event without content to send instead.
func (e *geminiEncoder) keepalive() {}

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
	case KImage:
		e.flushTool()
		e.chunk(geminiParts([]Part{{Kind: Image, MediaType: ev.Name, Data: ev.Text}}), "", nil)
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
