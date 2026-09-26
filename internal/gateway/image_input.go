package gateway

import (
	"encoding/json"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// textOnlyBody rejects a user image in the latest turn and omits tool images
// and images in older turns. It edits the wire JSON so passthrough keeps fields
// the IR does not represent (such as provider-specific request options).
func textOnlyBody(proto provider.Protocol, body []byte) ([]byte, bool) {
	var request map[string]json.RawMessage
	if json.Unmarshal(body, &request) != nil {
		return body, false
	}
	turnsKey, contentKey := "messages", "content"
	switch proto {
	case provider.Responses:
		turnsKey = "input"
	case provider.Gemini:
		turnsKey, contentKey = "contents", "parts"
	}
	var turns []json.RawMessage
	if json.Unmarshal(request[turnsKey], &turns) != nil {
		return body, false
	}
	changed := false
	for i, raw := range turns {
		var turn map[string]json.RawMessage
		if json.Unmarshal(raw, &turn) != nil {
			continue
		}
		key := contentKey
		tool := false
		var kind string
		if proto == provider.Responses {
			json.Unmarshal(turn["type"], &kind)
			if kind == "function_call_output" {
				key, tool = "output", true
			}
		}
		if proto == provider.Chat {
			json.Unmarshal(turn["role"], &kind)
			tool = kind == "tool"
		}
		content, found, directImage := omitImageBlocks(proto, turn[key])
		if !found {
			continue
		}
		if i == len(turns)-1 && directImage && !tool {
			return body, true
		}
		turn[key] = content
		turns[i], _ = json.Marshal(turn)
		changed = true
	}
	if !changed {
		return body, false
	}
	request[turnsKey], _ = json.Marshal(turns)
	body, _ = json.Marshal(request)
	return body, false
}

// directImage distinguishes a pasted image from an image nested in a tool_result.
func omitImageBlocks(proto provider.Protocol, raw json.RawMessage) (json.RawMessage, bool, bool) {
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return raw, false, false
	}
	found := false
	directImage := false
	for i, rawBlock := range blocks {
		var block map[string]json.RawMessage
		if json.Unmarshal(rawBlock, &block) != nil {
			continue
		}
		var kind string
		json.Unmarshal(block["type"], &kind)
		isImage := false
		switch proto {
		case provider.Chat:
			isImage = kind == "image_url"
		case provider.Responses:
			isImage = kind == "input_image"
		case provider.Anthropic:
			isImage = kind == "image"
		case provider.Gemini:
			var data struct {
				MimeType string `json:"mimeType"`
			}
			json.Unmarshal(block["inlineData"], &data)
			isImage = strings.HasPrefix(data.MimeType, "image/")
			if len(block["fileData"]) > 0 {
				var file struct {
					MimeType string `json:"mimeType"`
				}
				if json.Unmarshal(block["fileData"], &file) != nil || file.MimeType == "" || strings.HasPrefix(file.MimeType, "image/") {
					isImage = true // unknown file types may be images; fail closed
				}
			}
		}
		if isImage {
			blocks[i] = imagePlaceholder(proto)
			found = true
			directImage = true
			continue
		}
		if proto == provider.Anthropic && kind == "tool_result" {
			if content, nested, _ := omitImageBlocks(proto, block["content"]); nested {
				block["content"] = content
				blocks[i], _ = json.Marshal(block)
				found = true
			}
		}
	}
	if !found {
		return raw, false, false
	}
	out, _ := json.Marshal(blocks)
	return out, true, directImage
}

func imagePlaceholder(proto provider.Protocol) json.RawMessage {
	switch proto {
	case provider.Responses:
		return json.RawMessage(`{"type":"input_text","text":"[Image omitted: this model accepts text only.]"}`)
	case provider.Gemini:
		return json.RawMessage(`{"text":"[Image omitted: this model accepts text only.]"}`)
	}
	return json.RawMessage(`{"type":"text","text":"[Image omitted: this model accepts text only.]"}`)
}
