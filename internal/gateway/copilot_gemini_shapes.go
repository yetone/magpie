package gateway

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// Copilot's Gemini answers 400 to tool schemas of six shapes that other
// vendors take (MCP servers write all of them; ProgCZ and tmhyt1 on X, on
// v0.1.3853): an object with no properties, an anyOf whose branches share
// one type, an anyOf with an untyped branch, an enum on an array, a union
// at the root, and a tuple array (prefixItems). Each is written as the
// nearest schema Gemini takes, keeping what the model can act on:
//
//   - object with no properties: properties {} added;
//   - anyOf of one type (null apart): that one schema, branches merged
//     (object properties joined, string enums joined), nullable kept as an
//     anyOf with null;
//   - anyOf with a branch whose type can't be told: the union goes, the
//     description stays, and the node is typed like any untyped schema
//     (geminiTypes, which runs after);
//   - enum on an array: moved to items when those are strings, else dropped;
//   - union at the root: an object holding the properties of its object
//     branches, none of them required;
//   - prefixItems (or items as a list): items, the one schema the positions
//     share, or an anyOf of them.
//
// Disjoint object unions (Codex's create_thread target) fold the same way,
// since Copilot's Gemini takes a nested oneOf with 200 but drops its
// branches, and the model answers with a string. A nested oneOf a client
// wrote is left (#4374). A schema with
// none of the six goes byte for byte as it came, and only Copilot's Gemini
// on chat is touched.
func copilotGeminiToolShapes(p provider.Provider, to provider.Protocol, body []byte) []byte {
	if to != provider.Chat || !copilotGemini(p, body) {
		return body
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(tools.Raw)))
	dec.UseNumber()
	var list []any
	if dec.Decode(&list) != nil {
		return body
	}
	changed := false
	for _, tool := range list {
		m, _ := tool.(map[string]any)
		if fn, ok := m["function"].(map[string]any); ok {
			m = fn
		}
		for _, k := range []string{"parameters", "input_schema"} {
			if root, ok := m[k].(map[string]any); ok {
				changed = shapeForGemini(root, true, root) || changed
			}
		}
	}
	if !changed {
		return body
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return body
	}
	out := make([]byte, 0, len(body))
	out = append(out, body[:tools.Index]...)
	out = append(out, raw...)
	return append(out, body[tools.Index+len(tools.Raw):]...)
}

// shapeForGemini rewrites schema s in place, below first, and says whether
// it changed anything.
func shapeForGemini(s map[string]any, root bool, top map[string]any) bool {
	changed := false
	// children first, so a union is judged on branches already fit
	for _, k := range []string{"properties", "patternProperties", "$defs", "definitions"} {
		if m, ok := s[k].(map[string]any); ok {
			for _, v := range m {
				if c, ok := v.(map[string]any); ok {
					changed = shapeForGemini(c, false, top) || changed
				}
			}
		}
	}
	for _, k := range []string{"items", "additionalProperties", "not", "contains"} {
		if c, ok := s[k].(map[string]any); ok {
			changed = shapeForGemini(c, false, top) || changed
		}
	}
	for _, k := range []string{"anyOf", "oneOf", "allOf", "prefixItems", "items"} {
		if l, ok := s[k].([]any); ok {
			for _, v := range l {
				if c, ok := v.(map[string]any); ok {
					changed = shapeForGemini(c, false, top) || changed
				}
			}
		}
	}

	// a tuple: one items schema
	tuple, hasPrefix := s["prefixItems"].([]any)
	if l, ok := s["items"].([]any); ok {
		tuple, hasPrefix = l, true
	}
	if _, has := s["prefixItems"]; has || hasPrefix {
		delete(s, "prefixItems")
		if _, isSchema := s["items"].(map[string]any); !isSchema {
			s["items"] = tupleItems(tuple)
			if m, ok := s["items"].(map[string]any); ok {
				shapeForGemini(m, false, top)
			}
		}
		if s["type"] == nil {
			s["type"] = "array"
		}
		changed = true
	}

	// an array with no items: Vertex behind Copilot turns it away, and the
	// untyped items are typed after (geminiTypes)
	if s["type"] == "array" && s["items"] == nil {
		s["items"] = map[string]any{}
		changed = true
	}

	// an enum on an array belongs to its items
	if _, has := s["enum"]; has && s["type"] == "array" {
		if es, ok := s["enum"].([]any); ok && allStrings(es) {
			items, _ := s["items"].(map[string]any)
			if items == nil && s["items"] == nil {
				items = map[string]any{"type": "string"}
				s["items"] = items
			}
			if items != nil && items["enum"] == nil && (items["type"] == nil || items["type"] == "string") {
				items["enum"] = es
				if items["type"] == nil {
					items["type"] = "string"
				}
			}
		}
		delete(s, "enum")
		changed = true
	}

	// a union
	if alts, ok := s["oneOf"].([]any); ok && root {
		flattenRootUnion(s, "oneOf", alts)
		changed = true
	} else if alts, ok := s["anyOf"].([]any); ok {
		if root && s["type"] == nil {
			flattenRootUnion(s, "anyOf", alts)
			changed = true
		} else {
			changed = mergeUnion(s, "anyOf", alts, top) || changed
		}
	}

	if s["type"] == "object" {
		if _, has := s["properties"]; !has {
			s["properties"] = map[string]any{}
			changed = true
		}
	}
	return changed
}

func allStrings(es []any) bool {
	for _, e := range es {
		if _, ok := e.(string); !ok {
			return false
		}
	}
	return true
}

// tupleItems is the items schema a tuple's positions share.
func tupleItems(prefix []any) any {
	var uniq []any
	for _, p := range prefix {
		m, ok := p.(map[string]any)
		if !ok {
			continue
		}
		dup := false
		for _, u := range uniq {
			dup = dup || reflect.DeepEqual(u, m)
		}
		if !dup {
			uniq = append(uniq, m)
		}
	}
	switch len(uniq) {
	case 0:
		return map[string]any{}
	case 1:
		return uniq[0]
	}
	return map[string]any{"anyOf": uniq}
}

// flattenRootUnion turns a root union into an object holding every object
// branch's properties; no branch's required list survives, since another
// branch may be the one used.
func flattenRootUnion(s map[string]any, key string, alts []any) {
	props := map[string]any{}
	for _, a := range alts {
		m, _ := a.(map[string]any)
		if ps, ok := m["properties"].(map[string]any); ok {
			for n, v := range ps {
				if _, has := props[n]; !has {
					props[n] = v
				}
			}
		}
	}
	if own, ok := s["properties"].(map[string]any); ok {
		for n, v := range own {
			props[n] = v
		}
	}
	delete(s, key)
	delete(s, "required")
	s["type"] = "object"
	s["properties"] = props
}

// mergeUnion folds an anyOf whose branches share one type (null apart) into
// one schema, and drops one with a branch that has no type. It says whether
// it changed s.
func mergeUnion(s map[string]any, key string, alts []any, top map[string]any) bool {
	var typed []map[string]any
	nullable := false
	typ := ""
	for _, a := range alts {
		m, _ := a.(map[string]any)
		if m == nil {
			delete(s, key)
			return true
		}
		if ref, ok := m["$ref"].(string); ok {
			// a branch that is a local reference is the schema it names, so
			// the union is judged on what the model will fill in
			if r := derefLocal(top, ref); r != nil {
				shapeForGemini(r, false, top)
				m = r
			}
		}
		t, _ := m["type"].(string)
		if t == "null" {
			nullable = true
			continue
		}
		if t == "" {
			t = guessType(m)
		}
		if t == "" {
			// nothing says what this branch is: the node goes untyped, to be
			// given its type like any other
			delete(s, key)
			return true
		}
		m["type"] = t
		if typ != "" && typ != t {
			return false
		}
		typ = t
		typed = append(typed, m)
	}
	if len(typed) == 0 || len(typed) == 1 && nullable {
		// a nullable single schema is what Copilot's Gemini takes, but not
		// with a type written beside the union (400 invalid request body)
		if _, has := s["type"]; has && len(typed) == 1 {
			delete(s, "type")
			return true
		}
		return false
	}
	merged := map[string]any{}
	for _, m := range typed {
		for k, v := range m {
			switch k {
			case "properties":
				props, _ := merged[k].(map[string]any)
				if props == nil {
					props = map[string]any{}
				}
				pm, _ := v.(map[string]any)
				for n, pv := range pm {
					if have, has := props[n]; !has {
						props[n] = pv
					} else {
						props[n] = joinEnums(have, pv)
					}
				}
				merged[k] = props
			case "enum":
				es, _ := merged[k].([]any)
				for _, e := range asList(v) {
					dup := false
					for _, x := range es {
						dup = dup || reflect.DeepEqual(x, e)
					}
					if !dup {
						es = append(es, e)
					}
				}
				merged[k] = es
			case "required":
				// only what every branch requires, below
			default:
				if _, has := merged[k]; !has {
					merged[k] = v
				}
			}
		}
	}
	for _, m := range typed {
		if _, has := m["enum"]; !has {
			delete(merged, "enum") // a branch without an enum takes any value
		}
	}
	if req := commonRequired(typed); len(req) > 0 {
		merged["required"] = req
	}
	delete(s, key)
	for k, v := range merged {
		if _, has := s[k]; !has {
			s[k] = v
		}
	}
	if nullable {
		inner := map[string]any{}
		for _, k := range []string{"type", "properties", "required", "items", "enum"} {
			if v, ok := s[k]; ok {
				inner[k] = v
				delete(s, k)
			}
		}
		s[key] = []any{inner, map[string]any{"type": "null"}}
	}
	return true
}

func commonRequired(typed []map[string]any) []any {
	count := map[string]int{}
	for _, m := range typed {
		seen := map[string]bool{}
		for _, r := range asList(m["required"]) {
			if n, ok := r.(string); ok && !seen[n] {
				seen[n] = true
				count[n]++
			}
		}
	}
	var names []string
	for n, c := range count {
		if c == len(typed) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	out := make([]any, len(names))
	for i, n := range names {
		out[i] = n
	}
	return out
}

func asList(v any) []any { l, _ := v.([]any); return l }

// guessType is the type a branch without one says by its keywords.
func guessType(m map[string]any) string {
	switch {
	case m["properties"] != nil:
		return "object"
	case m["items"] != nil:
		return "array"
	}
	if es, ok := m["enum"].([]any); ok && len(es) > 0 && allStrings(es) {
		return "string"
	}
	if _, ok := m["const"].(string); ok {
		return "string"
	}
	return ""
}

// derefLocal is a copy of the schema a local reference ("#/$defs/X" or
// "#/definitions/X") names in top, nil when it is anything else or the
// copy refers on (a cycle is never followed).
func derefLocal(top map[string]any, ref string) map[string]any {
	var defs any
	switch {
	case strings.HasPrefix(ref, "#/$defs/"):
		defs, ref = top["$defs"], strings.TrimPrefix(ref, "#/$defs/")
	case strings.HasPrefix(ref, "#/definitions/"):
		defs, ref = top["definitions"], strings.TrimPrefix(ref, "#/definitions/")
	default:
		return nil
	}
	m, _ := defs.(map[string]any)
	target, _ := m[ref].(map[string]any)
	if target == nil {
		return nil
	}
	raw, err := json.Marshal(target)
	if err != nil || bodyHas(raw, `"$ref"`) {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out map[string]any
	if dec.Decode(&out) != nil {
		return nil
	}
	return out
}

// joinEnums is the property two branches both name: when both are string
// enums, one with the values of each (a discriminator's "project" and
// "none"); a branch without an enum takes any value, so the enum goes.
func joinEnums(a, b any) any {
	am, _ := a.(map[string]any)
	bm, _ := b.(map[string]any)
	if am == nil || bm == nil || am["type"] != bm["type"] {
		return a
	}
	ae, bem := asList(am["enum"]), asList(bm["enum"])
	_, aHas := am["enum"]
	_, bHas := bm["enum"]
	if aHas != bHas {
		out := map[string]any{}
		for k, v := range am {
			if k != "enum" {
				out[k] = v
			}
		}
		return out
	}
	if !aHas {
		return a
	}
	out := map[string]any{}
	for k, v := range am {
		out[k] = v
	}
	joined := append([]any{}, ae...)
	for _, e := range bem {
		dup := false
		for _, x := range joined {
			dup = dup || reflect.DeepEqual(x, e)
		}
		if !dup {
			joined = append(joined, e)
		}
	}
	out["enum"] = joined
	return out
}
