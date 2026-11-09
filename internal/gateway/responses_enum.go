package gateway

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
)

// responsesEnum is the OpenAI Responses stream event enum: the names a typed
// Responses client knows. Everything the ChatGPT backend says of its own
// that is not here — codex.rate_limits and codex.response.metadata (the
// account's windows, the model list's tag), response.metadata (the turn's
// headers on a socket, its moderation word on the HTTP route), keepalive (a
// quiet upstream's heartbeat) — is read as an unknown variant by such a
// client, which gives up on the whole turn. The list is the one the client
// names in "expected one of ...", with the audio events OpenAI's own SDK
// also names.
var responsesEnum = map[string]struct{}{
	"response.created":                             {},
	"response.in_progress":                         {},
	"response.completed":                           {},
	"response.failed":                              {},
	"response.incomplete":                          {},
	"response.output_item.added":                   {},
	"response.output_item.done":                    {},
	"response.content_part.added":                  {},
	"response.content_part.done":                   {},
	"response.output_text.delta":                   {},
	"response.output_text.done":                    {},
	"response.audio.delta":                         {},
	"response.audio.done":                          {},
	"response.audio.transcript.delta":              {},
	"response.audio.transcript.done":               {},
	"response.refusal.delta":                       {},
	"response.refusal.done":                        {},
	"response.function_call_arguments.delta":       {},
	"response.function_call_arguments.done":        {},
	"response.file_search_call.in_progress":        {},
	"response.file_search_call.searching":          {},
	"response.file_search_call.completed":          {},
	"response.web_search_call.in_progress":         {},
	"response.web_search_call.searching":           {},
	"response.web_search_call.completed":           {},
	"response.reasoning_summary_part.added":        {},
	"response.reasoning_summary_part.done":         {},
	"response.reasoning_summary_text.delta":        {},
	"response.reasoning_summary_text.done":         {},
	"response.reasoning_text.delta":                {},
	"response.reasoning_text.done":                 {},
	"response.image_generation_call.completed":     {},
	"response.image_generation_call.generating":    {},
	"response.image_generation_call.in_progress":   {},
	"response.image_generation_call.partial_image": {},
	"response.mcp_call_arguments.delta":            {},
	"response.mcp_call_arguments.done":             {},
	"response.mcp_call.completed":                  {},
	"response.mcp_call.failed":                     {},
	"response.mcp_call.in_progress":                {},
	"response.mcp_list_tools.completed":            {},
	"response.mcp_list_tools.failed":               {},
	"response.mcp_list_tools.in_progress":          {},
	"response.code_interpreter_call.in_progress":   {},
	"response.code_interpreter_call.interpreting":  {},
	"response.code_interpreter_call.completed":     {},
	"response.code_interpreter_call_code.delta":    {},
	"response.code_interpreter_call_code.done":     {},
	"response.output_text.annotation.added":        {},
	"response.queued":                              {},
	"response.custom_tool_call_input.delta":        {},
	"response.custom_tool_call_input.done":         {},
	"error":                                        {},
}

// responsesEnumTidy keeps events outside the Responses enum out of a reply
// to a client that isn't Codex. A client with a closed event enum reads one
// and gives up on the whole turn; Codex has names for the backend's own
// events and reads its limits from them, so its own request still gets every
// event as it came.
type responsesEnumTidy struct {
	buf []byte
}

func (t *responsesEnumTidy) write(b []byte) []byte {
	t.buf = append(t.buf, b...)
	out, rest := tidyEvents(t.buf, t.event)
	t.buf = rest
	return out
}

func (t *responsesEnumTidy) flush() []byte {
	b := t.buf
	t.buf = nil
	return b
}

// event is one whole SSE event, dropped when it is outside the enum. A
// keepalive goes on as an SSE comment, which every client skips, so a quiet
// upstream's heartbeat still keeps the client's idle timer from running out.
func (t *responsesEnumTidy) event(ev []byte) []byte {
	if responsesEnumAllowed(ev) {
		return ev
	}
	if sseEventName(ev) == "keepalive" {
		return []byte(": keepalive\n\n")
	}
	return nil
}

// responsesEnumAllowed says an event may go to a Responses client: its SSE
// name, or the type its data carries where the stream names none, is in the
// enum. A frame with neither is a comment or a blank separator, not an
// event, and goes as it is.
func responsesEnumAllowed(ev []byte) bool {
	name := sseEventName(ev)
	if name == "" {
		return true
	}
	_, ok := responsesEnum[name]
	return ok
}

// sseEventName is a frame's `event:` name, else the `type` its data carries,
// else "".
func sseEventName(ev []byte) string {
	for _, line := range bytes.Split(ev, []byte("\n")) {
		v, ok := bytes.CutPrefix(bytes.TrimRight(line, "\r"), []byte("event:"))
		if !ok {
			continue
		}
		return strings.TrimSpace(string(v))
	}
	data := eventData(ev)
	if len(data) == 0 {
		return ""
	}
	return gjson.GetBytes(data, "type").String()
}
