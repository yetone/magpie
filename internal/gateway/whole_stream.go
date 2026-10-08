package gateway

import (
	"encoding/json"
	"maps"
	"net/http"

	"github.com/yetone/magpie/internal/provider"
)

// wholeAsStream writes a reply its vendor gave whole, in proto's own shape,
// as proto's stream: the agent asked for a stream, and has the stream's
// headers already from keepAlive, so a whole reply can no longer go to it
// as one. Every item, block and part goes out as the vendor wrote it, in
// the events that stream it. It reports false, writing nothing, for a body
// that isn't such a reply.
func wholeAsStream(w http.ResponseWriter, proto provider.Protocol, b []byte) bool {
	events := wholeEvents(proto, b)
	if events == nil {
		return false
	}
	sw := newSSEWriter(w)
	sw.begun = true // the 200 and its headers went out with the keepalives
	for _, e := range events {
		sw.event(e.name, e.data)
	}
	return true
}

// wholeEvents are the events of proto's stream that say the whole reply
// b, or nil for a body that isn't one.
func wholeEvents(proto provider.Protocol, b []byte) []wholeEvent {
	switch proto {
	case provider.Chat:
		return chatWholeEvents(b)
	case provider.Responses:
		return responsesWholeEvents(b)
	case provider.Anthropic:
		return anthropicWholeEvents(b)
	}
	return nil
}

type wholeEvent struct {
	name string
	data any
}

// obj is a JSON object read with its values as the vendor wrote them.
type obj = map[string]json.RawMessage

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func str(m obj, k string) string {
	var s string
	json.Unmarshal(m[k], &s)
	return s
}

// chatWholeEvents streams a chat.completion as its chunks: each choice's
// message as one delta, its tool calls numbered as a stream numbers them,
// then its finish reason with the usage, then [DONE].
func chatWholeEvents(b []byte) []wholeEvent {
	var v obj
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	var choices []struct {
		Index        json.RawMessage `json:"index"`
		Message      obj             `json:"message"`
		FinishReason json.RawMessage `json:"finish_reason"`
		Logprobs     json.RawMessage `json:"logprobs"`
	}
	if json.Unmarshal(v["choices"], &choices) != nil || len(choices) == 0 {
		return nil
	}
	chunk := func(cs []obj, usage json.RawMessage) obj {
		c := obj{"object": raw("chat.completion.chunk"), "choices": raw(cs)}
		for _, k := range []string{"id", "created", "model", "system_fingerprint", "service_tier"} {
			if x, ok := v[k]; ok {
				c[k] = x
			}
		}
		if usage != nil {
			c["usage"] = usage
		}
		return c
	}
	var deltas, ends []obj
	for i, c := range choices {
		if c.Message == nil {
			return nil
		}
		index := c.Index
		if index == nil {
			index = raw(i)
		}
		delta := maps.Clone(c.Message)
		var calls []obj
		if json.Unmarshal(delta["tool_calls"], &calls) == nil && len(calls) > 0 {
			for n, call := range calls {
				if _, ok := call["index"]; !ok {
					call["index"] = raw(n)
				}
			}
			delta["tool_calls"] = raw(calls)
		}
		d := obj{"index": index, "delta": raw(delta), "finish_reason": raw(nil)}
		if c.Logprobs != nil {
			d["logprobs"] = c.Logprobs
		}
		deltas = append(deltas, d)
		finish := c.FinishReason
		if finish == nil {
			finish = raw(nil)
		}
		ends = append(ends, obj{"index": index, "delta": raw(obj{}), "finish_reason": finish})
	}
	return []wholeEvent{{"", chunk(deltas, nil)}, {"", chunk(ends, v["usage"])}, {"", "[DONE]"}}
}

// responsesWholeEvents streams a Responses response object: created and
// in progress with no output, each output item added, its text (with its
// citations), refusal, reasoning summary or arguments as deltas and their
// ends, the item done
// as the vendor wrote it, then the response completed (or incomplete, or
// failed) whole.
func responsesWholeEvents(b []byte) []wholeEvent {
	var v obj
	if json.Unmarshal(b, &v) != nil || str(v, "object") != "response" {
		return nil
	}
	var output []obj
	if json.Unmarshal(v["output"], &output) != nil {
		return nil
	}
	var events []wholeEvent
	seq := 0
	send := func(typ string, fields obj) {
		fields["type"], fields["sequence_number"] = raw(typ), raw(seq)
		seq++
		events = append(events, wholeEvent{typ, fields})
	}
	begun := maps.Clone(v)
	begun["status"], begun["output"] = raw("in_progress"), raw([]any{})
	for _, k := range []string{"usage", "incomplete_details", "error", "completed_at"} {
		delete(begun, k)
	}
	send("response.created", obj{"response": raw(begun)})
	send("response.in_progress", obj{"response": raw(begun)})
	for i, item := range output {
		at, id := raw(i), item["id"]
		added := maps.Clone(item)
		if _, ok := added["status"]; ok {
			added["status"] = raw("in_progress")
		}
		var parts []obj
		switch str(item, "type") {
		case "message":
			json.Unmarshal(item["content"], &parts)
			added["content"] = raw([]any{})
			send("response.output_item.added", obj{"output_index": at, "item": raw(added)})
			for j, part := range parts {
				ids := func(o obj) obj {
					o["item_id"], o["output_index"], o["content_index"] = id, at, raw(j)
					return o
				}
				switch str(part, "type") {
				case "output_text":
					text := str(part, "text")
					send("response.content_part.added", ids(obj{"part": raw(obj{"type": raw("output_text"), "text": raw(""), "annotations": raw([]any{})})}))
					send("response.output_text.delta", ids(obj{"delta": raw(text), "logprobs": raw([]any{})}))
					// its citations, which a client takes from these events
					// alone (ai-sdk's sources)
					var notes []json.RawMessage
					json.Unmarshal(part["annotations"], &notes)
					for k, a := range notes {
						send("response.output_text.annotation.added", ids(obj{"annotation_index": raw(k), "annotation": a}))
					}
					send("response.output_text.done", ids(obj{"text": raw(text), "logprobs": raw([]any{})}))
				case "refusal":
					refusal := str(part, "refusal")
					send("response.content_part.added", ids(obj{"part": raw(obj{"type": raw("refusal"), "refusal": raw("")})}))
					send("response.refusal.delta", ids(obj{"delta": raw(refusal)}))
					send("response.refusal.done", ids(obj{"refusal": raw(refusal)}))
				default:
					send("response.content_part.added", ids(obj{"part": raw(part)}))
				}
				send("response.content_part.done", ids(obj{"part": raw(part)}))
			}
		case "reasoning":
			json.Unmarshal(item["summary"], &parts)
			added["summary"] = raw([]any{})
			send("response.output_item.added", obj{"output_index": at, "item": raw(added)})
			for j, part := range parts {
				ids := func(o obj) obj {
					o["item_id"], o["output_index"], o["summary_index"] = id, at, raw(j)
					return o
				}
				text := str(part, "text")
				send("response.reasoning_summary_part.added", ids(obj{"part": raw(obj{"type": raw("summary_text"), "text": raw("")})}))
				send("response.reasoning_summary_text.delta", ids(obj{"delta": raw(text)}))
				send("response.reasoning_summary_text.done", ids(obj{"text": raw(text)}))
				send("response.reasoning_summary_part.done", ids(obj{"part": raw(part)}))
			}
		case "function_call":
			if str(item, "id") == "" {
				// with no id, no event can name it as its item: whole
				send("response.output_item.added", obj{"output_index": at, "item": raw(item)})
				break
			}
			args := str(item, "arguments")
			added["arguments"] = raw("")
			send("response.output_item.added", obj{"output_index": at, "item": raw(added)})
			send("response.function_call_arguments.delta", obj{"item_id": id, "output_index": at, "delta": raw(args)})
			send("response.function_call_arguments.done", obj{"item_id": id, "output_index": at, "arguments": raw(args)})
		default:
			// a custom tool's call, a web search, an image…: whole as it is
			send("response.output_item.added", obj{"output_index": at, "item": raw(item)})
		}
		send("response.output_item.done", obj{"output_index": at, "item": raw(item)})
	}
	end := "response.completed"
	switch str(v, "status") {
	case "incomplete":
		end = "response.incomplete"
	case "failed":
		end = "response.failed"
	}
	send(end, obj{"response": raw(v)})
	return events
}

// anthropicWholeEvents streams an Anthropic message: started with no
// content, each block started empty with what it holds as its deltas — a
// text's citations and text, a thinking's thought and signature, a tool
// call's input — or whole for a block that streams none, then the stop
// reason with the usage.
func anthropicWholeEvents(b []byte) []wholeEvent {
	var v obj
	if json.Unmarshal(b, &v) != nil || str(v, "type") != "message" {
		return nil
	}
	var content []obj
	if json.Unmarshal(v["content"], &content) != nil {
		return nil
	}
	var events []wholeEvent
	send := func(typ string, fields obj) {
		fields["type"] = raw(typ)
		events = append(events, wholeEvent{typ, fields})
	}
	start := maps.Clone(v)
	start["content"], start["stop_reason"], start["stop_sequence"] = raw([]any{}), raw(nil), raw(nil)
	var usage obj
	if json.Unmarshal(v["usage"], &usage) == nil && usage != nil {
		// the output is counted as it is said, in message_delta
		u := maps.Clone(usage)
		u["output_tokens"] = raw(0)
		start["usage"] = raw(u)
	}
	send("message_start", obj{"message": raw(start)})
	for i, block := range content {
		at := raw(i)
		delta := func(d obj) { send("content_block_delta", obj{"index": at, "delta": raw(d)}) }
		empty := maps.Clone(block)
		switch str(block, "type") {
		case "text":
			var cites []json.RawMessage
			json.Unmarshal(block["citations"], &cites)
			empty["text"] = raw("")
			if _, ok := block["citations"]; ok {
				empty["citations"] = raw([]any{})
			}
			send("content_block_start", obj{"index": at, "content_block": raw(empty)})
			for _, c := range cites {
				delta(obj{"type": raw("citations_delta"), "citation": c})
			}
			delta(obj{"type": raw("text_delta"), "text": raw(str(block, "text"))})
		case "thinking":
			empty["thinking"], empty["signature"] = raw(""), raw("")
			send("content_block_start", obj{"index": at, "content_block": raw(empty)})
			delta(obj{"type": raw("thinking_delta"), "thinking": raw(str(block, "thinking"))})
			if sig := str(block, "signature"); sig != "" {
				delta(obj{"type": raw("signature_delta"), "signature": raw(sig)})
			}
		case "tool_use", "server_tool_use", "mcp_tool_use":
			input := block["input"]
			if input == nil {
				input = raw(obj{})
			}
			empty["input"] = raw(obj{})
			send("content_block_start", obj{"index": at, "content_block": raw(empty)})
			delta(obj{"type": raw("input_json_delta"), "partial_json": raw(string(input))})
		default:
			// redacted thinking, a search's results…: whole as it is
			send("content_block_start", obj{"index": at, "content_block": raw(block)})
		}
		send("content_block_stop", obj{"index": at})
	}
	// the stop's details too (a refusal's category), which a client takes
	// from this event, null or not, over message_start's
	stop := obj{"stop_reason": orNull(v["stop_reason"]), "stop_sequence": orNull(v["stop_sequence"]), "stop_details": orNull(v["stop_details"])}
	if c, ok := v["container"]; ok {
		stop["container"] = c
	}
	end := obj{"delta": raw(stop)}
	if usage != nil {
		end["usage"] = v["usage"]
	}
	send("message_delta", end)
	send("message_stop", obj{})
	return events
}

func orNull(r json.RawMessage) json.RawMessage {
	if r == nil {
		return raw(nil)
	}
	return r
}
