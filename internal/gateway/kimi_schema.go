package gateway

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// kimiToolEnumTypes is body with a type written into each schema of its
// tools' that has an enum (or a const) and no type, for Kimi and Moonshot:
// their "moonshot flavored json schema" refuses one, 400 "tools.function.
// parameters is not a valid moonshot flavored json schema … type is not
// defined" (#886, cua-driver's parse_visual_regions, whose kinds' items are
// {"enum":["text","icon"]}), and the whole request fails though the tool is
// never called. The type is the one every value has — string, integer,
// number or boolean; values of mixed types, or a null among them, are left
// as they were. It is written in place, after the schema's brace, so the
// rest of the body and a cache's prefix stay as they were.
func kimiToolEnumTypes(p provider.Provider, to provider.Protocol, body []byte) []byte {
	if !isKimi(p, to) {
		return body
	}
	if !bytes.Contains(body, []byte(`"enum"`)) && !bytes.Contains(body, []byte(`"const"`)) {
		return body
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() || tools.Index <= 0 {
		return body
	}
	var ins []typeInsert
	collectUntypedEnums(body, tools, false, &ins)
	if len(ins) == 0 {
		return body
	}
	// the walk goes through the body in order, so the inserts ascend
	var out bytes.Buffer
	out.Grow(len(body) + len(ins)*20)
	last := 0
	for _, in := range ins {
		out.Write(body[last : in.at+1])
		out.WriteString(`"type":"` + in.typ + `",`)
		last = in.at + 1
	}
	out.Write(body[last:])
	return out.Bytes()
}

// isKimi is whether p is served by Moonshot: the Kimi platform's API or a
// Kimi Code membership's.
func isKimi(p provider.Provider, to provider.Protocol) bool {
	switch provider.HostOf(p.Base(to)) {
	case "api.moonshot.ai", "api.moonshot.cn", "api.kimi.com", "api.kimi.ai":
		return true
	}
	return strings.HasPrefix(p.Preset, "moonshot") || strings.HasPrefix(p.Preset, "kimi")
}

type typeInsert struct {
	at  int // the schema's opening brace
	typ string
}

// collectUntypedEnums adds, for each schema under node with an enum or a
// const and no type, where it opens and the type its values have.
func collectUntypedEnums(body []byte, node gjson.Result, inSchema bool, ins *[]typeInsert) {
	switch {
	case node.IsArray():
		node.ForEach(func(_, v gjson.Result) bool {
			collectUntypedEnums(body, v, inSchema, ins)
			return true
		})
	case node.IsObject():
		if inSchema && node.Index > 0 && node.Index < len(body) && body[node.Index] == '{' && !node.Get("type").Exists() {
			var vals []gjson.Result
			if e := node.Get("enum"); e.IsArray() {
				vals = e.Array()
			} else if c := node.Get("const"); c.Exists() {
				vals = []gjson.Result{c}
			}
			if t := valuesType(vals); t != "" {
				*ins = append(*ins, typeInsert{node.Index, t})
			}
		}
		node.ForEach(func(key, v gjson.Result) bool {
			k := key.String()
			if !inSchema {
				switch k {
				case "parameters", "input_schema":
					collectUntypedEnums(body, v, true, ins)
				case "function":
					collectUntypedEnums(body, v, false, ins)
				}
				return true
			}
			switch k {
			case "properties", "$defs", "definitions", "dependentSchemas", "patternProperties":
				v.ForEach(func(_, s gjson.Result) bool {
					collectUntypedEnums(body, s, true, ins)
					return true
				})
			case "items", "additionalProperties", "contains", "propertyNames", "not", "if", "then", "else", "allOf", "anyOf", "oneOf", "prefixItems", "unevaluatedProperties", "unevaluatedItems", "contentSchema":
				collectUntypedEnums(body, v, true, ins)
			}
			return true
		})
	}
}

// valuesType is the JSON Schema type all of vals have, "" when there are
// none or they differ; whole numbers among fractions make them number.
func valuesType(vals []gjson.Result) string {
	t := ""
	for _, v := range vals {
		var vt string
		switch v.Type {
		case gjson.String:
			vt = "string"
		case gjson.True, gjson.False:
			vt = "boolean"
		case gjson.Number:
			vt = "integer"
			if strings.ContainsAny(v.Raw, ".eE") {
				vt = "number"
			}
		default:
			return ""
		}
		switch {
		case t == "" || t == vt:
			t = vt
		case t == "integer" && vt == "number" || t == "number" && vt == "integer":
			t = "number"
		default:
			return ""
		}
	}
	return t
}

// kimiSamplingParams is body with the sampling parameters Kimi Code's
// servers validate against a per-model whitelist brought in line: a client
// that sends its own defaults (VS Code Copilot's chat, temperature 0.1 and
// top_p 1) is refused 400 "invalid temperature: only 1 is allowed for this
// model" before any token is read, and the whole request fails. The values
// are those Kimi Code accepts: temperature 1 and top_p 0.95. A request
// already carrying them, or going to another provider, is left as it was.
func kimiSamplingParams(p provider.Provider, to provider.Protocol, body []byte) []byte {
	if !isKimi(p, to) {
		return body
	}
	fixed := []struct {
		key string
		val string
	}{
		{"temperature", "1"},
		{"top_p", "0.95"},
	}
	changed := false
	for _, f := range fixed {
		v := gjson.GetBytes(body, f.key)
		switch {
		case !v.Exists():
			// insert after the opening brace; the body's own fields follow
			var out bytes.Buffer
			out.Grow(len(body) + 24)
			out.WriteByte('{')
			out.WriteString(`"` + f.key + `":` + f.val + `,`)
			out.Write(body[1:])
			body = out.Bytes()
			changed = true
		case v.Raw != f.val:
			body = bytes.Replace(body, []byte(`"`+f.key+`":`+v.Raw), []byte(`"`+f.key+`":`+f.val), 1)
			changed = true
		}
	}
	if !changed {
		return body
	}
	return body
}
