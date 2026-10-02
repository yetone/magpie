package provider

// Factory takes a subscription's model requests only from Droid (#242,
// #506): the same account's models answered Droid through magpie and
// refused Codex, Grok Build and Claude Code, on the same models, efforts,
// endpoint and headers, the body the only difference. Every request droid
// sends opens its system prompt with one line, droid's
// YOU_ARE_DROID_SYSTEM_PROMPT (droid 0.231.0: its system blocks are [that
// line, the agent's prompt, reminders…]). Responses joins those blocks
// with "\n" into instructions, chat completions joins them into the first
// system message, Anthropic's Messages keeps them as blocks with the line
// first, and Gemini's generateContent joins them into the one
// systemInstruction part. Another agent's prompt follows, joined on with
// one newline, as droid joins its own blocks. With no prompt of its own
// the line stands alone: the "\n" is only between blocks. One that already
// opens with the line goes on byte for byte, and so does droid's own.

import (
	"encoding/json"
	"strings"
)

// factoryDroidLine is the line every droid system prompt opens with.
const factoryDroidLine = "You are Droid, an AI software engineering agent built by Factory."

// factoryDroidPrompt is the opening droid writes in front of another
// agent's prompt: the line, then one newline.
const factoryDroidPrompt = factoryDroidLine + "\n"

// factoryDroidBody is body, a request to Factory at path, as droid would
// open it. /api/llm/o: Responses' instructions, or chat completions' first
// system message. /api/llm/a: Anthropic's system blocks, the line first
// (factoryDroidMessages). /api/llm/g: Gemini's systemInstruction parts,
// the line first (factoryDroidGoogle). Anything else, a body that can't
// be read, or one that already starts so is returned as it is.
func factoryDroidBody(path string, body []byte) []byte {
	if len(body) == 0 || (!strings.Contains(path, "/llm/o/") && !strings.Contains(path, "/llm/a/") && !strings.Contains(path, "/llm/g/")) {
		return body
	}
	var m map[string]json.RawMessage
	if zcodeDecode(body, &m) != nil {
		return body
	}
	switch {
	case strings.HasSuffix(path, "/responses"):
		var in string
		if raw, ok := m["instructions"]; ok && string(raw) != "null" && json.Unmarshal(raw, &in) != nil {
			return body // not a string: not something droid sends
		}
		if strings.HasPrefix(in, factoryDroidLine) {
			return body
		}
		if strings.TrimSpace(in) == "" {
			in = factoryDroidLine
		} else {
			in = factoryDroidPrompt + in
		}
		m["instructions"], _ = zcodeEncode(in)
	case strings.HasSuffix(path, "/chat/completions"):
		var msgs []map[string]any
		if m["messages"] == nil || zcodeDecode(m["messages"], &msgs) != nil {
			return body
		}
		if !factoryDroidChat(&msgs) {
			return body
		}
		var err error
		if m["messages"], err = zcodeEncode(msgs); err != nil {
			return body
		}
	case strings.HasSuffix(path, "/messages"):
		if !factoryDroidMessages(&m) {
			return body
		}
	case strings.HasSuffix(path, "/generate"):
		if !factoryDroidGoogle(&m) {
			return body
		}
	default:
		return body
	}
	out, err := zcodeEncode(m)
	if err != nil {
		return body
	}
	return out
}

// factoryDroidChat opens msgs' first system message with droid's line, or
// puts one before them with the line alone: false when it opens so already.
func factoryDroidChat(msgs *[]map[string]any) bool {
	ms := *msgs
	if len(ms) > 0 && ms[0]["role"] == "system" {
		switch c := ms[0]["content"].(type) {
		case string:
			if strings.HasPrefix(c, factoryDroidLine) {
				return false
			}
			if strings.TrimSpace(c) == "" {
				ms[0]["content"] = factoryDroidLine
			} else {
				ms[0]["content"] = factoryDroidPrompt + c
			}
			return true
		case []any:
			// droid sends a string, its blocks joined with "\n": text
			// parts alone are joined so, after the prompt
			var texts []string
			for i, p := range c {
				p, _ := p.(map[string]any)
				t, ok := p["text"].(string)
				if p["type"] != "text" || !ok {
					texts = nil
					break
				}
				if i == 0 && strings.HasPrefix(t, factoryDroidLine) {
					return false
				}
				texts = append(texts, t)
			}
			if texts != nil {
				joined := strings.Join(texts, "\n")
				if strings.TrimSpace(joined) == "" {
					ms[0]["content"] = factoryDroidLine
				} else {
					ms[0]["content"] = factoryDroidPrompt + joined
				}
			} else {
				ms[0]["content"] = append([]any{map[string]any{"type": "text", "text": factoryDroidLine}}, c...)
			}
			return true
		}
	}
	*msgs = append([]map[string]any{{"role": "system", "content": factoryDroidLine}}, ms...)
	return true
}

// factoryDroidMessages opens an Anthropic Messages body's system with
// droid's line as the first block, then the agent's prompt. A string system
// becomes those two blocks. False when it opens with the line already, or
// the field can't be read.
func factoryDroidMessages(m *map[string]json.RawMessage) bool {
	raw, ok := (*m)["system"]
	if !ok || string(raw) == "null" {
		b, err := zcodeEncode([]any{map[string]any{"type": "text", "text": factoryDroidLine}})
		if err != nil {
			return false
		}
		(*m)["system"] = b
		return true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if strings.HasPrefix(s, factoryDroidLine) {
			return false
		}
		blocks := []any{map[string]any{"type": "text", "text": factoryDroidLine}}
		if strings.TrimSpace(s) != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": s})
		}
		b, err := zcodeEncode(blocks)
		if err != nil {
			return false
		}
		(*m)["system"] = b
		return true
	}
	var blocks []any
	if zcodeDecode(raw, &blocks) != nil {
		return false
	}
	if len(blocks) > 0 {
		if b, ok := blocks[0].(map[string]any); ok {
			if t, _ := b["text"].(string); b["type"] == "text" && strings.HasPrefix(t, factoryDroidLine) {
				return false
			}
		}
	}
	blocks = append([]any{map[string]any{"type": "text", "text": factoryDroidLine}}, blocks...)
	b, err := zcodeEncode(blocks)
	if err != nil {
		return false
	}
	(*m)["system"] = b
	return true
}

// factoryDroidGoogle opens a generateContent body's systemInstruction the
// way droid does, and the way Responses and chat do: one part, its text the
// prompt and then the agent's, joined. droid joins its own blocks with "\n"
// into that one part. With nothing of the agent's, the part is the line
// alone. A body that already opens with the line is left as it is. False
// when the field can't be read.
func factoryDroidGoogle(m *map[string]json.RawMessage) bool {
	raw, ok := (*m)["systemInstruction"]
	if !ok || string(raw) == "null" {
		return factoryDroidGoogleText(m, nil, factoryDroidLine)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if strings.HasPrefix(s, factoryDroidLine) {
			return false
		}
		text := factoryDroidLine
		if strings.TrimSpace(s) != "" {
			text = factoryDroidPrompt + s
		}
		return factoryDroidGoogleText(m, nil, text)
	}
	var content map[string]any
	if zcodeDecode(raw, &content) != nil {
		return false
	}
	parts, _ := content["parts"].([]any)
	if len(parts) > 0 {
		if p, ok := parts[0].(map[string]any); ok {
			if t, _ := p["text"].(string); strings.HasPrefix(t, factoryDroidLine) {
				return false
			}
		}
	}
	var texts []string
	var rest []any
	for _, p := range parts {
		pm, ok := p.(map[string]any)
		t, tok := pm["text"].(string)
		if ok && tok && len(pm) == 1 {
			texts = append(texts, t)
			continue
		}
		rest = append(rest, p)
	}
	text := factoryDroidLine
	if joined := strings.Join(texts, "\n"); strings.TrimSpace(joined) != "" {
		text = factoryDroidPrompt + joined
	}
	return factoryDroidGoogleText(m, content, text, rest...)
}

// factoryDroidGoogleText writes systemInstruction as one text part, then
// rest. content's other fields are kept when it is the instruction object
// already; nil starts a new one.
func factoryDroidGoogleText(m *map[string]json.RawMessage, content map[string]any, text string, rest ...any) bool {
	if content == nil {
		content = map[string]any{}
	}
	content["parts"] = append([]any{map[string]any{"text": text}}, rest...)
	b, err := zcodeEncode(content)
	if err != nil {
		return false
	}
	(*m)["systemInstruction"] = b
	return true
}
