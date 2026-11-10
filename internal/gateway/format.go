package gateway

import (
	"bytes"
	"encoding/json"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
)

// Format is the shape a client asked the answer to come in (structured
// output), read from whichever API it spoke: Responses' text.format,
// Chat's response_format, Anthropic's output_config.format, Gemini's
// generationConfig.responseMimeType with its schema. Each API it is built
// for again asks for it in its own words; one that can't is told it in
// the system prompt (inSystem).
type Format struct {
	// Type is "json_schema" (Schema holds the schema the answer fits) or
	// "json_object" (any JSON object).
	Type string
	// Name and Description are json_schema's, as OpenAI's APIs take them;
	// Name is "response" where the client's API has none.
	Name, Description string
	Schema            json.RawMessage
	// Strict is the client's strict, nil when it said none.
	Strict *bool
	// told is a JSON object asked for in place of a schema the system
	// prompt already holds (asObject).
	told bool
}

// formatName is the name a schema goes under when the client's API names
// none (Anthropic's, Gemini's): OpenAI requires one.
const formatName = "response"

// openAIFormat reads the format OpenAI's APIs ask for, in Responses'
// text.format ({type, name, schema, strict, description}) or Chat's
// response_format ({type, json_schema: {name, schema, strict,
// description}}). nil for {type: "text"}, the default, and for anything
// else.
func openAIFormat(raw json.RawMessage) *Format {
	var f struct {
		Type        string          `json:"type"`
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Schema      json.RawMessage `json:"schema"`
		Strict      *bool           `json:"strict"`
		JSONSchema  *struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Schema      json.RawMessage `json:"schema"`
			Strict      *bool           `json:"strict"`
		} `json:"json_schema"`
	}
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	switch f.Type {
	case "json_object":
		return &Format{Type: "json_object"}
	case "json_schema":
		if js := f.JSONSchema; js != nil {
			f.Name, f.Description, f.Schema, f.Strict = js.Name, js.Description, js.Schema, js.Strict
		}
		if len(sentRaw(f.Schema)) == 0 {
			// a schema of no shape is any JSON object
			return &Format{Type: "json_object"}
		}
		if f.Name == "" {
			f.Name = formatName
		}
		return &Format{Type: "json_schema", Name: f.Name, Description: f.Description, Schema: f.Schema, Strict: f.Strict}
	}
	return nil
}

// schema is the JSON schema the answer has to fit, nil when there is none
// (no format, or any JSON object).
func (f *Format) schema() json.RawMessage {
	if f == nil || f.Type != "json_schema" {
		return nil
	}
	return f.Schema
}

// responses is the format as Responses' text.format says it.
func (f *Format) responses() map[string]any {
	if f.Type != "json_schema" {
		return map[string]any{"type": "json_object"}
	}
	out := map[string]any{"type": "json_schema", "name": f.Name, "schema": f.Schema}
	if f.Description != "" {
		out["description"] = f.Description
	}
	if f.Strict != nil {
		out["strict"] = *f.Strict
	}
	return out
}

// chat is the format as Chat's response_format says it.
func (f *Format) chat() map[string]any {
	if f.Type != "json_schema" {
		return map[string]any{"type": "json_object"}
	}
	js := map[string]any{"name": f.Name, "schema": f.Schema}
	if f.Description != "" {
		js["description"] = f.Description
	}
	if f.Strict != nil {
		js["strict"] = *f.Strict
	}
	return map[string]any{"type": "json_schema", "json_schema": js}
}

// instruction is the format said in words, for a model whose API can't be
// asked for it: Anthropic's Messages for any JSON object (its
// output_config.format takes a schema only), a bridge to a vendor's own
// API, or an upstream that turned the field away (formatRefused).
func (f *Format) instruction() string {
	if s := f.schema(); len(s) > 0 {
		return "Respond with a single JSON value and nothing else, matching this JSON schema:\n" + string(s) + "."
	}
	return "Respond with a single JSON object and nothing else."
}

// inSystem is r with its format told in the system prompt instead of
// asked for in a field, r itself when it has none.
func (r *Request) inSystem() *Request {
	if r.Format == nil {
		return r
	}
	q := *r
	q.Format, q.FormatTold = nil, true
	if !r.Format.told {
		q.System = withLine(q.System, r.Format.instruction())
	}
	return &q
}

// formatRefused is how unfit remembers a provider turning away the format
// field for a model (a relay or model without structured output: DeepSeek's
// "This response_format type is unavailable now"), so it is told the
// format in the system prompt from then on.
func formatRefused(model string) string { return "response_format:" + model }

// formatRefusal is an upstream's 400 naming the field structured output is
// asked for in, in any API's words.
var formatRefusal = lazyre.New(`(?i)response_format|json_schema|json_object|output_config\.format|text\.format|structured.output|response_?mime_?type|response_?json_?schema|response_?schema`)

// refusesFormat is whether an upstream's answer turned the request away
// over its format.
func refusesFormat(status int, body []byte) bool {
	return (status == 400 || status == 422) && formatRefusal.Match(body)
}

// schemaRefused is how unfit remembers a provider turning away a schema
// while it takes any JSON object (Xiaomi MiMo's Token Plan: "text.format
// type 'json_schema' is not supported, only 'text' and 'json_object' are
// allowed", #2126), so it is asked for a JSON object, the schema told in
// the system prompt, from then on.
func schemaRefused(model string) string { return "json_schema:" + model }

// asObject is r asking for any JSON object, its schema told in the system
// prompt; r itself when it asks for no schema.
func (r *Request) asObject() *Request {
	if r.Format == nil || r.Format.Type != "json_schema" {
		return r
	}
	q := *r
	q.Format = &Format{Type: "json_object", told: true}
	q.System = withLine(q.System, r.Format.instruction())
	return &q
}

// withLine is a system prompt with s said at its end.
func withLine(system, s string) string {
	if system != "" {
		system += "\n\n"
	}
	return system + s
}

// bodyFormat is the format a body passed through as the client sent it
// asks for, nil when it asks for none: Responses' text.format, Chat's
// response_format, Anthropic's output_config.format.
func bodyFormat(proto provider.Protocol, body []byte) *Format {
	switch proto {
	case provider.Responses:
		return openAIFormat(json.RawMessage(gjson.GetBytes(body, "text.format").Raw))
	case provider.Chat:
		return openAIFormat(json.RawMessage(gjson.GetBytes(body, "response_format").Raw))
	case provider.Anthropic:
		if f := gjson.GetBytes(body, "output_config.format"); f.Get("type").String() == "json_schema" && f.Get("schema").IsObject() {
			return &Format{Type: "json_schema", Name: formatName, Schema: json.RawMessage(f.Get("schema").Raw)}
		}
	}
	return nil
}

// formatInBody is a passed-through body asking for its format less, the
// rest told in the system prompt: with object, a schema as any JSON object
// (asObject); without, no format field at all (inSystem). Anthropic's
// output_config.format takes a schema only, so it is never asked for an
// object. The body and false when there is nothing to change.
func formatInBody(proto provider.Protocol, body []byte, object bool) ([]byte, bool) {
	f := bodyFormat(proto, body)
	if f == nil || object && (proto == provider.Anthropic || f.Type != "json_schema") {
		return body, false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil {
		return body, false
	}
	line := f.instruction()
	switch proto {
	case provider.Responses:
		text, _ := m["text"].(map[string]any)
		if text == nil {
			return body, false
		}
		if object {
			text["format"] = map[string]any{"type": "json_object"}
		} else if delete(text, "format"); len(text) == 0 {
			delete(m, "text")
		}
		s, _ := m["instructions"].(string)
		m["instructions"] = withLine(s, line)
	case provider.Chat:
		if object {
			m["response_format"] = map[string]any{"type": "json_object"}
		} else {
			delete(m, "response_format")
		}
		m["messages"] = withSystemLine(m["messages"], line)
	case provider.Anthropic:
		oc, _ := m["output_config"].(map[string]any)
		if delete(oc, "format"); len(oc) == 0 {
			delete(m, "output_config")
		}
		switch s := m["system"].(type) {
		case []any:
			m["system"] = append(s, map[string]any{"type": "text", "text": line})
		case string:
			m["system"] = withLine(s, line)
		default:
			m["system"] = line
		}
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false) // <, > and & as the agent wrote them
	if enc.Encode(m) != nil {
		return body, false
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), true
}

// withSystemLine is a chat's messages with s said at the end of its first
// system (or developer) message, or in one of its own ahead of the rest
// when it has none.
func withSystemLine(v any, s string) []any {
	msgs, _ := v.([]any)
	for _, raw := range msgs {
		msg, ok := raw.(map[string]any)
		if !ok || msg["role"] != "system" && msg["role"] != "developer" {
			continue
		}
		switch c := msg["content"].(type) {
		case string:
			msg["content"] = withLine(c, s)
			return msgs
		case []any:
			msg["content"] = append(c, map[string]any{"type": "text", "text": s})
			return msgs
		}
	}
	return append([]any{map[string]any{"role": "system", "content": s}}, msgs...)
}

// wantsJSON is whether the client asked for structured output, in a field
// or told in the system prompt.
func (r *Request) wantsJSON() bool { return r.Format != nil || r.FormatTold }
