package gateway

import (
	"bytes"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// toolOneOfAsAnyOf is body with every oneOf in its tools' schemas named
// anyOf, for the routers whose models are served behind a grammar
// compiler (xgrammar) that refuses oneOf: OpenRouter's ModelRun answered
// a tool of Codex's (mcp__codex_app__automation_update) "unsupported
// schema keyword: oneOf with overlapping or non-provably-disjoint
// branches", 400, through Cline (H20 on Discord), and takes anyOf. To a
// model choosing what to call, one of the branches is what either says.
// The key is renamed in place, the same length, so nothing else of the
// body changes and a cache keeps its prefix.
func toolOneOfAsAnyOf(p provider.Provider, to provider.Protocol, body []byte) []byte {
	if !(p.IsCline() || p.IsKilo() || provider.HostOf(p.Base(to)) == "openrouter.ai") {
		return body
	}
	if !bytes.Contains(body, []byte(`"oneOf"`)) {
		return body
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() || tools.Index <= 0 {
		return body
	}
	var at []int
	collectOneOf(body, tools, false, &at)
	if len(at) == 0 {
		return body
	}
	out := bytes.Clone(body)
	for _, i := range at {
		copy(out[i:], `"anyOf"`)
	}
	return out
}

// collectOneOf adds where each oneOf key of a schema under node starts in
// body: a key, not a property of that name.
func collectOneOf(body []byte, node gjson.Result, inSchema bool, at *[]int) {
	switch {
	case node.IsArray():
		node.ForEach(func(_, v gjson.Result) bool {
			collectOneOf(body, v, inSchema, at)
			return true
		})
	case node.IsObject():
		node.ForEach(func(key, v gjson.Result) bool {
			k := key.String()
			if !inSchema {
				switch k {
				case "parameters", "input_schema":
					collectOneOf(body, v, true, at)
				case "function":
					collectOneOf(body, v, false, at)
				}
				return true
			}
			switch k {
			case "properties", "$defs", "definitions", "dependentSchemas", "patternProperties":
				v.ForEach(func(_, s gjson.Result) bool {
					collectOneOf(body, s, true, at)
					return true
				})
			case "oneOf":
				if i := keyBefore(body, v.Index, `"oneOf"`); i >= 0 {
					*at = append(*at, i)
				}
				collectOneOf(body, v, true, at)
			case "items", "additionalProperties", "contains", "propertyNames", "not", "if", "then", "else", "allOf", "anyOf", "prefixItems", "unevaluatedProperties", "unevaluatedItems", "contentSchema":
				collectOneOf(body, v, true, at)
			}
			return true
		})
	}
}

// keyBefore is where key, as written, starts before the value at i in
// body (past the colon and any space), or -1 when it isn't written so.
func keyBefore(body []byte, i int, key string) int {
	if i <= 0 || i > len(body) {
		return -1
	}
	j := i - 1
	for j >= 0 && (body[j] == ' ' || body[j] == '\t' || body[j] == '\n' || body[j] == '\r') {
		j--
	}
	if j < 0 || body[j] != ':' {
		return -1
	}
	j--
	for j >= 0 && (body[j] == ' ' || body[j] == '\t' || body[j] == '\n' || body[j] == '\r') {
		j--
	}
	if s := j + 1 - len(key); s >= 0 && string(body[s:j+1]) == key {
		return s
	}
	return -1
}
