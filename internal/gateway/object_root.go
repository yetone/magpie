package gateway

import (
	"bytes"
	"encoding/json"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
)

// A tool whose parameters are an anyOf, oneOf or allOf at the root, as
// Codex desktop's codex_app automation_update is, is turned away by xAI's
// models wherever they are served ("tool parameter root must be an object
// type", #1271: Grok on a relay's key or xAI's own API, not only the Grok
// subscription, whose requests grokBody folds) and by Anthropic's
// ("input_schema does not support oneOf, allOf, or anyOf at the top
// level", #646) behind a relay's Chat API, and by OpenAI's API and any
// upstream that validates tools as it does ("schema must have type
// 'object' and not have 'oneOf'/'anyOf'/'allOf'/'enum'/'const'/'not' at
// the top level", #1493; for a root with no type, "schema must be a JSON
// Schema of 'type: "object"', got 'type: "None"'"). xAI's own API gets
// such a tool folded to an object root (provider.ObjectRoot) from the
// start; any other upstream that refuses one is asked once more with them
// folded, and so from then on for that model. An Anthropic Messages body is
// folded from the start for every upstream, as one translated to Messages
// is (objectSchema): a client that speaks Messages itself (ZCode, Claude
// Code) had its tools sent as they came, and Claude behind Zed's
// subscription refused every request whose tools had a oneOf at a root.

// rootUnionRefusal is an upstream refusing a tool's parameters for a
// union at their root.
var rootUnionRefusal = lazyre.New(`(?i)tool parameter root must be an object type|does not support oneOf, allOf, or anyOf at the top level|schema must have type .object. and not have .oneOf.|schema must be a JSON Schema of .type: \\?"object\\?".`)

// rootUnionRefused is how unfit remembers model refusing one.
func rootUnionRefused(model string) string { return "root union\x00" + model }

// foldsRoots reports whether p's requests for model go with their tools'
// parameters folded to an object root.
func (s *Server) foldsRoots(p provider.Provider, model string, proto provider.Protocol) bool {
	return proto == provider.Anthropic || p.VendorHost() == "api.x.ai" || !s.fits(p.ID, rootUnionRefused(model), proto)
}

// objectRootsBody is a Chat, Responses or Messages body whose function
// tools' parameters (a Messages tool's input_schema) have an object root,
// and whether any was changed.
func objectRootsBody(proto provider.Protocol, body []byte) ([]byte, bool) {
	if (proto != provider.Chat && proto != provider.Responses && proto != provider.Anthropic) || !bodyHas(body, `Of"`) {
		return body, false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return body, false
	}
	tools, _ := m["tools"].([]any)
	if !foldTools(proto, tools) {
		return body, false
	}
	out, err := marshalPlain(m)
	if err != nil {
		return body, false
	}
	return out, true
}

// foldTools folds the parameters of the function tools among tools, a
// namespace's included, and reports whether it changed any.
func foldTools(proto provider.Protocol, tools []any) bool {
	changed := false
	for _, t := range tools {
		tm, _ := t.(map[string]any)
		if tm == nil {
			continue
		}
		if proto == provider.Responses && tm["type"] == "namespace" {
			nested, _ := tm["tools"].([]any)
			if foldTools(proto, nested) {
				changed = true
			}
			continue
		}
		if proto == provider.Anthropic {
			// a client tool; a server tool (web_search_…) has a type and
			// no input_schema
			if ps, _ := tm["input_schema"].(map[string]any); ps != nil && provider.ObjectRoot(ps) {
				changed = true
			}
			continue
		}
		fn := tm
		if proto == provider.Chat {
			fn, _ = tm["function"].(map[string]any)
		}
		if ps, _ := fn["parameters"].(map[string]any); ps != nil && provider.ObjectRoot(ps) {
			changed = true
		}
	}
	return changed
}

// objectRootsReq is a translated request whose tools' schemas have an
// object root, and whether any was changed.
func objectRootsReq(req *Request) (*Request, bool) {
	var tools []Tool
	for i, t := range req.Tools {
		s := objectSchema(t.Schema)
		if bytes.Equal(s, t.Schema) {
			continue
		}
		if tools == nil {
			tools = append([]Tool(nil), req.Tools...)
		}
		tools[i].Schema = s
	}
	if tools == nil {
		return req, false
	}
	r := *req
	r.Tools = tools
	return &r, true
}
