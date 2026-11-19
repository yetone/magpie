package provider

import (
	"strings"
)

// The ChatGPT backend compiles a function tool's parameters into a
// grammar unless the tool says "strict": false, and refuses the whole
// request over one schema the compiler can't take. Codex CLI says false
// on every function tool (codex-rs/tools ResponsesApiTool, its MCP
// tools' oneOf kept as written); a client that leaves strict out, as
// LobeHub's OpenAI provider does on Responses, got its request held to
// the compiler's rules: 400 "Invalid schema for 'tools[3].parameters':
// oneOf constraints are not supported" (Anotiaww on X, LobeHub's
// calculator, whose defintegrate bounds are a oneOf of string and
// number), and "regex lookaround is not supported" for a pattern (tried
// 2026-11-19: left out it is refused, false is taken).
//
// A tool that asks for strict is held to it where its schema can be:
// a oneOf is asked as anyOf, which the backend takes in a strict schema
// and its own error suggests ("approximate oneOf with anyOf"); to a
// model choosing what to write, one of the branches is what either
// says. A schema strict mode still can't take — a keyword it refuses
// (allOf, not, if, uniqueItems, minProperties …), an object whose
// properties aren't all required or that allows more, a lookaround in
// a pattern — goes as written with strict false, as the backend would
// refuse the request otherwise. Each rule was tried on the backend on
// 2026-11-19 with strict true.

// codexToolStrict gives every function tool of m, a Responses request for
// the ChatGPT backend, a strict the backend can compile, as above: where
// the tools are read is where codexToolNames reads them. It says whether
// it changed one.
func codexToolStrict(m map[string]any) bool {
	changed := false
	var tools func(ts []any)
	tools = func(ts []any) {
		for _, t := range ts {
			tm, _ := t.(map[string]any)
			switch tm["type"] {
			case "function":
				if codexStrictTool(tm) {
					changed = true
				}
			case "namespace":
				nested, _ := tm["tools"].([]any)
				tools(nested)
			}
		}
	}
	ts, _ := m["tools"].([]any)
	tools(ts)
	input, _ := m["input"].([]any)
	for _, raw := range input {
		it, _ := raw.(map[string]any)
		switch it["type"] {
		case "additional_tools", "tool_search_output":
			nested, _ := it["tools"].([]any)
			tools(nested)
		}
	}
	return changed
}

// codexStrictTool sets one function tool's strict as codexToolStrict
// says, and says whether it changed the tool.
func codexStrictTool(t map[string]any) bool {
	if s, ok := t["strict"].(bool); !ok {
		// left out (or not a bool): false, as Codex says it
		t["strict"] = false
		return true
	} else if !s {
		return false
	}
	params, _ := t["parameters"].(map[string]any)
	if params == nil {
		t["strict"] = false
		return true
	}
	if !hasKey(params, "oneOf") {
		if strictSchema(params, true) {
			return false
		}
		t["strict"] = false
		return true
	}
	asAny, ok := oneOfAsAnyOf(params).(map[string]any)
	if ok && strictSchema(asAny, true) {
		t["parameters"] = asAny
	} else {
		t["strict"] = false
	}
	return true
}

// hasKey reports whether key is a key of an object anywhere in v.
func hasKey(v any, key string) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			if k == key || hasKey(c, key) {
				return true
			}
		}
	case []any:
		for _, c := range x {
			if hasKey(c, key) {
				return true
			}
		}
	}
	return false
}

// schemaMaps are the keywords whose value is an object of schemas by
// name; schemaOne's value is one schema; schemaList's a list of them.
var (
	schemaMaps = []string{"properties", "$defs", "definitions", "patternProperties", "dependentSchemas"}
	schemaOne  = []string{"items", "additionalProperties", "not", "if", "then", "else", "contains", "propertyNames", "unevaluatedProperties", "unevaluatedItems", "contentSchema"}
	schemaList = []string{"anyOf", "oneOf", "allOf", "prefixItems"}
)

// oneOfAsAnyOf is a copy of the schema s with every oneOf keyword (not a
// property of that name) named anyOf, at any depth. A schema with both
// keeps its oneOf: the two say more together than an anyOf can.
func oneOfAsAnyOf(s any) any {
	switch x := s.(type) {
	case []any:
		out := make([]any, len(x))
		for i, c := range x {
			out[i] = oneOfAsAnyOf(c)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, v := range x {
			out[k] = v
		}
		for _, k := range schemaMaps {
			if m, ok := x[k].(map[string]any); ok {
				c := make(map[string]any, len(m))
				for n, sub := range m {
					c[n] = oneOfAsAnyOf(sub)
				}
				out[k] = c
			}
		}
		for _, k := range append(schemaOne, schemaList...) {
			if v, ok := x[k]; ok {
				out[k] = oneOfAsAnyOf(v)
			}
		}
		if one, ok := out["oneOf"]; ok {
			if _, both := out["anyOf"]; !both {
				delete(out, "oneOf")
				out["anyOf"] = one
			}
		}
		return out
	}
	return s
}

// strictKeys are the keywords a strict schema may say, as the backend
// took each with strict true; any other is refused or untried.
var strictKeys = map[string]bool{
	"type": true, "properties": true, "required": true, "additionalProperties": true,
	"items": true, "anyOf": true, "$ref": true, "$defs": true, "definitions": true,
	"enum": true, "const": true, "description": true, "title": true, "default": true,
	"pattern": true, "format": true, "minLength": true, "maxLength": true,
	"minimum": true, "maximum": true, "exclusiveMinimum": true, "exclusiveMaximum": true,
	"multipleOf": true, "minItems": true, "maxItems": true, "$schema": true, "$id": true,
	"$comment": true, "examples": true,
}

// strictSchema reports whether the backend takes s in a strict schema:
// root is the parameters themselves, which have to be an object.
func strictSchema(s map[string]any, root bool) bool {
	for k := range s {
		if !strictKeys[k] {
			return false
		}
	}
	if root && s["type"] != "object" {
		return false
	}
	if p, ok := s["pattern"].(string); ok && (strings.Contains(p, "(?=") || strings.Contains(p, "(?!") || strings.Contains(p, "(?<=") || strings.Contains(p, "(?<!")) {
		return false
	}
	_, typed := s["type"]
	_, union := s["anyOf"]
	_, ref := s["$ref"]
	_, enum := s["enum"]
	if !typed && !union && !ref && !enum {
		// "schema must specify a type": a const alone is refused too
		return false
	}
	if isType(s["type"], "object") {
		props, ok := s["properties"].(map[string]any)
		if !ok || s["additionalProperties"] != false {
			return false
		}
		req := map[string]bool{}
		list, _ := s["required"].([]any)
		for _, r := range list {
			if n, ok := r.(string); ok {
				req[n] = true
			}
		}
		for n, sub := range props {
			sm, ok := sub.(map[string]any)
			if !req[n] || !ok || !strictSchema(sm, false) {
				return false
			}
		}
	} else if _, ok := s["additionalProperties"]; ok {
		return false
	}
	if isType(s["type"], "array") {
		items, ok := s["items"].(map[string]any)
		if !ok || !strictSchema(items, false) {
			return false
		}
	}
	if u, ok := s["anyOf"]; ok {
		list, ok := u.([]any)
		if !ok || len(list) == 0 {
			return false
		}
		for _, b := range list {
			bm, ok := b.(map[string]any)
			if !ok || !strictSchema(bm, false) {
				return false
			}
		}
	}
	for _, k := range []string{"$defs", "definitions"} {
		if d, ok := s[k]; ok {
			defs, ok := d.(map[string]any)
			if !ok {
				return false
			}
			for _, sub := range defs {
				sm, ok := sub.(map[string]any)
				if !ok || !strictSchema(sm, false) {
					return false
				}
			}
		}
	}
	return true
}

// isType reports whether a schema's type says name: the word, or a list
// holding it.
func isType(t any, name string) bool {
	switch x := t.(type) {
	case string:
		return x == name
	case []any:
		for _, v := range x {
			if v == name {
				return true
			}
		}
	}
	return false
}
