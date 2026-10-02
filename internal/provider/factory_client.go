package provider

// PLUGIN-SERVED (see AGENTS.md): Factory ("factory") is a deprecated
// built-in subscription served by its plugin,
// @magpie-community/opencode-factory-auth, once moved onto it
// (provider.Moved; the default for a new sign-in). A moved one's sign-ins,
// models, requests and usage are all the plugin's, never this code's (only
// the move, in migrate*.go, still reads its accounts). A fix here alone
// doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/factory) and raise the
// mover's min in internal/provider/migrate_factory.go.

// Factory takes a subscription's model requests only from Droid (#242,
// #506): the same account's GPT, Grok and GLM answered Droid through magpie
// every time and refused Codex, Grok Build and Claude Code every time, on
// the same models, efforts, endpoint and headers, the body the only
// difference. Every request droid sends opens its system prompt with one
// line, droid's YOU_ARE_DROID_SYSTEM_PROMPT (droid 0.231.0: its system
// blocks are [that line, the agent's prompt, reminders…], sent as
// Responses' instructions joined with "\n", and on chat completions as one
// system message first, joined the same way; its smaller calls, the
// latency ping among them, send the line alone). So another agent's request
// to Factory's OpenAI-shaped API (/api/llm/o: GPT and Grok on Responses,
// the open models on chat completions) opens with that line too, the
// agent's own system prompt after it, as droid's own does. Anthropic's
// Messages (/api/llm/a: Claude, MiniMax M2.7) is left as the agent sent
// it, and so is a request that already opens with the line: droid's own
// goes on byte for byte.

import (
	"encoding/json"
	"strings"
)

// factoryDroidLine is the line every droid system prompt opens with.
const factoryDroidLine = "You are Droid, an AI software engineering agent built by Factory."

// factoryDroidBody is body, a request to Factory's /api/llm/o at path, as
// droid would open it: Responses' instructions, or chat completions' first
// system message, starting with droid's line. Anything else, a body that
// can't be read, or one that already starts so is returned as it is.
func factoryDroidBody(path string, body []byte) []byte {
	if !strings.Contains(path, "/llm/o/") || len(body) == 0 {
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
			in = factoryDroidLine + "\n" + in
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
				ms[0]["content"] = factoryDroidLine + "\n" + c
			}
			return true
		case []any:
			// droid sends a string, its blocks joined with "\n": text
			// parts alone are joined so
			texts := []string{factoryDroidLine}
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
				ms[0]["content"] = strings.Join(texts, "\n")
			} else {
				ms[0]["content"] = append([]any{map[string]any{"type": "text", "text": factoryDroidLine}}, c...)
			}
			return true
		}
	}
	*msgs = append([]map[string]any{{"role": "system", "content": factoryDroidLine}}, ms...)
	return true
}
