package gateway

import (
	"bytes"
	"strconv"
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

// kimiLockedSampling is body without temperature and top_p when the model's
// server locks them to one value: since kimi-k2.5 every kimi-kX.Y on both
// Kimi platforms (Moonshot's API and a Kimi Code membership's), and Kimi
// Code's own kimi-for-coding and k3*. A client that sends its own
// defaults — VS Code Copilot's chat sends temperature 0.1 and top_p 1 — is
// refused 400 "invalid temperature: only 1 is allowed for this model"
// before any token is read (a K2.5/K2.6 with thinking off takes only 0.6),
// and the whole request fails. Left out, the server fills in the value the
// model and mode want, so no number is written here. Models that take a
// range — moonshot-v1-*, kimi-k2-0905-preview, kimi-k2-thinking,
// kimi-latest — keep what the user set, as does anyone not served by
// Moonshot.
func kimiLockedSampling(p provider.Provider, to provider.Protocol, body []byte) []byte {
	if !isKimi(p, to) {
		return body
	}
	if !kimiLockedSamplingModel(gjson.GetBytes(body, "model").String()) {
		return body
	}
	return withoutFields(body, "temperature", "top_p")
}

// kimiLockedSamplingModel is whether the model's server locks temperature
// and top_p: kimi-for-coding* and k3* on Kimi Code, and kimi-k2.5 and
// later (kimi-k2.6, kimi-k3, …) on either platform. A leading "vendor/" is
// ignored.
func kimiLockedSamplingModel(model string) bool {
	m := strings.ToLower(model)
	if i := strings.LastIndexByte(m, '/'); i >= 0 {
		m = m[i+1:]
	}
	if strings.HasPrefix(m, "kimi-for-coding") {
		return true
	}
	v, ok := strings.CutPrefix(m, "kimi-k")
	if !ok {
		if v, ok = strings.CutPrefix(m, "k"); !ok {
			return false
		}
	}
	major, rest := leadingInt(v)
	if major < 0 {
		return false
	}
	if major != 2 {
		return major >= 3
	}
	if !strings.HasPrefix(rest, ".") {
		return false // kimi-k2-thinking, kimi-k2-0905-preview
	}
	minor, _ := leadingInt(rest[1:])
	return minor >= 5
}

// leadingInt reads the decimal integer s begins with, -1 when there is
// none, and what follows it.
func leadingInt(s string) (int, string) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return -1, s
	}
	n, err := strconv.Atoi(s[:i])
	if err != nil {
		return -1, s
	}
	return n, s[i:]
}
