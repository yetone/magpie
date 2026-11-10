package gateway

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// Codex offers its image tool, the namespace image_gen with its function
// imagegen, to a provider it reads as the OpenAI actor, as magpie's is
// (#870). A Codex backend (a ChatGPT sign-in, sub2api) takes it, and a
// call to it is drawn through magpie (drawing_route.go). A vendor's
// Responses API that knows no namespaces turns the whole request away with
// a bare 400, so a plain chat broke (#949). Such a vendor is asked again
// without the tool, and once that works it isn't sent it again.

// imageNamespace is the namespace Codex's image tool is offered in.
const imageNamespace = "image_gen"

// imageToolRefused is how unfit remembers a provider refusing it.
const imageToolRefused = "tool:" + imageNamespace

// mayDropImageTool reports whether p is one that may not take Codex's
// image tool: anything but a ChatGPT sign-in and OpenAI's own API.
func mayDropImageTool(p provider.Provider) bool {
	return (p.Account == nil || p.Account.Agent != "codex") && !strings.HasSuffix(p.VendorHost(), "openai.com")
}

// withoutImageTool is a Responses body without Codex's image tool among its
// tools, and whether it had it.
func withoutImageTool(body []byte) ([]byte, bool) {
	if !bytes.Contains(body, []byte(`"`+imageNamespace+`"`)) {
		return body, false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil {
		return body, false
	}
	tools, _ := m["tools"].([]any)
	kept := make([]any, 0, len(tools))
	for _, t := range tools {
		if tm, _ := t.(map[string]any); tm != nil && tm["type"] == "namespace" && tm["name"] == imageNamespace {
			continue
		}
		kept = append(kept, t)
	}
	if len(kept) == len(tools) {
		return body, false
	}
	if len(kept) == 0 {
		delete(m, "tools")
		delete(m, "tool_choice")
	} else {
		m["tools"] = kept
	}
	out, err := marshalPlain(m)
	if err != nil {
		return body, false
	}
	return out, true
}

// withoutImageToolReq is a translated request without Codex's image tool,
// which it offers flat (image_gen__imagegen), and whether it had it.
func withoutImageToolReq(req *Request) (*Request, bool) {
	var kept []Tool
	for _, t := range req.Tools {
		if ns, ok := req.Namespaced[t.Name]; ok && ns.Namespace == imageNamespace {
			continue
		}
		kept = append(kept, t)
	}
	if len(kept) == len(req.Tools) {
		return req, false
	}
	r := *req
	r.Tools = kept
	if len(kept) == 0 && r.ToolChoice != "" && r.ToolChoice != "none" {
		r.ToolChoice = ""
	}
	return &r, true
}
