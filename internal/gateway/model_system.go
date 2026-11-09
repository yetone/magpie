package gateway

import (
	"net/http"
	"strconv"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// A model's own system prompt (liucl on X): the text the user gave one
// model in its Model settings (provider.ModelSystemPrompt) is added to
// every request for that model, whichever agent sends it, on every route
// that asks it (chat on each API, count_tokens), a signed-in account's and
// a plugin's included: it is added in the agent's own API before the
// request is translated or handed to a plugin's fetch.
//
// It is only ever added after what the agent says, never in its place and
// never before it, so an identity the agent's prompt starts with (Claude
// Code's) stays first and Codex's base_instructions are never changed:
//   - Anthropic: a text block after the last of system's blocks, or a
//     paragraph after a system that is one string; a request with none
//     gets it as its system.
//   - Chat: a paragraph at the end of the last of the system (or
//     developer) messages the conversation starts with; a conversation
//     starting with none gets a system message of its own first.
//   - Responses: instructions are never changed; a developer message of
//     its own goes after the system and developer items input starts
//     with (first when there are none), the same place every turn, so the
//     prefix before it stays cached. A turn that goes on from a stored
//     response (previous_response_id still set) is left as it is: its
//     input is only the new items, and the message would pile up in the
//     stored conversation.
//   - Gemini: a part after systemInstruction's, or a systemInstruction of
//     its own.
//
// Prepending is not offered. A model with no prompt of its own is sent the
// body byte for byte as the agent wrote it. magpie's own asks (titles, the
// router's classifier, search and vision stand-ins) go without it: their
// replies are magpie's to read, in a shape the text could break.
func withModelSystem(r *http.Request, p provider.Provider, model string, from provider.Protocol, body []byte) []byte {
	text := provider.ModelSystemPrompt(p.ID, model)
	if text == "" || magpiesOwnAsk(r) {
		return body
	}
	return addSystem(from, body, text)
}

// magpiesOwnAsk says r is a request magpie makes of a model for itself.
func magpiesOwnAsk(r *http.Request) bool {
	if r == nil {
		return false
	}
	ctx := r.Context()
	if on, set := ctx.Value(agentTurnKey{}).(bool); set && !on {
		return true // ownAsk: search and vision stand-ins
	}
	if ctx.Value(titleCheckKey{}) != nil {
		return true // a title, in the shape it is read in
	}
	return r.UserAgent() == RouterAgent // the router's classifier
}

// addSystem is body, in from's API, with text added after its system
// prompt (withModelSystem). A body it can't read, or with no place for
// it, is returned as it is; the edit is made in place in the bytes, the
// rest as the client wrote it.
func addSystem(from provider.Protocol, body []byte, text string) []byte {
	if text == "" || !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return body
	}
	switch from {
	case provider.Anthropic:
		return addSystemAnthropic(body, text)
	case provider.Chat:
		return addSystemChat(body, text)
	case provider.Responses:
		return addSystemResponses(body, text)
	case provider.Gemini:
		return addSystemGemini(body, text)
	}
	return body
}

func addSystemAnthropic(body []byte, text string) []byte {
	s := gjson.GetBytes(body, "system")
	switch {
	case !s.Exists():
		return withTopField(body, "system", noteQuoted(text))
	case s.Type == gjson.Null:
		return spliced(body, s, noteQuoted(text))
	case s.Type == gjson.String:
		return spliced(body, s, noteQuoted(withLine(s.String(), text)))
	case s.IsArray():
		if len(s.Array()) == 0 {
			return spliced(body, s, noteQuoted(text))
		}
		return appended(body, s, noteJSON(map[string]any{"type": "text", "text": "\n\n" + text}))
	}
	return body
}

func addSystemChat(body []byte, text string) []byte {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return body
	}
	last := -1
	for i, m := range msgs.Array() {
		if !isSystemRole(m) {
			break
		}
		last = i
	}
	if last < 0 {
		return prepended(body, msgs, noteJSON(map[string]any{"role": "system", "content": text}))
	}
	c := gjson.GetBytes(body, "messages."+strconv.Itoa(last)+".content")
	switch {
	case c.Type == gjson.String:
		return spliced(body, c, noteQuoted(withLine(c.String(), text)))
	case c.IsArray():
		return appended(body, c, noteJSON(map[string]any{"type": "text", "text": "\n\n" + text}))
	case c.Type == gjson.Null:
		return spliced(body, c, noteQuoted(text))
	}
	return body
}

func addSystemResponses(body []byte, text string) []byte {
	if gjson.GetBytes(body, "previous_response_id").String() != "" {
		return body
	}
	in := gjson.GetBytes(body, "input")
	dev := noteJSON(map[string]any{"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": text}}})
	if in.Type == gjson.String {
		user := noteJSON(map[string]any{"role": "user", "content": in.String()})
		return spliced(body, in, []byte("["+string(dev)+","+string(user)+"]"))
	}
	if !in.IsArray() {
		return body
	}
	last := -1
	for i, item := range in.Array() {
		if !isSystemRole(item) || (item.Get("type").Exists() && item.Get("type").String() != "message") {
			break
		}
		last = i
	}
	if last < 0 {
		return prepended(body, in, dev)
	}
	after := gjson.GetBytes(body, "input."+strconv.Itoa(last))
	if !at(body, after) {
		return body
	}
	return insertAt(body, after.Index+len(after.Raw), ","+string(dev))
}

func addSystemGemini(body []byte, text string) []byte {
	part := noteJSON(map[string]any{"parts": []any{map[string]any{"text": text}}})
	for _, key := range []string{"systemInstruction", "system_instruction"} {
		si := gjson.GetBytes(body, key)
		if !si.Exists() || si.Type == gjson.Null {
			continue
		}
		parts := gjson.GetBytes(body, key+".parts")
		switch {
		case parts.IsArray() && len(parts.Array()) > 0:
			return appended(body, parts, noteJSON(map[string]any{"text": "\n\n" + text}))
		case parts.IsArray():
			return appended(body, parts, noteJSON(map[string]any{"text": text}))
		}
		return body
	}
	for _, key := range []string{"systemInstruction", "system_instruction"} {
		if si := gjson.GetBytes(body, key); si.Type == gjson.Null && si.Exists() {
			return spliced(body, si, part)
		}
	}
	return withTopField(body, "systemInstruction", part)
}
