package gateway

import (
	"bytes"
	"encoding/json"
)

// mistralMaxImages is the most images Mistral takes in one request: past
// it the API answers 400 "Total number of images exceeds the maximum
// allowed of 20." (regain4125 on X, Codex after 20+ screenshots). Mistral's
// docs say only that the count depends on the model, so the number is the
// one its error names.
const mistralMaxImages = 20

// mistralImagesLeft is the text that stands where an image was dropped.
const mistralImagesLeft = "[An earlier image is left out of this request.]"

// mistralImageLimit keeps a Chat request bound for Mistral within its image
// limit: the oldest images past the last mistralMaxImages are replaced by a
// short text part, in place, so the turns around them stay. A request
// within the limit comes back byte for byte.
func mistralImageLimit(body []byte) []byte {
	if bytes.Count(body, []byte(`"image_url"`)) <= mistralMaxImages {
		return body
	}
	var q map[string]json.RawMessage
	if decodeJSON(body, &q) != nil {
		return body
	}
	var msgs []json.RawMessage
	if decodeJSON(q["messages"], &msgs) != nil {
		return body
	}
	type slot struct{ msg, part int }
	var images []slot
	parsed := make([]map[string]json.RawMessage, len(msgs))
	lists := make([][]json.RawMessage, len(msgs))
	for i, raw := range msgs {
		var m map[string]json.RawMessage
		if decodeJSON(raw, &m) != nil {
			continue
		}
		c := bytes.TrimSpace(m["content"])
		var list []json.RawMessage
		if len(c) == 0 || c[0] != '[' || decodeJSON(c, &list) != nil {
			continue
		}
		parsed[i], lists[i] = m, list
		for j, part := range list {
			var t struct {
				Type string `json:"type"`
			}
			if decodeJSON(part, &t) == nil && t.Type == "image_url" {
				images = append(images, slot{i, j})
			}
		}
	}
	if len(images) <= mistralMaxImages {
		return body
	}
	text, _ := marshalPlain(map[string]string{"type": "text", "text": mistralImagesLeft})
	touched := map[int]bool{}
	for _, s := range images[:len(images)-mistralMaxImages] {
		lists[s.msg][s.part] = text
		touched[s.msg] = true
	}
	for i := range touched {
		parsed[i]["content"], _ = marshalPlain(lists[i])
		nb, err := marshalPlain(parsed[i])
		if err != nil {
			return body
		}
		msgs[i] = nb
	}
	q["messages"], _ = marshalPlain(msgs)
	nb, err := marshalPlain(q)
	if err != nil {
		return body
	}
	return nb
}
