package gateway

import (
	"encoding/json"

	"github.com/yetone/magpie/internal/lazyre"
)

// idlessReasoningRefusal is an upstream's Responses API turning a request
// away over a reasoning input item it reads as neither a message nor a
// tool's result: a DeepSeek channel's 422 "Input tag 'reasoning' found
// using 'type' does not match any of the expected tags: 'message',
// 'tool_result'" (yyhhyyyyyy on X), pydantic's words for a tagged union
// whose tag it has no member for. It takes a reasoning item with an id
// (one it gave, which Codex sends back), and refuses one without: the
// " " a step leads with when it has none (noReasoningItem, reasonFirst),
// or a translated turn's reasoning text (buildResponses), neither of which
// has an id.
var idlessReasoningRefusal = lazyre.New(`(?i)input tag \\?['"]?reasoning\\?['"]? found using \\?['"]?type\\?['"]? does not match any of the expected tags`)

// idlessReasoningRefused is how unfit remembers a provider turning away,
// for model, a reasoning item without an id (withoutIDlessReasoning).
func idlessReasoningRefused(model string) string { return "idless reasoning\x00" + model }

// withoutIDlessReasoning is a Responses body without its reasoning input
// items that carry no id, for an upstream that refused one
// (idlessReasoningRefusal). A body with none comes back as it is.
func withoutIDlessReasoning(body []byte) []byte {
	if !bodyHas(body, `"reasoning"`) {
		return body
	}
	var q map[string]json.RawMessage
	if decodeJSONView(body, &q) != nil {
		return body
	}
	items, isList := inputList(q["input"])
	if !isList {
		return body
	}
	kept := make([]json.RawMessage, 0, len(items))
	for _, raw := range items {
		var it struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if decodeJSONView(raw, &it) == nil && it.Type == "reasoning" && it.ID == "" {
			continue
		}
		kept = append(kept, raw)
	}
	if len(kept) == len(items) {
		return body
	}
	q["input"], _ = marshalJSON(kept)
	nb, err := marshalJSON(q)
	if err != nil {
		return body
	}
	return nb
}
