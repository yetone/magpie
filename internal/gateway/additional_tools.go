package gateway

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// Codex's Responses Lite, for the models its catalog marks so (the ChatGPT
// backend's gpt-6 and gpt-5.6 among them), sends no tools and no
// instructions: its tools go as the first input item, an additional_tools
// one, and its prompt as a developer message after it. Every plain
// function and freeform tool is grouped in a namespace named "functions",
// which Codex reads as no namespace at all when a call comes back. Only
// OpenAI knows the item — xAI's backend turns the whole request away over
// it (#350: 422 "unknown item type additional_tools") — so a request going
// anywhere else has its tools in the tools, the "functions" namespace
// opened into them, as Codex sends a model that isn't Lite.

// Lite also asks for reasoning context "all_turns" (the ChatGPT backend
// turns a Lite request away without it, #534) and says it is Lite in the
// X-OpenAI-Internal-Codex-Responses-Lite header. Neither is in the
// Responses API anyone else serves. A Codex websocket turn also repeats the
// header in client_metadata. A subagent a GPT lead spawns on another model
// is sent in the lead's Lite shape, its model name alone changed (codex-rs
// builds the child's request from the lead's catalog entry), and OpenCode
// Go's DeepSeek answered it with 503 "Endpoint is unavailable" while the
// same subagent under a DeepSeek lead, not Lite, was answered (TangForX on
// X). So a request leaving Lite has all three taken off too (fromLite,
// liteHeaderKept).

// liteHeader says a Codex request is Responses Lite.
const liteHeader = "X-OpenAI-Internal-Codex-Responses-Lite"

// fromLite is a Responses request Codex sent as Responses Lite in the shape
// it sends a model that isn't Lite: its additional_tools in its tools
// (liftAdditionalTools), no reasoning context, and no Lite marker in
// client_metadata. A request that isn't Lite is returned as it was.
func fromLite(body []byte) []byte {
	return withoutLiteMetadata(withoutReasoningContext(liftAdditionalTools(body)))
}

// withoutLiteMetadata is body without the Lite marker a Codex websocket
// turn repeats in client_metadata, the rest of client_metadata and every
// other member byte for byte as they were. The marker is removed whatever
// its value, as the header is: a request leaving Lite must not still claim
// to be one.
func withoutLiteMetadata(body []byte) []byte {
	const marker = "ws_request_header_x_openai_internal_codex_responses_lite"
	if !bytes.Contains(body, []byte(marker)) {
		return body
	}
	r, found, ok := topMember(body, "client_metadata")
	if !found || !ok || len(r) == 0 || r[0] != '{' {
		return body
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(r, &m) != nil || m == nil {
		return body
	}
	if _, has := m[marker]; !has {
		return body
	}
	delete(m, marker)
	nb, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return withMembers(body, map[string][]byte{"client_metadata": nb})
}

// withoutReasoningContext is body without reasoning.context, the rest of
// reasoning and every other member byte for byte as they were.
func withoutReasoningContext(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"context"`)) {
		return body
	}
	r, found, ok := topMember(body, "reasoning")
	if !found || !ok {
		return body
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(r, &m) != nil || m == nil {
		return body
	}
	if _, has := m["context"]; !has {
		return body
	}
	delete(m, "context")
	nb, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return withMembers(body, map[string][]byte{"reasoning": nb})
}

// liteKept says a Responses request to p goes in Codex's Lite shape as it
// was sent: to OpenAI, to a ChatGPT account, to a relay that takes Codex's
// requests as Codex sent them (Provider.CodexAsSent, #1758), and to another
// magpie's codex model, which that magpie serves through its ChatGPT
// account (codexModel, #4445).
func liteKept(p provider.Provider, to provider.Protocol, codex bool, model string) bool {
	if to != provider.Responses {
		return false
	}
	return p.Account != nil && p.Account.Agent == "codex" ||
		strings.HasSuffix(p.Host(), "openai.com") ||
		p.CodexAsSent && p.Account == nil && codex ||
		p.IsRemoteMagpie() && codexModel(model)
}

// codexModel is a magpie's ChatGPT account model: one named codex/<model>,
// the account's namespace in every magpie's catalog, so a remote magpie
// lists one only when that account is there to serve it. The backend turns
// a request for one of its Lite models away without reasoning context
// "all_turns" (#4445), and what a Lite request carries is that account's to
// pass on, not this magpie's to take off: the shape and header lifted for
// anyone else leave the account a request its backend refuses.
func codexModel(model string) bool {
	name, _, ok := strings.Cut(model, "/")
	return ok && name == "codex"
}

// liteNamespace is the namespace Responses Lite groups Codex's own
// functions in (codex_protocol::DEFAULT_FUNCTION_NAMESPACE).
const liteNamespace = "functions"

// liftAdditionalTools is a Responses request with its additional_tools items
// moved into its tools, or the request as it was when it has none.
//
// Only the additional_tools items and the tools are decoded; every other
// item, and every other member of the request, goes byte for byte as it
// came. Decoding the whole conversation into maps to move one item cost
// ~7.5 times the body on every read of a Lite request, and each Codex
// request was read so twice (its turn and its folder): ~110 MB of a
// 4 MB conversation, held at once by each request in flight, so a few
// subagents at once took the app toward 2 GB (XiaoYin_OK on X).
func liftAdditionalTools(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"additional_tools"`)) {
		return body
	}
	in, found, ok := topMember(body, "input")
	if !ok || !found || len(in) == 0 || in[0] != '[' {
		return body
	}
	var tools []any
	if raw, has, _ := topMember(body, "tools"); has {
		if t, ok := decodeAny(raw).([]any); ok {
			tools = t
		}
	}
	have := toolsByName(tools)
	var kept [][]byte
	lifted, bad := false, false
	eachElement(in, func(it []byte) bool {
		if ty, ok := stringBytes(memberOrNil(it, "type")); !ok || string(ty) != "additional_tools" {
			// a call the model made to one of Codex's own tools, under the
			// namespace they were grouped in, is the plain call it now is
			if ns, ok := stringBytes(memberOrNil(it, "namespace")); ok && string(ns) == liteNamespace {
				it = withoutTopMember(it, "namespace")
			}
			kept = append(kept, it)
			return true
		}
		im, _ := decodeAny(it).(map[string]any)
		if im == nil {
			bad = true
			return false
		}
		lifted = true
		more, _ := im["tools"].([]any)
		for _, t := range more {
			tm, _ := t.(map[string]any)
			if tm == nil {
				continue
			}
			if tm["type"] == "namespace" && tm["name"] == liteNamespace {
				nested, _ := tm["tools"].([]any)
				for _, n := range nested {
					if nm, _ := n.(map[string]any); nm != nil {
						tools = addTool(tools, have, nm)
					}
				}
				continue
			}
			tools = addTool(tools, have, tm)
		}
		return true
	})
	if !lifted || bad {
		return body
	}
	var toolsJSON []byte
	if len(tools) > 0 {
		t, err := marshalPlain(tools)
		if err != nil {
			return body
		}
		toolsJSON = t
	}
	// written once, at its size: the input with the kept items, the tools
	// given again, every other byte of the body as it was
	raw := trimJSON(body)
	out := make([]byte, 0, len(raw)+len(toolsJSON)+len(`,"tools":`))
	last, hadTools := 0, false
	eachMemberAt(raw, func(k0, k1, v0, v1 int) bool {
		switch key := raw[k0:k1]; {
		case keyIs(key, "input"):
			out = append(append(out, raw[last:v0]...), '[')
			for i, it := range kept {
				if i > 0 {
					out = append(out, ',')
				}
				out = append(out, it...)
			}
			out = append(out, ']')
			last = v1
		case keyIs(key, "tools"):
			hadTools = true
			if toolsJSON != nil {
				out = append(append(out, raw[last:v0]...), toolsJSON...)
				last = v1
			}
		}
		return true
	})
	out = append(out, raw[last:len(raw)-1]...)
	if toolsJSON != nil && !hadTools {
		out = append(append(out, `,"tools":`...), toolsJSON...)
	}
	return append(out, '}')
}

// decodeAny is raw, a valid JSON value, decoded as encoding/json decodes
// into an any, its numbers as they were written; nil when it doesn't
// decode.
func decodeAny(raw []byte) any {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil
	}
	return v
}

// toolsByName is a request's tools by their names, a hosted tool without
// one (web_search) by its type.
func toolsByName(tools []any) map[string]map[string]any {
	have := map[string]map[string]any{}
	for _, t := range tools {
		if tm, _ := t.(map[string]any); tm != nil {
			have[toolKey(tm)] = tm
		}
	}
	return have
}

func toolKey(tm map[string]any) string {
	if n, _ := tm["name"].(string); n != "" {
		return n
	}
	ty, _ := tm["type"].(string)
	return "#" + ty
}

// addTool is tools with tm among them: a tool added once, and a namespace
// given again adding to the one there the tools it didn't have.
func addTool(tools []any, have map[string]map[string]any, tm map[string]any) []any {
	k := toolKey(tm)
	old := have[k]
	if old == nil {
		have[k] = tm
		return append(tools, tm)
	}
	if old["type"] != "namespace" || tm["type"] != "namespace" {
		return tools
	}
	nested, _ := old["tools"].([]any)
	more, _ := tm["tools"].([]any)
	for _, nt := range more {
		nm, _ := nt.(map[string]any)
		if nm != nil && !containsNamed(nested, nm["name"]) {
			nested = append(nested, nm)
		}
	}
	old["tools"] = nested
	return tools
}

func containsNamed(tools []any, name any) bool {
	for _, o := range tools {
		if om, _ := o.(map[string]any); om["name"] == name {
			return true
		}
	}
	return false
}
