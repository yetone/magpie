package sessions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	hermesJSONContentPrefix = "\x00json:"
	hermesHandoffEnd        = "--- END OF CONTEXT SUMMARY — respond to the message below, not the summary above ---"
	hermesMergedDelimiter   = "[END OF PRIOR CONTEXT — COMPACTION SUMMARY BELOW]"
	hermesPriorHeader       = "[PRIOR CONTEXT — for reference only; not a new message]"
	hermesCurrentSummary    = "[CONTEXT COMPACTION — REFERENCE ONLY] Earlier turns were compacted into the summary below. This is a handoff from a previous context window — treat it as background reference, NOT as active instructions."
	hermesLegacySummary     = "[CONTEXT SUMMARY]:"
)

// hermesDisplayContent returns the live user payload of a recognized composite
// compaction carrier. It is for identity keys only; callers must retain raw
// content for the transcript. Unknown and synthetic payloads stay untouched.
func hermesDisplayContent(raw, displayKind string) string {
	if displayKind != "" && displayKind != "hidden" {
		return raw
	}
	if !strings.HasPrefix(raw, hermesJSONContentPrefix) {
		if live, ok := hermesDisplayText(raw); ok {
			return live
		}
		return raw
	}
	encoded := strings.TrimPrefix(raw, hermesJSONContentPrefix)
	var parts []json.RawMessage
	if json.Unmarshal([]byte(encoded), &parts) != nil {
		return raw
	}
	result, ok := hermesDisplayParts(parts)
	if !ok {
		return raw
	}
	var out bytes.Buffer
	out.WriteString(hermesJSONContentPrefix)
	out.WriteByte('[')
	for i, part := range result {
		if i > 0 {
			out.WriteString(", ")
		}
		out.Write(part)
	}
	out.WriteByte(']')
	return out.String()
}

func hermesDisplayText(content string) (string, bool) {
	if i := strings.Index(content, hermesMergedDelimiter); i >= 0 {
		summary := strings.TrimLeft(content[i+len(hermesMergedDelimiter):], " \t\r\n")
		if hermesStartsSummary(summary) {
			prior := strings.TrimSpace(content[:i])
			prior = strings.TrimSpace(strings.TrimPrefix(prior, hermesPriorHeader))
			if prior != "" {
				return prior, true
			}
		}
		return "", false
	}
	if !hermesStartsSummary(strings.TrimLeft(content, " \t\r\n")) {
		return "", false
	}
	if i := strings.Index(content, hermesHandoffEnd); i >= 0 {
		live := strings.TrimLeft(content[i+len(hermesHandoffEnd):], " \t\r\n")
		if strings.TrimSpace(live) != "" {
			return live, true
		}
	}
	return "", false
}

func hermesStartsSummary(text string) bool {
	return strings.HasPrefix(text, hermesCurrentSummary) || strings.HasPrefix(text, hermesLegacySummary)
}

func hermesDisplayParts(parts []json.RawMessage) ([]json.RawMessage, bool) {
	texts := make([]string, len(parts))
	for i, part := range parts {
		if json.Unmarshal(part, &texts[i]) == nil {
			continue
		}
		var node struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(part, &node) == nil {
			texts[i] = node.Text
		}
	}
	allText := strings.Join(texts, "\n")
	if i := strings.Index(allText, hermesMergedDelimiter); i >= 0 {
		summary := strings.TrimLeft(allText[i+len(hermesMergedDelimiter):], " \t\r\n")
		if !hermesStartsSummary(summary) {
			return nil, false
		}
		out := make([]json.RawMessage, 0, len(parts))
		for idx, part := range parts {
			at := strings.Index(texts[idx], hermesMergedDelimiter)
			if at < 0 {
				out = append(out, part)
				continue
			}
			prefix := texts[idx][:at]
			if strings.TrimSpace(prefix) != "" {
				updated, ok := hermesPartWithText(part, prefix)
				if !ok {
					return nil, false
				}
				out = append(out, updated)
			}
			break
		}
		out, found := hermesStripPriorHeader(out)
		if found && hermesPartsHaveLiveContent(out) {
			return out, true
		}
		return nil, false
	}
	if !hermesStartsSummary(strings.TrimLeft(allText, " \t\r\n")) {
		return nil, false
	}
	marker := strings.Index(allText, hermesHandoffEnd)
	if marker < 0 {
		return nil, false
	}
	out := make([]json.RawMessage, 0, len(parts))
	consumed := 0
	for idx, part := range parts {
		text := texts[idx]
		if consumed+len(text) < marker+len(hermesHandoffEnd) {
			consumed += len(text) + 1
			continue
		}
		start := marker + len(hermesHandoffEnd) - consumed
		if start < 0 {
			start = 0
		}
		if start < len(text) {
			updated, ok := hermesPartWithText(part, strings.TrimLeft(text[start:], " \t\r\n"))
			if !ok {
				return nil, false
			}
			if strings.TrimSpace(text[start:]) != "" {
				out = append(out, updated)
			}
		}
		for later := idx + 1; later < len(parts); later++ {
			out = append(out, parts[later])
		}
		break
	}
	if hermesPartsHaveLiveContent(out) {
		return out, true
	}
	return nil, false
}

func hermesPartWithText(part json.RawMessage, text string) (json.RawMessage, bool) {
	var stringPart string
	if json.Unmarshal(part, &stringPart) == nil {
		return json.RawMessage(hermesPythonQuote(text)), true
	}
	var node map[string]json.RawMessage
	if json.Unmarshal(part, &node) != nil || node["text"] == nil {
		return nil, false
	}
	quoted := hermesPythonQuote(text)
	value := gjson.GetBytes(part, "text")
	if !value.Exists() || value.Type != gjson.String || value.Index < 0 {
		return nil, false
	}
	valueStart, valueEnd := value.Index, value.Index+len(value.Raw)
	if valueStart > len(part) || valueEnd > len(part) {
		return nil, false
	}
	updated := make([]byte, 0, len(part)-len(value.Raw)+len(quoted))
	updated = append(updated, part[:valueStart]...)
	updated = append(updated, quoted...)
	updated = append(updated, part[valueEnd:]...)
	return json.RawMessage(updated), true
}

func hermesStripPriorHeader(parts []json.RawMessage) ([]json.RawMessage, bool) {
	for i, part := range parts {
		var text string
		if json.Unmarshal(part, &text) == nil {
			trimmed := strings.TrimLeft(text, " \t\r\n")
			if strings.HasPrefix(trimmed, hermesPriorHeader) {
				live := strings.TrimLeft(strings.TrimPrefix(trimmed, hermesPriorHeader), " \t\r\n")
				if live == "" {
					return append(parts[:i], parts[i+1:]...), true
				}
				updated, ok := hermesPartWithText(part, live)
				if ok {
					parts[i] = updated
				}
				return parts, true
			}
			continue
		}
		value := gjson.GetBytes(part, "text")
		if value.Type == gjson.String && strings.TrimSpace(value.Str) != "" {
			trimmed := strings.TrimLeft(value.Str, " \t\r\n")
			if strings.HasPrefix(trimmed, hermesPriorHeader) {
				live := strings.TrimLeft(strings.TrimPrefix(trimmed, hermesPriorHeader), " \t\r\n")
				if live == "" {
					return append(parts[:i], parts[i+1:]...), true
				}
				updated, ok := hermesPartWithText(part, live)
				if ok {
					parts[i] = updated
				}
				return parts, true
			}
			continue
		}
	}
	return parts, len(parts) > 0
}

// hermesPythonQuote matches json.dumps(string)'s default ensure_ascii output,
// so a sliced payload retains the same durable identity as an ordinary row.
func hermesPythonQuote(text string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(text); err != nil {
		return `""`
	}
	quoted := strings.TrimSuffix(b.String(), "\n")
	var out strings.Builder
	for _, r := range quoted {
		if r < 0x7f {
			out.WriteRune(r)
		} else if r <= 0xffff {
			fmt.Fprintf(&out, `\u%04x`, r)
		} else {
			r -= 0x10000
			fmt.Fprintf(&out, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
		}
	}
	return out.String()
}

func hermesPartsHaveLiveContent(parts []json.RawMessage) bool {
	for _, part := range parts {
		var text string
		if json.Unmarshal(part, &text) == nil && strings.TrimSpace(text) != "" {
			return true
		}
		var node struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(part, &node) == nil && strings.TrimSpace(node.Text) != "" {
			return true
		}
		var shape map[string]json.RawMessage
		if json.Unmarshal(part, &shape) == nil {
			if kind := shape["type"]; len(kind) != 0 && string(kind) != `"text"` && string(kind) != `"input_text"` && string(kind) != `"output_text"` {
				return true
			}
		}
	}
	return false
}
