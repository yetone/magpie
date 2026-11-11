package gateway

import (
	"bytes"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// A Copilot Gemini model is served on chat completions alone, so Codex's
// tools go to it through the translation, schemas as the client declared
// them — and Copilot turns every tool-bearing turn of Codex Desktop's away
// 400 "invalid request body" on them (#4374, as #3931's comparisons
// showed: the same request with the unions as oneOf came back 200, and
// nothing else about the tools — descriptions, strict, required — made the
// difference). What it rejects is a tool schema's anyOf of object branches,
// so one whose branches are provably disjoint goes as oneOf, which says
// the same to a model choosing what to call: at most one branch can match,
// whichever says it.
//
// Disjoint is proved as #3931's fix did, and only so: two branches are
// apart when one requires a field of a string whose values (enum, const)
// the other's required field of that name never takes, or when one
// requires a field the other forbids through additionalProperties: false
// (with no patternProperties beside it, which would admit the name). A
// union that can't be proved — nullable ones among them — stays an anyOf.
func copilotGeminiToolUnions(p provider.Provider, to provider.Protocol, body []byte) []byte {
	if to != provider.Chat || p.Account == nil || p.Account.Agent != "copilot" ||
		!geminiNamed(gjson.GetBytes(body, "model").String()) {
		return body
	}
	if !bytes.Contains(body, []byte(`"anyOf"`)) {
		return body
	}
	var at []int
	gjson.GetBytes(body, "tools").ForEach(func(_, tool gjson.Result) bool {
		for _, path := range []string{"function.parameters", "parameters", "input_schema"} {
			collectCopilotUnions(body, tool.Get(path), &at)
		}
		return true
	})
	if len(at) == 0 {
		return body
	}
	// renamed in place, the same length, so nothing else of the body moves
	// and a cache keeps its prefix
	out := bytes.Clone(body)
	for _, i := range at {
		copy(out[i:], `"oneOf"`)
	}
	return out
}

// collectCopilotUnions adds where each anyOf of provably disjoint object
// branches under node, a tool's schema, starts in body: a key, not a
// property of that name.
func collectCopilotUnions(body []byte, node gjson.Result, at *[]int) {
	switch {
	case node.IsArray():
		node.ForEach(func(_, v gjson.Result) bool {
			collectCopilotUnions(body, v, at)
			return true
		})
	case node.IsObject():
		if union := node.Get("anyOf"); union.Exists() && !node.Get("oneOf").Exists() && disjointObjectBranches(union) {
			if i := keyBefore(body, union.Index, `"anyOf"`); i >= 0 {
				*at = append(*at, i)
			}
		}
		node.ForEach(func(key, v gjson.Result) bool {
			switch key.String() {
			case "properties", "$defs", "definitions", "dependentSchemas", "patternProperties":
				v.ForEach(func(_, s gjson.Result) bool {
					collectCopilotUnions(body, s, at)
					return true
				})
			case "items", "additionalProperties", "contains", "propertyNames", "not", "if", "then", "else", "allOf", "anyOf", "oneOf", "prefixItems", "unevaluatedProperties", "unevaluatedItems", "contentSchema":
				collectCopilotUnions(body, v, at)
			}
			return true
		})
	}
}

// disjointObjectBranches is whether union, an anyOf, is of object branches
// only and every two of them are proved apart.
func disjointObjectBranches(union gjson.Result) bool {
	if !union.IsArray() {
		return false
	}
	branches := union.Array()
	if len(branches) < 2 {
		return false
	}
	for i, a := range branches {
		if a.Get("type").String() != "object" || a.Get(`\$ref`).Exists() {
			return false
		}
		for _, b := range branches[:i] {
			if !objectBranchesApart(a, b) && !objectBranchesApart(b, a) {
				return false
			}
		}
	}
	return true
}

// objectBranchesApart is whether a is proved apart from b: a required
// field of a that b never takes. Either b forbids the field outright
// (additionalProperties: false, no patternProperties to admit it), or b
// requires it too and the string values (enum, const) the two say for it
// never meet — a field b holds optional is one b may leave out, which is
// every value of a's.
func objectBranchesApart(a, b gjson.Result) bool {
	bProps := b.Get("properties").Map()
	for _, name := range a.Get("required").Array() {
		if name.Type != gjson.String {
			continue
		}
		field := name.String()
		bProp, there := bProps[field]
		if !there {
			if b.Get("additionalProperties").Type == gjson.False && !b.Get("patternProperties").Exists() {
				return true
			}
			continue
		}
		required := false
		for _, other := range b.Get("required").Array() {
			required = required || other.Type == gjson.String && other.String() == field
		}
		av, bv := schemaStringValues(a.Get("properties").Map()[field]), schemaStringValues(bProp)
		if !required || len(av) == 0 || len(bv) == 0 {
			continue
		}
		shared := false
		for v := range av {
			shared = shared || bv[v]
		}
		if !shared {
			return true
		}
	}
	return false
}

// schemaStringValues is the string values (enum, const) of schema, nil
// when they can't be read whole (a $ref, a value that isn't a string).
func schemaStringValues(schema gjson.Result) map[string]bool {
	if schema.Get(`\$ref`).Exists() {
		return nil
	}
	values := schema.Get("enum").Array()
	if c := schema.Get("const"); c.Type == gjson.String {
		values = []gjson.Result{c}
	}
	out := map[string]bool{}
	for _, v := range values {
		if v.Type != gjson.String {
			return nil
		}
		out[v.String()] = true
	}
	return out
}

// copilotGemini is whether body goes to a Gemini model served by a Copilot
// account.
func copilotGemini(p provider.Provider, body []byte) bool {
	return p.Account != nil && p.Account.Agent == "copilot" && geminiNamed(gjson.GetBytes(body, "model").String())
}

// copilotGrokFlat is a Responses body to Copilot's Grok with Codex's
// namespaced functions flat and their parameters objects at the root: the
// backend behind Copilot turns a request with either away, and Copilot's
// word for it is a bare status (#4374), so nothing teaches magpie to do it
// after a refusal. The calls come back under their namespaces
// (namespacedIn). Another vendor's tools go as they came.
func copilotGrokFlat(p provider.Provider, to provider.Protocol, body []byte) []byte {
	if to != provider.Responses || p.Account == nil || p.Account.Agent != "copilot" ||
		!grokModel(gjson.GetBytes(body, "model").String()) {
		return body
	}
	return provider.GrokFlatBody(body)
}
