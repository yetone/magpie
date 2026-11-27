package gateway

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// xAI's own API (api.x.ai), wherever a Grok model is served, takes four
// things a client's wire puts in wrong, as the Grok subscription's plugin
// shapes its bodies too (provider's grokShaped):
//
//   - Codex hands a sealed reasoning item back with "content": null
//     beside its encrypted_content, which the backend answers "Could not
//     decode the compaction blob. Ensure it is unmodified from the compact
//     response." (#3392, kongkongklng: the first turn went through, every
//     one after it 400ed) — the null goes.
//   - Claude Code sends a system turn mid-conversation (Anthropic made
//     them GA), which xAI's Messages answers "Invalid request content:
//     Invalid message role." (#3392: every claude call 400ed) — each folds
//     into the top-level system, the only place it takes the role.
//   - A tool's object schema can say "required": null where none are
//     (an MCP server's — Anthropic's API takes it) or leave the key out
//     (Claude Code's CronList, TaskStop, ListAgents, Workflow…), and xAI
//     answers both "Invalid request content: Schema validation failed:
//     [standard_violation] /required: null is not of type \"array\""
//     (#3392: TwinsDestiny and guqing, straight against api.x.ai:
//     {"type":"object","properties":{}} is refused, the same with
//     "required":[] taken) — every object node gets an empty array.
//   - Its Messages and chat take no stop sequences, Claude Code's
//     auto-mode "</block>" among them (withoutStop) — they go.
//
// The relay and the translation shape alike: a refusal of custom tools
// puts a Codex request on the translated path from the next turn on
// (custom_tool.go), and there is nothing to learn from either answer —
// xAI takes no null content, no system turn and no object schema without
// its required at all.

// xaiBody shapes body as xAI's own API takes it on proto.
func xaiBody(p provider.Provider, proto provider.Protocol, body []byte) []byte {
	if p.VendorHost() != "api.x.ai" {
		return body
	}
	body = toolsRequiredFilled(body)
	switch proto {
	case provider.Responses:
		return withoutNullReasoningContent(body)
	case provider.Anthropic:
		return systemTurnsFolded(withoutStop(proto, body))
	case provider.Chat:
		return withoutStop(proto, body)
	}
	return body
}

// withoutStop is a Messages or chat body without its stop sequences:
// xAI's /v1/messages and /v1/chat/completions answer any that are there
// "Model grok-4.7 does not support parameter stop." (#3392, guqing:
// Claude Code's auto-mode classifier asks with ["</block>"]; straight
// against api.x.ai both endpoints refused a non-empty stop, took an empty
// one, and /v1/responses took it). The reply
// runs on past where it would have stopped, which the classifier's
// parser reads as before (it takes "<block>no" with or without the
// closing tag) and max_tokens still bounds. An empty list or string goes
// as it came.
func withoutStop(proto provider.Protocol, body []byte) []byte {
	key := "stop"
	if proto == provider.Anthropic {
		key = "stop_sequences"
	}
	if !bodyHas(body, `"`+key+`"`) {
		return body
	}
	r := gjson.GetBytes(body, key)
	switch {
	case !r.Exists() || r.Type == gjson.Null:
		return body
	case r.IsArray() && len(r.Array()) == 0, r.Type == gjson.String && r.Str == "":
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return body
	}
	delete(m, key)
	out, err := marshalPlain(m)
	if err != nil {
		return body
	}
	return out
}

// withoutNullReasoningContent is a Responses body with the null content of
// a reasoning item gone (#3392).
func withoutNullReasoningContent(body []byte) []byte {
	if !bodyHas(body, `"reasoning"`) || !bodyHas(body, `"content"`) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return body
	}
	dirty := false
	items, _ := m["input"].([]any)
	for _, it := range items {
		im, _ := it.(map[string]any)
		if im == nil || im["type"] != "reasoning" {
			continue
		}
		if c, there := im["content"]; there && c == nil {
			delete(im, "content")
			dirty = true
		}
	}
	if !dirty {
		return body
	}
	out, err := marshalPlain(m)
	if err != nil {
		return body
	}
	return out
}

// systemTurnsFolded is an Anthropic body with each system turn in messages
// folded into the top-level system, its text and blocks as they came
// (#3392).
func systemTurnsFolded(body []byte) []byte {
	if !bodyHas(body, `"messages"`) || !bodyHas(body, `"system"`) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return body
	}
	msgs, _ := m["messages"].([]any)
	kept := msgs[:0:0]
	var folded []any // each system turn's content, a string or blocks
	for _, mm := range msgs {
		im, _ := mm.(map[string]any)
		if im == nil || im["role"] != "system" {
			kept = append(kept, mm)
			continue
		}
		switch c := im["content"].(type) {
		case string:
			folded = append(folded, c)
		case []any:
			folded = append(folded, c...)
		}
	}
	if len(kept) == len(msgs) {
		return body
	}
	m["messages"] = kept
	// the system prompt: what was there, then each turn's text and blocks
	var blocks []any
	switch s := m["system"].(type) {
	case string:
		if s != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": s})
		}
	case []any:
		blocks = append(blocks, s...)
	}
	for _, f := range folded {
		if s, ok := f.(string); ok {
			if s != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": s})
			}
			continue
		}
		blocks = append(blocks, f)
	}
	if len(blocks) > 0 {
		m["system"] = blocks
	} else {
		delete(m, "system")
	}
	out, err := marshalPlain(m)
	if err != nil {
		return body
	}
	return out
}

// toolsRequiredFilled is a body whose tool schemas give every object node
// its required (#3392). xAI validates each tool's schema and takes a
// required left out as null, refusing both; Claude Code leaves it out on
// the tools that take no arguments, and an MCP server's schema can say
// null. The schemas are found under every shape a body leaves magpie in:
// input_schema (Anthropic), parameters (Responses) and function's
// parameters (chat). Only the tools array is written again: the rest of
// the body keeps its bytes, and a body whose objects all carry their
// required keeps all of them.
func toolsRequiredFilled(body []byte) []byte {
	if !bodyHas(body, `"tools"`) {
		return body
	}
	r := gjson.GetBytes(body, "tools")
	if !r.IsArray() {
		return body
	}
	dec := json.NewDecoder(strings.NewReader(r.Raw))
	dec.UseNumber()
	var tools []any
	if dec.Decode(&tools) != nil {
		return body
	}
	dirty := false
	for _, t := range tools {
		tm, _ := t.(map[string]any)
		if tm == nil {
			continue
		}
		for _, k := range []string{"parameters", "input_schema"} {
			dirty = fillRequired(tm[k]) || dirty
		}
		if fm, _ := tm["function"].(map[string]any); fm != nil {
			dirty = fillRequired(fm["parameters"]) || dirty
		}
	}
	if !dirty {
		return body
	}
	out, err := marshalPlain(tools)
	if err != nil {
		return body
	}
	return spliced(body, r, out)
}

// Where a JSON Schema holds schemas of its own: a keyword's value is one,
// or an array of them (subschemaKeys), or names each mapped to one
// (subschemaMapKeys). Values elsewhere (a default, an enum, an example)
// are data, and stay as they came.
var (
	subschemaKeys = [...]string{"items", "prefixItems", "additionalItems",
		"additionalProperties", "unevaluatedItems", "unevaluatedProperties",
		"contains", "propertyNames", "not", "if", "then", "else",
		"anyOf", "oneOf", "allOf"}
	subschemaMapKeys = [...]string{"properties", "patternProperties",
		"$defs", "definitions", "dependentSchemas"}
)

// fillRequired gives each object node of schema v, at its root and
// nested, a required of [] where it has none or a null one, and says
// whether it changed anything. A real array of names stays as it came; a
// null required on a node that is no object goes, as there it means
// nothing.
func fillRequired(v any) bool {
	s, _ := v.(map[string]any)
	if s == nil {
		return false
	}
	dirty := false
	if r, there := s["required"]; r == nil {
		if ofTypeObject(s) {
			s["required"] = []any{}
			dirty = true
		} else if there {
			delete(s, "required")
			dirty = true
		}
	}
	for _, k := range subschemaKeys {
		switch c := s[k].(type) {
		case map[string]any:
			dirty = fillRequired(c) || dirty
		case []any:
			for _, e := range c {
				dirty = fillRequired(e) || dirty
			}
		}
	}
	for _, k := range subschemaMapKeys {
		m, _ := s[k].(map[string]any)
		for _, e := range m {
			dirty = fillRequired(e) || dirty
		}
	}
	return dirty
}

// ofTypeObject says whether schema s is of type object: "object", or a
// list of types that has it.
func ofTypeObject(s map[string]any) bool {
	switch t := s["type"].(type) {
	case string:
		return t == "object"
	case []any:
		for _, e := range t {
			if e == "object" {
				return true
			}
		}
	}
	return false
}
