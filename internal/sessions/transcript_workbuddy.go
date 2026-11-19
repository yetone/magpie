package sessions

import (
	"bytes"
	"cmp"
	"encoding/json"
	"strings"
)

// wbTranscript tells add what a WorkBuddy, WorkBuddy AI or CodeBuddy Code
// session said, from the same lines its summary is read from
// (workbuddyLine): message lines (a user's input_text, a reply's
// output_text), a function_call with its arguments, and its
// function_call_result.
func wbTranscript(path string, add func(bool, Part) bool) error {
	names := map[string]string{} // a call's tool name, by its callId
	_, err := scanAt(path, 0, nil, func(b []byte, _, _ int64) bool {
		if !bytes.Contains(b, []byte(`"type":"message"`)) &&
			!bytes.Contains(b, []byte(`"type":"function_call`)) {
			return true
		}
		var l struct {
			wbLine
			Name      string          `json:"name"`
			CallID    string          `json:"callId"`
			Arguments json.RawMessage `json:"arguments"`
			Output    json.RawMessage `json:"output"`
		}
		if json.Unmarshal(b, &l) != nil {
			return true
		}
		for _, p := range wbParts(l.Type, l.Role, l.Content, l.Name, l.CallID, l.Arguments, l.Output, l.ProviderData.Model, names) {
			if !add(p.Role != "assistant", p) {
				return false
			}
		}
		return true
	})
	return err
}

func wbParts(typ, role string, content json.RawMessage, name, callID string, args, output json.RawMessage, model string, names map[string]string) []Part {
	switch typ {
	case "message":
		if role != "user" && role != "assistant" {
			return nil
		}
		// one part to a block: a user message holds WorkBuddy's own
		// <system-reminder> and @scene: blocks beside what was typed
		var parts []Part
		for _, text := range wbTexts(content) {
			if role == "user" && wbScene.MatchString(strings.TrimSpace(text)) {
				continue
			}
			p := textPart(role, text)
			if role == "assistant" {
				p.Model = model
			}
			parts = append(parts, p)
		}
		return parts
	case "function_call":
		names[callID] = name
		var s string
		if json.Unmarshal(args, &s) == nil {
			args = json.RawMessage(s)
		}
		text := string(args)
		if json.Valid(args) {
			text = pretty(args)
		}
		return []Part{{Role: "assistant", Kind: "tool_use", Name: name, Text: text, Model: model}}
	case "function_call_result":
		var o struct{ Text string }
		text := resultText(output)
		if json.Unmarshal(output, &o) == nil && o.Text != "" {
			text = o.Text
		}
		return []Part{{Role: "tool", Kind: "tool_result", Name: cmp.Or(names[callID], name), Text: text}}
	}
	return nil
}

// wbTexts are the words of a message: its string, or each of its text
// blocks.
func wbTexts(content json.RawMessage) []string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []string{text}
	}
	var blocks []struct{ Type, Text string }
	if json.Unmarshal(content, &blocks) != nil {
		return nil
	}
	var out []string
	for _, b := range blocks {
		if (b.Type == "input_text" || b.Type == "output_text" || b.Type == "text") && strings.TrimSpace(b.Text) != "" {
			out = append(out, b.Text)
		}
	}
	return out
}
