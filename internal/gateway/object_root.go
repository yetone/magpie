package gateway

import (
	"bytes"
	"encoding/json"
	"regexp"

	"github.com/yetone/magpie/internal/provider"
)

// A tool whose parameters are an anyOf, oneOf or allOf at the root, as
// Codex desktop's codex_app automation_update is, is turned away by xAI's
// models wherever they are served ("tool parameter root must be an object
// type", #1271: Grok on a relay's key or xAI's own API, not only the Grok
// subscription, whose requests grokBody folds) and by Anthropic's
// ("input_schema does not support oneOf, allOf, or anyOf at the top
// level", #646) behind a relay's Chat API. xAI's own API gets such a tool
// folded to an object root (provider.ObjectRoot) from the start; any other
// upstream that refuses one is asked once more with them folded, and so
// from then on for that model.

// rootUnionRefusal is an upstream refusing a tool's parameters for a
// union at their root.
var rootUnionRefusal = regexp.MustCompile(`(?i)tool parameter root must be an object type|does not support oneOf, allOf, or anyOf at the top level`)

// rootUnionRefused is how unfit remembers model refusing one.
func rootUnionRefused(model string) string { return "root union\x00" + model }

// foldsRoots reports whether p's requests for model go with their tools'
// parameters folded to an object root.
func (s *Server) foldsRoots(p provider.Provider, model string, proto provider.Protocol) bool {
	return p.Host() == "api.x.ai" || !s.fits(p.ID, rootUnionRefused(model), proto)
}

// objectRootsBody is a Chat or Responses body whose function tools'
// parameters have an object root, and whether any was changed.
func objectRootsBody(proto provider.Protocol, body []byte) ([]byte, bool) {
	if (proto != provider.Chat && proto != provider.Responses) || !bytes.Contains(body, []byte(`Of"`)) {
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
