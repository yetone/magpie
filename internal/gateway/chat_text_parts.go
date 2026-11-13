package gateway

import (
	"strings"

	"github.com/tidwall/gjson"
)

// textPartsAsString turns a Chat request's message content that is a list
// of plain text parts ([{"type":"text","text":"…"}]) into the string the
// parts say, for a request relayed as the client sent it. OpenAI's Chat
// API takes either for the same text, but a vendor's own OpenAI-compatible
// server may take a string alone: Shanghai AI Lab's Intern discovery API
// (a custom provider, Atria-Dawn, tim072844 on Discord) answered DeepSeek
// Harness, whose pi-ai sends every user message as such a list, with a
// validation error per message ("Input should be a valid string", 238 of
// them), and every turn failed. magpie's own Chat requests, built from
// another API, already send text as a string (buildChat).
//
// Only a list of text parts with nothing else in them is joined, with a
// newline between parts, as vLLM's chat template renders such a list: a
// part with an image, a file, a thinking part or a cache_control mark
// (OpenRouter's Claude cache points) keeps the whole list as it is. Every
// other byte of the body stays as sent, and a body with no such list
// comes back as it was.
func textPartsAsString(body []byte) []byte {
	if !bodyHasBytes(body, contentParts) && !bodyHasBytes(body, contentPartsSpace) {
		return body
	}
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return body
	}
	type edit struct {
		at, n int
		s     []byte
	}
	var edits []edit
	msgs.ForEach(func(_, m gjson.Result) bool {
		if !m.IsObject() {
			return true
		}
		c := m.Get("content")
		if !c.IsArray() || c.Index <= 0 || c.Index+len(c.Raw) > len(body) || string(body[c.Index:c.Index+len(c.Raw)]) != c.Raw {
			return true
		}
		text, ok := plainTextParts(c)
		if !ok {
			return true
		}
		s, err := marshalPlain(text)
		if err != nil {
			return true
		}
		edits = append(edits, edit{c.Index, len(c.Raw), s})
		return true
	})
	if len(edits) == 0 {
		return body
	}
	out := make([]byte, 0, len(body))
	last := 0
	for _, e := range edits {
		out = append(out, body[last:e.at]...)
		out = append(out, e.s...)
		last = e.at + e.n
	}
	return append(out, body[last:]...)
}

// plainTextParts is the text of a content list whose every part is
// {"type":"text","text":"…"} and nothing more, its parts joined by a
// newline; ok is false for an empty list or any other part.
func plainTextParts(c gjson.Result) (string, bool) {
	var texts []string
	ok := true
	c.ForEach(func(_, p gjson.Result) bool {
		if !p.IsObject() {
			ok = false
			return false
		}
		isText, hasText := false, false
		p.ForEach(func(k, v gjson.Result) bool {
			switch k.Str {
			case "type":
				isText = v.Type == gjson.String && v.Str == "text"
			case "text":
				hasText = v.Type == gjson.String
				if hasText {
					texts = append(texts, v.Str)
				}
			default:
				isText = false
				return false
			}
			return isText || k.Str == "text"
		})
		ok = isText && hasText
		return ok
	})
	if !ok || len(texts) == 0 {
		return "", false
	}
	return strings.Join(texts, "\n"), true
}
