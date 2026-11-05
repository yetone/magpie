package gateway

import (
	"bytes"
	"encoding/json"

	"github.com/yetone/magpie/internal/provider"
)

// takesOriginalDetail is whether p's Responses API takes an input_image's
// detail "original", Codex's for a screenshot: OpenAI's own API (a ChatGPT
// account's Codex backend, Azure OpenAI), DeepSeek's own, whose Codex
// setup asks for it, and a remote magpie, which decides for its own
// upstream. A relay's Responses API may not: OpenCode Go answers 400
// "invalid image detail" for DeepSeek V4.1 Flash, which it serves through
// another vendor (qiu_eth).
func takesOriginalDetail(p provider.Provider) bool {
	if p.IsAzure() || p.IsRemoteMagpie() {
		return true
	}
	switch provider.HostOf(p.Base(provider.Responses)) {
	case "api.openai.com", "chatgpt.com", "api.deepseek.com":
		return true
	}
	return false
}

// originalAsHigh is a Responses body with each input_image's detail
// "original" sent as "high", the closest an API without it has, as a Chat
// upstream is sent it (chatDetail); the body as it came when it has none.
func originalAsHigh(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"original"`)) {
		return body
	}
	out, err := walkImages(provider.Responses, body, func(im imageAt) (json.RawMessage, error) {
		var block map[string]json.RawMessage
		if decodeJSON(im.Block, &block) != nil {
			return nil, nil
		}
		var d string
		if decodeJSON(block["detail"], &d) != nil || d != "original" {
			return nil, nil
		}
		block["detail"] = json.RawMessage(`"high"`)
		b, err := json.Marshal(block)
		if err != nil {
			return nil, nil
		}
		return b, nil
	})
	if err != nil {
		return body
	}
	return out
}
