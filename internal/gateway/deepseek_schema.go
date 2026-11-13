package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// deepseekToolPatterns normalizes the ECMAScript null escape \0 in tool schemas
// to the Unicode escape \u0000. DeepSeek's schema validator rejects \0 with:
// "Invalid schema for function ... is not a 'regex'".
//
// To avoid re-encoding the entire payload or tool array (which reorders keys,
// escapes <, > and &, and breaks prompt caching), it finds only the offending
// "pattern" fields in tool schemas using gjson and splices the normalized string
// literals in place from the end of the body backwards.
func deepseekToolPatterns(p provider.Provider, to provider.Protocol, body []byte) []byte {
	if p.VendorPreset() != "deepseek" && provider.HostOf(p.Base(to)) != "api.deepseek.com" && p.VendorHost() != "api.deepseek.com" {
		return body
	}
	if !bodyHas(body, `\\0`) {
		return body
	}
	return toolPatterns(body, unicodeNullEscape)
}

// toolPatterns is body with fix applied to every "pattern" of its tools'
// schemas, each spliced in place so the rest of the body stays byte for
// byte as it came.
func toolPatterns(body []byte, fix func(string) string) []byte {
	toolsRes := gjson.GetBytes(body, "tools")
	if !toolsRes.Exists() || toolsRes.Index <= 0 || !toolsRes.IsArray() {
		return body
	}

	var targets []patternTarget
	collectPatterns(toolsRes, false, fix, &targets)
	if len(targets) == 0 {
		return body
	}

	// Sort targets ascending by Index so we can copy cleanly forward into a
	// new buffer without modifying the caller's slice or its backing array.
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].index < targets[j].index
	})

	out := make([]byte, 0, len(body)+6*len(targets))
	last := 0
	for _, t := range targets {
		if t.index < last || t.index+t.rawLen > len(body) {
			continue
		}
		out = append(out, body[last:t.index]...)
		out = append(out, t.replacement...)
		last = t.index + t.rawLen
	}
	out = append(out, body[last:]...)
	return out
}

type patternTarget struct {
	index       int
	rawLen      int
	replacement string
}

func collectPatterns(node gjson.Result, inSchema bool, fix func(string) string, targets *[]patternTarget) {
	if !node.Exists() {
		return
	}
	if node.IsObject() {
		node.ForEach(func(key, value gjson.Result) bool {
			k := key.String()
			if inSchema {
				if k == "pattern" && value.Type == gjson.String && value.Index > 0 {
					var pattern string
					if err := json.Unmarshal([]byte(value.Raw), &pattern); err == nil {
						if norm := fix(pattern); norm != pattern {
							*targets = append(*targets, patternTarget{
								index:       value.Index,
								rawLen:      len(value.Raw),
								replacement: encodeJSONStringNoHTMLEscape(norm),
							})
						}
					}
				}
				switch k {
				case "properties", "$defs", "definitions", "dependentSchemas", "patternProperties":
					if value.IsObject() {
						value.ForEach(func(_, schema gjson.Result) bool {
							collectPatterns(schema, true, fix, targets)
							return true
						})
					}
				case "items", "additionalProperties", "contains", "propertyNames", "not", "if", "then", "else", "allOf", "anyOf", "oneOf", "prefixItems", "unevaluatedProperties", "unevaluatedItems", "contentSchema":
					collectPatterns(value, true, fix, targets)
				}
			} else {
				if k == "input_schema" || k == "parameters" {
					collectPatterns(value, true, fix, targets)
				} else if k == "function" {
					collectPatterns(value, false, fix, targets)
				}
			}
			return true
		})
	} else if node.IsArray() {
		node.ForEach(func(_, item gjson.Result) bool {
			collectPatterns(item, inSchema, fix, targets)
			return true
		})
	}
}

func encodeJSONStringNoHTMLEscape(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func unicodeNullEscape(pattern string) string {
	var out strings.Builder
	for i := 0; i < len(pattern); i++ {
		if pattern[i] != '\\' || i+1 == len(pattern) {
			out.WriteByte(pattern[i])
			continue
		}
		next := pattern[i+1]
		// A pair of backslashes is literal; an octal escape has its own
		// meaning and must not be shortened to a null character.
		if next == '0' && (i+2 == len(pattern) || pattern[i+2] < '0' || pattern[i+2] > '9') {
			out.WriteString(`\u0000`)
		} else {
			out.WriteByte('\\')
			out.WriteByte(next)
		}
		i++
	}
	return out.String()
}

// deepseekModel is whether id, the model as the body going upstream names
// it, is one of DeepSeek's, under whatever prefix a relay or router gives
// it (deepseek/deepseek-v4.1, openrouter's deepseek/…). DeepSeek's quirks
// are DeepSeek's whoever serves them: a relay that upstream-validates
// like DeepSeek carries a DeepSeek model's name (#5030, #4852).
func deepseekModel(id string) bool {
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	return strings.HasPrefix(strings.ToLower(id), "deepseek")
}

// deepseekTokenField names a chat request's reply length max_tokens, the
// field DeepSeek's chat API reads: it has no max_completion_tokens and
// ignores one, so a client that asks OpenAI's way (DSH's pi-ai with a
// custom provider) got DeepSeek's default, 8K without thinking (#2355).
// It is DeepSeek's API whoever serves a DeepSeek model: on a relay's own
// domain the model named in the body tells (#5030, NOMOAX), as Grok's
// schema quirk is told by its model (grokModel). Only the key is renamed,
// in place, so the rest of the body keeps its bytes. A request that names
// max_tokens too goes as it is, since DeepSeek reads that one. Another
// vendor's chat API is left alone: OpenAI's and Azure's want
// max_completion_tokens.
func deepseekTokenField(p provider.Provider, to provider.Protocol, body []byte) []byte {
	if to != provider.Chat || p.VendorPreset() != "deepseek" && provider.HostOf(p.Base(to)) != "api.deepseek.com" && p.VendorHost() != "api.deepseek.com" && !deepseekModel(gjson.GetBytes(body, "model").String()) {
		return body
	}
	if !bodyHas(body, `"max_completion_tokens"`) || gjson.GetBytes(body, "max_tokens").Exists() {
		return body
	}
	v := gjson.GetBytes(body, "max_completion_tokens")
	if !v.Exists() || v.Index <= 0 {
		return body
	}
	// the key is the last string before its value: only a colon and
	// spaces lie between them
	key := bytes.LastIndex(body[:v.Index], []byte(`"max_completion_tokens"`))
	if key < 0 || strings.Trim(string(body[key+len(`"max_completion_tokens"`):v.Index]), " \t\r\n:") != "" {
		return body
	}
	out := make([]byte, 0, len(body))
	out = append(out, body[:key]...)
	out = append(out, `"max_tokens"`...)
	return append(out, body[key+len(`"max_completion_tokens"`):]...)
}
