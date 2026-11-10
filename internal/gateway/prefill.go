package gateway

import (
	"encoding/json"

	"github.com/yetone/magpie/internal/lazyre"
)

// A model that won't go on from a conversation ending with the
// assistant's message (#1447). OpenCode Go's Claude models refuse one,
// "This model does not support assistant message prefill. The
// conversation must end with a user message.", as Volcengine's Agent Plan
// does, "The last message cannot be from the assistant for a model that
// does not support prefill", and Gemini on Vertex AI's, "Requests ending
// with a model turn are not supported" (yycsyu; Gemini's own requests never
// end so: buildCodeAssistSent). The same providers' other models take it, so
// the refusal is remembered for that model on that provider and API, in
// unfit, and nowhere else.
//
// Such a conversation reaches the upstream two ways. magpie's own
// continuation of a reply cut mid-way (continuation.go) is not asked of
// that model again: the reply ends with the cut's own error, as it did
// before continuations, not with the refusal. A conversation the client
// sent ending with the assistant's message, which OpenAI's Chat and
// Responses APIs read as a turn already said, not one to go on from
// (Request.LastIsTurn), is asked with a user turn after it, as the
// refusal says: the conversation the model takes, answered after the
// assistant's message as an OpenAI upstream answers it. An Anthropic
// client's prefill is the client's own ask, and gets the refusal as
// Anthropic itself answers it.
// A request built in Gemini's own shape is the exception: every upstream
// of it turns the model's turn last away, so it never goes out so
// (buildCodeAssistSent, modelLastAsk).

// prefillRefusal is an upstream's refusal of a conversation ending with
// the assistant's message.
var prefillRefusal = lazyre.New(`(?i)does not support (?:assistant message )?prefill|requests ending with a model turn are not supported`)

// prefillRefused is how unfit remembers a provider's model refusing a
// conversation ending with the assistant's message.
func prefillRefused(model string) string { return "prefill\x00" + model }

// refusesPrefill says whether an upstream's answer refuses a prefill.
func refusesPrefill(status int, body []byte) bool {
	return badRequest(status) && prefillRefusal.Match(body)
}

// continueText is the user turn asked after a conversation the client
// ended with the assistant's message, for a model that takes no prefill.
const continueText = "Continue."

// compactLastText is that user turn when the conversation is an agent
// compacting it with the ask in the system prompt (compacting): "Continue."
// there reads as going on with the task, and the summary isn't written.
const compactLastText = "Write the summary of the conversation above that the system prompt asks for."

// modelLastAsk is the user turn put after a conversation ending with the
// model's turn, for an upstream that takes none (buildCodeAssistSent).
func modelLastAsk(r *Request) string {
	if compacting(r) {
		return compactLastText
	}
	return continueText
}

// endsWithAssistant says whether r's conversation ends with the
// assistant's message rather than a call it made: what a prefill is.
func (r *Request) endsWithAssistant() bool {
	n := len(r.Messages)
	if n == 0 || r.Messages[n-1].Role != "assistant" {
		return false
	}
	for _, p := range r.Messages[n-1].Parts {
		if p.Kind == ToolCall {
			return false
		}
	}
	return true
}

// userLast is r with a user turn after its last, the assistant's,
// message.
func (r *Request) userLast() *Request {
	q := *r
	q.Messages = append(q.Messages[:len(q.Messages):len(q.Messages)], Message{Role: "user", Parts: []Part{{Kind: Text, Text: continueText}}})
	return &q
}

// userLastBody is a Chat or Responses request relayed as it is, with a
// user turn after its last message when that is the assistant's; false
// when it isn't.
func userLastBody(chat bool, body []byte) ([]byte, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return body, false
	}
	field := "input"
	if chat {
		field = "messages"
	}
	var items []json.RawMessage
	if json.Unmarshal(m[field], &items) != nil || len(items) == 0 {
		return body, false
	}
	var last struct {
		Type      string            `json:"type"`
		Role      string            `json:"role"`
		ToolCalls []json.RawMessage `json:"tool_calls"`
	}
	json.Unmarshal(items[len(items)-1], &last)
	if last.Role != "assistant" || last.Type != "" && last.Type != "message" || len(last.ToolCalls) > 0 {
		return body, false
	}
	var turn any = map[string]any{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": continueText}}}
	if chat {
		turn = map[string]string{"role": "user", "content": continueText}
	}
	t, _ := json.Marshal(turn)
	m[field], _ = json.Marshal(append(items, t))
	out, err := json.Marshal(m)
	if err != nil {
		return body, false
	}
	return out, true
}
