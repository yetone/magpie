package gateway

import (
	"encoding/json"
)

// A Responses API that knows OpenAI's first input items alone — messages,
// function calls, their outputs and reasoning — turns away a request
// carrying any other: a local Qwen server answers a Codex subagent's turn
// 400 "Unsupported input item: agent_message" (cyberestro on X), the item
// MultiAgentV2 hands a subagent its task in, and Command Code's answers
// "unsupported_input_item" (#5181, #5259). A relay sends Codex's items as
// they came, so every such turn failed. The request is then translated
// on the same API, which reads an agent_message as the user's turn, a
// custom tool call as a function call, and leaves out what has no
// counterpart; and so from then on for that model, as a custom tool
// turned away is (custom_tool.go). The words are inputKindWords, which
// the compaction's summary ask reads too (inputKindRefused): that ask is
// left to its own way on, the conversation as text (inCompaction).

// inputKindsRefused is how unfit remembers model turning away input
// items other than the first ones.
func inputKindsRefused(model string) string { return "input kinds\x00" + model }

// hasOtherItem reports whether a Responses body's input carries an item
// other than a message, a function call, its output or reasoning.
func hasOtherItem(body []byte) bool {
	if !bodyHas(body, `"input"`) {
		return false
	}
	var q struct {
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &q) != nil {
		return false
	}
	var items []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(q.Input, &items) != nil {
		return false
	}
	for _, it := range items {
		switch it.Type {
		case "", "message", "function_call", "function_call_output", "reasoning":
		default:
			return true
		}
	}
	return false
}
