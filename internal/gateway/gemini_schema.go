package gateway

import (
	"strings"

	"github.com/tidwall/gjson"
)

// geminiToolTypes is body with a type written into each schema of its
// tools' that has none, for a Gemini model whoever serves it. Vertex AI
// takes a function declaration's parameters as its own Schema object, whose
// type is required, and turns the whole request away over one schema
// without it, though the tool is never called: 400 "Unable to submit
// request because `mcp__cua-driver__drag` functionDeclaration
// `parameters.target` schema didn't specify the schema type field."
// (kthink on Discord: dsh with cua-driver's MCP tools, Command Code's
// google/gemini-3.8-flash, which its gateway serves on Vertex). Whoever
// relays a Chat, Responses or Messages body to Vertex — a plugin's
// gateway, a router, a relay — turns the schema into Vertex's, and an
// untyped node stays untyped, so it is keyed on the model, not the
// provider.
//
// Each schema with no type is given the one its values (enum, const,
// default), keywords (properties: object, items: array, pattern:
// string …; an object's or array's before its values) or branches (anyOf, oneOf, allOf, a local $ref) say, null
// left out: Vertex has one type a node, nullable apart. A node whose
// branches have more than one type, or a $ref or union whose types can't
// be told, is left as it came: no one type is right for it. One that says
// nothing ({} or only a description) is a string, the value that carries
// anything; a tool's root is an object. "not" and "if" are left alone: a
// type there would turn the rule round. The type is written in place,
// after the schema's brace, so the rest of the body and a cache's prefix
// stay as they were; every other model's body goes as it came.
//
// Boolean schemas and x- keywords, which a Schema object can't hold
// either, are taken out first (geminiSchemaShapes).
//
// Google's own Gemini API and Code Assist are sent parametersJsonSchema,
// which takes JSON Schema whole, and Antigravity its own cut-down schema
// (plainSchema), so a Gemini-shaped body isn't walked.
func geminiToolTypes(body []byte) []byte { return geminiTypes(body, false) }

// geminiTypes is geminiToolTypes, with keepUnions leaving a union node
// (anyOf or oneOf) without the type written beside it: Copilot's Gemini
// turns the request away over `type: string` next to a nullable
// `anyOf: [string, null]` (#4374, isolated by one field in a live request),
// whatever the union's branches say, so a union goes as its client wrote it.
// Its branches are typed as ever.
func geminiTypes(body []byte, keepUnions bool) []byte {
	if !geminiNamed(gjson.GetBytes(body, "model").String()) {
		return body
	}
	body = geminiSchemaShapes(body)
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() || tools.Index <= 0 {
		return body
	}
	var ins []typeInsert
	tools.ForEach(func(_, tool gjson.Result) bool {
		for _, at := range []string{"function.parameters", "parameters", "input_schema"} {
			if root := tool.Get(at); root.IsObject() {
				collectGeminiUntyped(body, root, root, true, keepUnions, &ins)
			}
		}
		return true
	})
	return withTypes(body, ins)
}

// geminiNamed is whether a model id names a Gemini model: gemini-…, or
// a relay's or router's vendor/gemini-… (google/gemini-3.8-flash,
// vertex_ai/gemini-…).
func geminiNamed(id string) bool {
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	return strings.HasPrefix(strings.ToLower(id), "gemini")
}

// collectGeminiUntyped adds, for node and each schema under it that has no
// type, where it opens and the type Vertex is told.
func collectGeminiUntyped(body []byte, root, node gjson.Result, isRoot, keepUnions bool, ins *[]typeInsert) {
	switch {
	case node.IsArray():
		node.ForEach(func(_, v gjson.Result) bool {
			collectGeminiUntyped(body, root, v, false, keepUnions, ins)
			return true
		})
	case node.IsObject():
		union := keepUnions && !isRoot && (node.Get("anyOf").Exists() || node.Get("oneOf").Exists())
		if !union && !node.Get("type").Exists() && node.Index > 0 && node.Index < len(body) && body[node.Index] == '{' {
			if t := geminiSchemaType(root, node, isRoot); t != "" {
				*ins = append(*ins, typeInsert{node.Index, `"` + t + `"`})
			}
		}
		node.ForEach(func(key, v gjson.Result) bool {
			switch key.String() {
			case "properties", "$defs", "definitions", "dependentSchemas", "patternProperties":
				v.ForEach(func(_, s gjson.Result) bool {
					collectGeminiUntyped(body, root, s, false, keepUnions, ins)
					return true
				})
			case "items", "additionalProperties", "contains", "propertyNames", "then", "else", "allOf", "anyOf", "oneOf", "prefixItems", "unevaluatedProperties", "unevaluatedItems", "contentSchema":
				collectGeminiUntyped(body, root, v, false, keepUnions, ins)
			}
			return true
		})
	}
}

// geminiSchemaType is the one type an untyped schema s is given for
// Vertex, "" when it is left as it came.
func geminiSchemaType(root, s gjson.Result, isRoot bool) string {
	if t := keywordType(s); t == "object" || t == "array" {
		// properties or items say it before its values do: a schema
		// with items and a string enum given "string" went to Google
		// with items on a string, and Antigravity turned the whole turn
		// away ("properties[display].items: field predicate failed:
		// $type == Type.ARRAY", ferock on Discord)
		return t
	}
	vals := s.Get("enum").Array()
	if c := s.Get("const"); c.Exists() {
		vals = []gjson.Result{c}
	}
	if s.Get("enum").IsArray() || s.Get("const").Exists() {
		var rest []gjson.Result
		for _, v := range vals {
			if v.Type != gjson.Null {
				rest = append(rest, v)
			}
		}
		return valuesType(rest)
	}
	ts := schemaTypes(root, s, 0)
	delete(ts, "null")
	if ts["integer"] && ts["number"] {
		delete(ts, "integer")
	}
	if len(ts) == 1 {
		for t := range ts {
			return t
		}
	}
	if len(ts) > 1 {
		return ""
	}
	if isRoot {
		return "object"
	}
	for _, k := range []string{`\$ref`, "anyOf", "oneOf", "allOf"} {
		if s.Get(k).Exists() {
			return ""
		}
	}
	if d := s.Get("default"); d.Exists() && d.Type != gjson.Null {
		switch {
		case d.IsObject():
			return "object"
		case d.IsArray():
			return "array"
		}
		if t := valuesType([]gjson.Result{d}); t != "" {
			return t
		}
	}
	return "string"
}

// geminiSchemaShapes is body with each tool schema a Gemini model is
// sent cut to what a Schema object can hold, wherever it is turned into
// one. Two things in valid JSON Schema have no place there, and Google
// turns the whole request away over either:
//
//   - a boolean schema (items: true, a property's true or false): "Invalid
//     value at '…items.items' (…aiplatform.master.Schema), true" (han_tiv
//     on Discord, an MCP tool's array of arrays of anything). True is {},
//     any value, and is typed below like any other {}; false, no value, is
//     left out: a property of it, a branch of it, a keyword of it.
//     additionalProperties and the unevaluated ones keep theirs, which a
//     relay already knows to drop.
//   - an x- keyword, a vendor's own annotation, which no model reads: MCP's
//     x-mcp-header (the 2026-07-28 spec, SEP-2243) on a property, which a
//     relay to Vertex answered 'Unknown name "x-mcp-header" at …
//     properties[2].value: Cannot find field'. Only a schema's own keys
//     are keywords: a property named x-… stays.
//
// A schema with neither goes byte for byte as it came; one with either is
// written again in its own order.
func geminiSchemaShapes(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body
	}
	type cut struct {
		from, to int
		raw      string
	}
	var cuts []cut
	tools.ForEach(func(_, tool gjson.Result) bool {
		for _, at := range []string{"function.parameters", "parameters", "input_schema"} {
			root := tool.Get(at)
			if !root.IsObject() || root.Index <= 0 || !schemaNeedsShape(root, true) {
				continue
			}
			if raw, keep := shapeSchema(root); keep && root.Index+len(root.Raw) <= len(body) && string(body[root.Index:root.Index+len(root.Raw)]) == root.Raw {
				cuts = append(cuts, cut{root.Index, root.Index + len(root.Raw), raw})
			}
		}
		return true
	})
	if len(cuts) == 0 {
		return body
	}
	out := make([]byte, 0, len(body))
	last := 0
	for _, c := range cuts {
		if c.from < last {
			continue
		}
		out = append(out, body[last:c.from]...)
		out = append(out, c.raw...)
		last = c.to
	}
	return append(out, body[last:]...)
}

// schemaMaps are the keywords whose value is a map of names to schemas,
// schemaOne those whose value is one schema, schemaList a list of them.
// additionalProperties, unevaluatedProperties and unevaluatedItems are
// left out on purpose: their false is the common "nothing else", which
// every converter knows.
var (
	schemaMaps = map[string]bool{"properties": true, "patternProperties": true, "$defs": true, "definitions": true, "dependentSchemas": true}
	schemaOne  = map[string]bool{"items": true, "contains": true, "propertyNames": true, "not": true, "if": true, "then": true, "else": true, "contentSchema": true}
	schemaList = map[string]bool{"anyOf": true, "oneOf": true, "allOf": true, "prefixItems": true}
)

// schemaNeedsShape is whether node, a schema when isSchema, holds a
// boolean schema or an x- keyword anywhere under it.
func schemaNeedsShape(node gjson.Result, isSchema bool) bool {
	if isSchema && (node.Type == gjson.True || node.Type == gjson.False) {
		return true
	}
	if !node.IsObject() || !isSchema {
		return false
	}
	need := false
	node.ForEach(func(key, v gjson.Result) bool {
		k := key.String()
		switch {
		case strings.HasPrefix(k, "x-"):
			need = true
		case schemaMaps[k]:
			v.ForEach(func(_, s gjson.Result) bool {
				need = schemaNeedsShape(s, true)
				return !need
			})
		case schemaOne[k]:
			need = schemaNeedsShape(v, true)
		case schemaList[k]:
			v.ForEach(func(_, s gjson.Result) bool {
				need = schemaNeedsShape(s, true)
				return !need
			})
		}
		return !need
	})
	return need
}

// shapeSchema is schema s written again without its boolean schemas and
// x- keywords; keep is false when s itself is false, no schema at all.
func shapeSchema(s gjson.Result) (raw string, keep bool) {
	switch s.Type {
	case gjson.True:
		return "{}", true
	case gjson.False:
		return "", false
	}
	if !s.IsObject() || !schemaNeedsShape(s, true) {
		return s.Raw, true
	}
	var b strings.Builder
	b.WriteByte('{')
	first := true
	put := func(key, val string) {
		if !first {
			b.WriteByte(',')
		}
		first = false
		b.WriteString(key)
		b.WriteByte(':')
		b.WriteString(val)
	}
	s.ForEach(func(key, v gjson.Result) bool {
		k := key.String()
		switch {
		case strings.HasPrefix(k, "x-"):
		case schemaMaps[k] && v.IsObject():
			var m strings.Builder
			m.WriteByte('{')
			n := 0
			v.ForEach(func(name, sub gjson.Result) bool {
				if r, ok := shapeSchema(sub); ok {
					if n > 0 {
						m.WriteByte(',')
					}
					n++
					m.WriteString(name.Raw)
					m.WriteByte(':')
					m.WriteString(r)
				}
				return true
			})
			m.WriteByte('}')
			put(key.Raw, m.String())
		case schemaOne[k]:
			if r, ok := shapeSchema(v); ok {
				put(key.Raw, r)
			}
		case schemaList[k] && v.IsArray():
			var m strings.Builder
			m.WriteByte('[')
			n := 0
			v.ForEach(func(_, sub gjson.Result) bool {
				if r, ok := shapeSchema(sub); ok {
					if n > 0 {
						m.WriteByte(',')
					}
					n++
					m.WriteString(r)
				}
				return true
			})
			m.WriteByte(']')
			put(key.Raw, m.String())
		default:
			put(key.Raw, v.Raw)
		}
		return true
	})
	b.WriteByte('}')
	return b.String(), true
}
