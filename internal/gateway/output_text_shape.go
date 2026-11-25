package gateway

import (
	"github.com/tidwall/gjson"
)

// An output_text part always carries its annotations list, empty or not, as
// OpenAI's own replies do. The AI SDK (@ai-sdk/openai's
// openaiResponsesResponseSchema) requires it on every output_text part of a
// reply asked for whole, and refuses the reply without it as "Invalid JSON
// response"; its stream schema reads no part, so a stream of the same reply
// passed. An upstream relayed as it came (a plugin's Responses API, whose
// stream magpie gathers for a client that asked for one body, plugins#112)
// can leave it out, while what magpie writes itself has it (Winnie7572 on
// X: auto/deepseek-v4.1-flash and glm-5.3-flash through Open Design). A
// relayed message's parts get an empty list where they have none
// (phaseTidy, which goes through every relayed Responses reply's items).

// bareTextPart reports whether a content part, read in place, is an
// output_text with no annotations list.
func bareTextPart(p gjson.Result) bool {
	return p.Get("type").Str == "output_text" && !p.Get("annotations").IsArray()
}

// bareTextItem reports whether an output item, read in place, is a message
// with a part bareTextPart finds.
func bareTextItem(it gjson.Result) bool {
	if it.Get("type").Str != "message" {
		return false
	}
	found := false
	it.Get("content").ForEach(func(_, p gjson.Result) bool {
		found = bareTextPart(p)
		return !found
	})
	return found
}

// anyBareText reports whether a reply's output has an item bareTextItem
// finds.
func anyBareText(output gjson.Result) bool {
	found := false
	output.ForEach(func(_, it gjson.Result) bool {
		found = bareTextItem(it)
		return !found
	})
	return found
}

// annotatePart gives p, an output_text part, an empty annotations list
// where it has none, and reports whether it did.
func annotatePart(p map[string]any) bool {
	if p == nil || p["type"] != "output_text" {
		return false
	}
	if _, ok := p["annotations"].([]any); ok {
		return false
	}
	p["annotations"] = []any{}
	return true
}

// annotateItem gives a message item's output_text parts their annotations
// list, and reports whether it gave any.
func annotateItem(it map[string]any) bool {
	if it == nil || it["type"] != "message" {
		return false
	}
	parts, _ := it["content"].([]any)
	changed := false
	for _, p := range parts {
		if pm, _ := p.(map[string]any); annotatePart(pm) {
			changed = true
		}
	}
	return changed
}

// annotateOutput is annotateItem over a reply's output.
func annotateOutput(output []any) bool {
	changed := false
	for _, it := range output {
		if m, _ := it.(map[string]any); annotateItem(m) {
			changed = true
		}
	}
	return changed
}

// annotatedEvent is a Responses stream event's data with the part or item
// it carries given its annotations, and whether it needed them: the
// events that carry a whole part or item, which a client may keep as the
// reply's. response.completed's output is gone through where its phases
// are (phaseTidy.ended).
func annotatedEvent(typ string, data []byte) ([]byte, bool) {
	var bare bool
	switch typ {
	case "response.output_item.added", "response.output_item.done":
		bare = bareTextItem(gjson.GetBytes(data, "item"))
	case "response.content_part.added", "response.content_part.done":
		bare = bareTextPart(gjson.GetBytes(data, "part"))
	}
	if !bare {
		return nil, false
	}
	var e map[string]any
	if decodeNumbers(data, &e) != nil {
		return nil, false
	}
	it, _ := e["item"].(map[string]any)
	part, _ := e["part"].(map[string]any)
	if !annotateItem(it) && !annotatePart(part) {
		return nil, false
	}
	nb, err := marshalPlain(e)
	return nb, err == nil
}
