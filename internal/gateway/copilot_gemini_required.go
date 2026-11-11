package gateway

import (
	"bytes"
	"encoding/json"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// Copilot's Gemini turns a request away 400 "invalid request body" for a
// tool schema that names a required field its properties lack (an MCP
// server's schema often does: Gemini's own API checks it, the other
// vendors Copilot serves don't), and for two tools of one name. Every turn
// that carried such a tool failed whole, whatever the client or the model
// (yuushabu_buyinn on X; probed live: the same request with the required
// entry gone, or the second tool gone, came back 200). A required entry
// that names no property says nothing a model can act on, so it is dropped
// and the first of two same-named tools is kept; a schema without either
// is not touched, byte for byte.
func copilotGeminiToolsFit(p provider.Provider, to provider.Protocol, body []byte) []byte {
	if to != provider.Chat || p.Account == nil || p.Account.Agent != "copilot" ||
		!geminiNamed(gjson.GetBytes(body, "model").String()) {
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
	seen := map[string]bool{}
	kept := list[:0:0]
	for _, tool := range list {
		m, _ := tool.(map[string]any)
		name := ""
		if fn, ok := m["function"].(map[string]any); ok {
			name, _ = fn["name"].(string)
		}
		if name != "" {
			if seen[name] {
				changed = true
				continue
			}
			seen[name] = true
		}
		if pruneRequired(tool) {
			changed = true
		}
		kept = append(kept, tool)
	}
	if !changed {
		return body
	}
	raw, err := json.Marshal(kept)
	if err != nil {
		return body
	}
	out := make([]byte, 0, len(body))
	out = append(out, body[:tools.Index]...)
	out = append(out, raw...)
	return append(out, body[tools.Index+len(tools.Raw):]...)
}

// pruneRequired drops, under node, every required entry that names no
// property of its object, and says whether it dropped one.
func pruneRequired(node any) bool {
	changed := false
	switch v := node.(type) {
	case []any:
		for _, e := range v {
			changed = pruneRequired(e) || changed
		}
	case map[string]any:
		if req, ok := v["required"].([]any); ok {
			props, _ := v["properties"].(map[string]any)
			keep := make([]any, 0, len(req))
			for _, r := range req {
				if s, ok := r.(string); ok {
					if _, has := props[s]; !has {
						continue
					}
				}
				keep = append(keep, r)
			}
			if len(keep) != len(req) {
				changed = true
				if len(keep) == 0 {
					delete(v, "required")
				} else {
					v["required"] = keep
				}
			}
		}
		for _, e := range v {
			changed = pruneRequired(e) || changed
		}
	}
	return changed
}
