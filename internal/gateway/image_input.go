package gateway

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

const imageOmitted = "[Image omitted: this model accepts text only.]"

// errCurrentImage stops a walk at an image the latest turn gives the model.
var errCurrentImage = errors.New("an image in the latest turn")

// textOnlyBody rejects a user image in the latest turn and omits tool images
// and images in older turns. It edits the wire JSON so passthrough keeps fields
// the IR does not represent (such as provider-specific request options).
func textOnlyBody(proto provider.Protocol, body []byte) ([]byte, bool) {
	out, err := walkImages(proto, body, func(im imageAt) (json.RawMessage, error) {
		if im.Current() {
			return nil, errCurrentImage
		}
		return textBlock(proto, imageOmitted), nil
	})
	if err != nil {
		return body, true
	}
	return out, false
}

// hasImage is whether the body has an image in any turn.
func hasImage(proto provider.Protocol, body []byte) bool {
	found := errors.New("found")
	_, err := walkImages(proto, body, func(imageAt) (json.RawMessage, error) { return nil, found })
	return err == found
}

// hasUnportableCurrentImage reports a current Responses image with no URL to
// carry to another protocol. A file_id belongs to the Responses provider that
// stored it; translating it as a URL or dropping it would mislead the model.
func hasUnportableCurrentImage(body []byte) bool {
	missing := errors.New("current image has no portable source")
	_, err := walkImages(provider.Responses, body, func(im imageAt) (json.RawMessage, error) {
		if im.Current() && im.Src == "" {
			return nil, missing
		}
		return nil, nil
	})
	return err == missing
}

// imageAt is an image in a request, where it is.
type imageAt struct {
	// Src is the image as a URL, a data: URL for one sent inline; empty for
	// one only its vendor can open (a Responses file_id, a Gemini file).
	Src string
	// Last is set on one in the latest turn, Tool on one a tool gave back.
	Last, Tool bool
}

// Current is whether the image is one the user gives the model now, which
// the model is asked about.
func (im imageAt) Current() bool { return im.Last && !im.Tool }

// walkImages gives every image in the body's turns to swap, and puts the
// block it returns in the image's place (nil keeps it). An error from swap
// ends the walk. The body is returned as it was when nothing was swapped.
func walkImages(proto provider.Protocol, body []byte, swap func(imageAt) (json.RawMessage, error)) ([]byte, error) {
	var request map[string]json.RawMessage
	if json.Unmarshal(body, &request) != nil {
		return body, nil
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
		return body, nil
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
		content, swapped, err := swapImageBlocks(proto, turn[key], i == len(turns)-1, tool, swap)
		if err != nil {
			return body, err
		}
		if !swapped {
			continue
		}
		turn[key] = content
		turns[i], _ = json.Marshal(turn)
		changed = true
	}
	if !changed {
		return body, nil
	}
	request[turnsKey], _ = json.Marshal(turns)
	return json.Marshal(request)
}

// swapImageBlocks swaps the images among a turn's blocks, and those in an
// Anthropic tool_result's, which are a tool's.
func swapImageBlocks(proto provider.Protocol, raw json.RawMessage, last, tool bool, swap func(imageAt) (json.RawMessage, error)) (json.RawMessage, bool, error) {
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return raw, false, nil
	}
	swapped := false
	for i, rawBlock := range blocks {
		var block map[string]json.RawMessage
		if json.Unmarshal(rawBlock, &block) != nil {
			continue
		}
		var kind string
		json.Unmarshal(block["type"], &kind)
		src, isImage := imageSrc(proto, kind, block)
		if isImage {
			out, err := swap(imageAt{Src: src, Last: last, Tool: tool})
			if err != nil {
				return raw, false, err
			}
			if out != nil {
				blocks[i] = out
				swapped = true
			}
			continue
		}
		if proto == provider.Anthropic && kind == "tool_result" {
			content, nested, err := swapImageBlocks(proto, block["content"], last, true, swap)
			if err != nil {
				return raw, false, err
			}
			if nested {
				block["content"] = content
				blocks[i], _ = json.Marshal(block)
				swapped = true
			}
		}
	}
	if !swapped {
		return raw, false, nil
	}
	out, _ := json.Marshal(blocks)
	return out, true, nil
}

// imageSrc is whether a block is an image, and the image as a URL when it
// can be read from the block.
func imageSrc(proto provider.Protocol, kind string, block map[string]json.RawMessage) (string, bool) {
	switch proto {
	case provider.Chat:
		if kind != "image_url" {
			return "", false
		}
		var url string
		if json.Unmarshal(block["image_url"], &url) != nil {
			var obj struct {
				URL string `json:"url"`
			}
			json.Unmarshal(block["image_url"], &obj)
			url = obj.URL
		}
		return url, true
	case provider.Responses:
		if kind != "input_image" {
			return "", false
		}
		var url string
		json.Unmarshal(block["image_url"], &url)
		return url, true
	case provider.Anthropic:
		if kind != "image" {
			return "", false
		}
		var src struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
			URL       string `json:"url"`
		}
		json.Unmarshal(block["source"], &src)
		switch {
		case src.Type == "base64" && src.Data != "":
			return "data:" + src.MediaType + ";base64," + src.Data, true
		case src.Type == "url":
			return src.URL, true
		}
		return "", true
	case provider.Gemini:
		var data struct {
			MimeType string `json:"mimeType"`
			Data     string `json:"data"`
		}
		json.Unmarshal(block["inlineData"], &data)
		if strings.HasPrefix(data.MimeType, "image/") {
			return "data:" + data.MimeType + ";base64," + data.Data, true
		}
		if len(block["fileData"]) > 0 {
			var file struct {
				MimeType string `json:"mimeType"`
			}
			if json.Unmarshal(block["fileData"], &file) != nil || file.MimeType == "" || strings.HasPrefix(file.MimeType, "image/") {
				return "", true // unknown file types may be images; fail closed
			}
		}
	}
	return "", false
}

// textBlock is a text block of the protocol's.
func textBlock(proto provider.Protocol, text string) json.RawMessage {
	var b []byte
	switch proto {
	case provider.Responses:
		b, _ = json.Marshal(map[string]string{"type": "input_text", "text": text})
	case provider.Gemini:
		b, _ = json.Marshal(map[string]string{"text": text})
	default:
		b, _ = json.Marshal(map[string]string{"type": "text", "text": text})
	}
	return b
}
