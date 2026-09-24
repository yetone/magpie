package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ---- OpenAI Responses -------------------------------------------------------

type rItem struct {
	Type    string          `json:"type,omitempty"`
	Role    string          `json:"role,omitempty"`
	Content json.RawMessage `json:"content,omitempty"`
	// function_call / function_call_output
	ID        string          `json:"id,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
	Status    string          `json:"status,omitempty"`
	// reasoning
	Summary          []rText `json:"summary,omitempty"`
	EncryptedContent string  `json:"encrypted_content,omitempty"`
}

type rText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type rRequest struct {
	Model        string          `json:"model"`
	Instructions string          `json:"instructions,omitempty"`
	Input        json.RawMessage `json:"input"`
	Tools        []struct {
		Type        string          `json:"type"`
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"tools,omitempty"`
	ToolChoice        json.RawMessage `json:"tool_choice,omitempty"`
	MaxOutputTokens   int             `json:"max_output_tokens,omitempty"`
	Temperature       *float64        `json:"temperature,omitempty"`
	TopP              *float64        `json:"top_p,omitempty"`
	Stream            bool            `json:"stream,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	Reasoning         *struct {
		Effort  string `json:"effort,omitempty"`
		Summary string `json:"summary,omitempty"`
	} `json:"reasoning,omitempty"`
}

func parseResponses(body []byte) (*Request, error) {
	var q rRequest
	if err := json.Unmarshal(body, &q); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Model: q.Model, System: q.Instructions, MaxTokens: q.MaxOutputTokens, Temp: q.Temperature,
		TopP: q.TopP, Stream: q.Stream, Parallel: q.ParallelToolCalls}
	if q.Reasoning != nil {
		r.Effort = effortOf(q.Reasoning.Effort)
		r.Thinking = true
	}
	var s string
	if json.Unmarshal(q.Input, &s) == nil {
		r.Messages = append(r.Messages, Message{Role: "user", Parts: []Part{{Kind: Text, Text: s}}})
	} else {
		var items []rItem
		if err := json.Unmarshal(q.Input, &items); err != nil {
			return nil, fmt.Errorf("invalid input: %v", err)
		}
		for _, it := range items {
			switch {
			case it.Type == "message" || (it.Type == "" && it.Role != ""):
				role := "user"
				if it.Role == "assistant" {
					role = "assistant"
				}
				parts := responsesParts(it.Content)
				if it.Role == "system" || it.Role == "developer" {
					if t := text(parts); t != "" {
						if r.System != "" {
							r.System += "\n\n"
						}
						r.System += t
					}
					continue
				}
				r.Messages = append(r.Messages, Message{Role: role, Parts: parts})
			case it.Type == "function_call":
				r.Messages = append(r.Messages, Message{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: it.CallID, Name: it.Name, Args: parseArgs(it.Arguments)}}})
			case it.Type == "function_call_output":
				r.Messages = append(r.Messages, Message{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: it.CallID, Text: stringOrText(it.Output)}}})
			case it.Type == "reasoning":
				var b strings.Builder
				for _, s := range it.Summary {
					b.WriteString(s.Text)
				}
				if b.Len() > 0 {
					r.Messages = append(r.Messages, Message{Role: "assistant", Parts: []Part{{Kind: Thinking, Text: b.String()}}})
				}
			}
		}
	}
	r.Messages = mergeTurns(r.Messages)
	for _, t := range q.Tools {
		if t.Type != "function" {
			continue
		}
		r.Tools = append(r.Tools, Tool{Name: t.Name, Description: t.Description, Schema: t.Parameters})
	}
	var tc string
	if json.Unmarshal(q.ToolChoice, &tc) == nil {
		r.ToolChoice = tc
	} else {
		var o struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if json.Unmarshal(q.ToolChoice, &o) == nil && o.Name != "" {
			r.ToolChoice = "name:" + o.Name
		}
	}
	return r, nil
}

// mergeTurns joins consecutive messages of the same role, since the
// Responses API splits an assistant turn into one item per part.
func mergeTurns(msgs []Message) []Message {
	var out []Message
	for _, m := range msgs {
		if n := len(out); n > 0 && out[n-1].Role == m.Role {
			out[n-1].Parts = append(out[n-1].Parts, m.Parts...)
			continue
		}
		out = append(out, m)
	}
	return out
}

func responsesParts(raw json.RawMessage) []Part {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil
		}
		return []Part{{Kind: Text, Text: s}}
	}
	var items []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL string `json:"image_url"`
	}
	json.Unmarshal(raw, &items)
	var out []Part
	for _, it := range items {
		switch it.Type {
		case "input_text", "output_text", "text":
			out = append(out, Part{Kind: Text, Text: it.Text})
		case "input_image":
			if it.ImageURL != "" {
				out = append(out, imagePart(it.ImageURL))
			}
		}
	}
	return out
}

// buildResponses renders a request for a Responses upstream.
func buildResponses(r *Request, model string, rejectTemp bool) []byte {
	var input []map[string]any
	for _, m := range r.Messages {
		var content []map[string]any
		flushMsg := func() {
			if len(content) == 0 {
				return
			}
			input = append(input, map[string]any{"type": "message", "role": m.Role, "content": content})
			content = nil
		}
		for _, p := range m.Parts {
			switch p.Kind {
			case Text:
				t := "input_text"
				if m.Role == "assistant" {
					t = "output_text"
				}
				content = append(content, map[string]any{"type": t, "text": p.Text})
			case Image:
				if m.Role != "assistant" {
					content = append(content, map[string]any{"type": "input_image", "image_url": dataURL(p)})
				}
			case ToolCall:
				flushMsg()
				id := p.ID
				if id == "" {
					id = "call_" + newID()
				}
				input = append(input, map[string]any{"type": "function_call", "call_id": id, "name": p.Name, "arguments": argsString(p)})
			case ToolResult:
				flushMsg()
				input = append(input, map[string]any{"type": "function_call_output", "call_id": p.CallID, "output": p.Text})
			}
		}
		flushMsg()
	}
	if input == nil {
		input = []map[string]any{}
	}
	out := map[string]any{"model": model, "input": input, "stream": r.Stream, "store": false}
	if r.System != "" {
		out["instructions"] = r.System
	}
	if r.MaxTokens > 0 {
		out["max_output_tokens"] = r.MaxTokens
	}
	if !rejectTemp {
		if r.Temp != nil {
			out["temperature"] = *r.Temp
		}
		if r.TopP != nil {
			out["top_p"] = *r.TopP
		}
	}
	if r.Effort != "" {
		out["reasoning"] = map[string]any{"effort": r.Effort, "summary": "auto"}
	} else if r.Thinking {
		out["reasoning"] = map[string]any{"summary": "auto"}
	}
	if len(r.Tools) > 0 {
		var tools []map[string]any
		for _, t := range r.Tools {
			tool := map[string]any{"type": "function", "name": t.Name, "description": t.Description}
			if len(t.Schema) > 0 {
				tool["parameters"] = t.Schema
			}
			tools = append(tools, tool)
		}
		out["tools"] = tools
		switch {
		case r.ToolChoice == "auto" || r.ToolChoice == "none" || r.ToolChoice == "required":
			out["tool_choice"] = r.ToolChoice
		case strings.HasPrefix(r.ToolChoice, "name:"):
			out["tool_choice"] = map[string]any{"type": "function", "name": strings.TrimPrefix(r.ToolChoice, "name:")}
		}
		if r.Parallel != nil {
			out["parallel_tool_calls"] = *r.Parallel
		}
	}
	b, _ := json.Marshal(out)
	return b
}

type rUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func (u rUsage) usage() Usage {
	return Usage{Input: u.InputTokens - u.InputTokensDetails.CachedTokens, Output: u.OutputTokens,
		CacheRead: u.InputTokensDetails.CachedTokens, Reasoning: u.OutputTokensDetails.ReasoningTokens}
}

func (u Usage) responses() map[string]any {
	in := u.Input + u.CacheRead
	return map[string]any{"input_tokens": in, "output_tokens": u.Output, "total_tokens": in + u.Output,
		"input_tokens_details":  map[string]any{"cached_tokens": u.CacheRead},
		"output_tokens_details": map[string]any{"reasoning_tokens": u.Reasoning}}
}

// responsesDecoder turns a Responses stream into events.
type responsesDecoder struct {
	started  bool
	argsSeen bool // arguments of the open function call arrived as deltas
}

func (d *responsesDecoder) decode(data string, emit func(Event)) error {
	var ev struct {
		Type     string `json:"type"`
		Delta    string `json:"delta"`
		Item     rItem  `json:"item"`
		Response struct {
			ID                string `json:"id"`
			Model             string `json:"model"`
			Status            string `json:"status"`
			Usage             rUsage `json:"usage"`
			IncompleteDetails *struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Output []rItem `json:"output"`
		} `json:"response"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return nil
	}
	switch ev.Type {
	case "response.created":
		if !d.started {
			d.started = true
			emit(Event{Kind: KStart, MsgID: ev.Response.ID, Model: ev.Response.Model})
		}
	case "response.output_item.added":
		if ev.Item.Type == "function_call" {
			d.argsSeen = false
			emit(Event{Kind: KToolStart, ID: ev.Item.CallID, Name: ev.Item.Name})
		}
	case "response.output_text.delta":
		emit(Event{Kind: KText, Text: ev.Delta})
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		emit(Event{Kind: KThink, Text: ev.Delta})
	case "response.function_call_arguments.delta":
		d.argsSeen = true
		emit(Event{Kind: KToolArgs, Text: ev.Delta})
	case "response.output_item.done":
		if ev.Item.Type == "function_call" && !d.argsSeen && ev.Item.Arguments != "" {
			emit(Event{Kind: KToolArgs, Text: ev.Item.Arguments})
		}
	case "response.completed", "response.incomplete", "response.failed":
		if ev.Response.Error != nil {
			emit(Event{Kind: KError, Text: ev.Response.Error.Message})
			return nil
		}
		stop := "stop"
		if ev.Response.Status == "incomplete" {
			stop = "length"
			if ev.Response.IncompleteDetails != nil && ev.Response.IncompleteDetails.Reason == "content_filter" {
				stop = "filter"
			}
		} else {
			for _, it := range ev.Response.Output {
				if it.Type == "function_call" {
					stop = "tool"
				}
			}
		}
		emit(Event{Kind: KStop, Stop: stop})
		emit(Event{Kind: KUsage, Usage: ev.Response.Usage.usage()})
	case "error":
		msg := ev.Message
		if ev.Error != nil {
			msg = ev.Error.Message
		}
		emit(Event{Kind: KError, Text: msg})
	}
	return nil
}

// responsesEncoder writes events as a Responses stream. Codex reads the
// full item from output_item.done and usage from response.completed.
type responsesEncoder struct {
	w       *sseWriter
	model   string
	id      string
	created int64
	seq     int
	started bool
	item    int             // index of the open output item
	open    Kind            // kind of the open item
	itemID  string          // id of the open item
	text    strings.Builder // text of the open message / reasoning
	output  []map[string]any
	col     collector
}

func (e *responsesEncoder) send(typ string, fields map[string]any) {
	fields["type"] = typ
	fields["sequence_number"] = e.seq
	e.seq++
	e.w.event(typ, fields)
}

func (e *responsesEncoder) response(status string, extra map[string]any) map[string]any {
	out := map[string]any{"id": e.id, "object": "response", "created_at": e.created, "status": status,
		"model": e.model, "output": e.output, "parallel_tool_calls": true, "tool_choice": "auto", "tools": []any{}}
	if out["output"] == nil {
		out["output"] = []any{}
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func (e *responsesEncoder) start(ev Event) {
	if e.started {
		return
	}
	e.started, e.item = true, -1
	e.id, e.created = ev.MsgID, time.Now().Unix()
	if e.id == "" {
		e.id = newID()
	}
	if !strings.HasPrefix(e.id, "resp_") {
		e.id = "resp_" + e.id
	}
	if ev.Model != "" {
		e.model = ev.Model
	}
	e.send("response.created", map[string]any{"response": e.response("in_progress", nil)})
	e.send("response.in_progress", map[string]any{"response": e.response("in_progress", nil)})
}

func (e *responsesEncoder) closeItem() {
	if e.open == "" {
		return
	}
	var item map[string]any
	switch e.open {
	case Text:
		t := e.text.String()
		e.send("response.output_text.done", map[string]any{"item_id": e.itemID, "output_index": e.item, "content_index": 0, "text": t, "logprobs": []any{}})
		part := map[string]any{"type": "output_text", "text": t, "annotations": []any{}, "logprobs": []any{}}
		e.send("response.content_part.done", map[string]any{"item_id": e.itemID, "output_index": e.item, "content_index": 0, "part": part})
		item = map[string]any{"id": e.itemID, "type": "message", "role": "assistant", "status": "completed", "content": []map[string]any{part}}
	case Thinking:
		t := e.text.String()
		e.send("response.reasoning_summary_text.done", map[string]any{"item_id": e.itemID, "output_index": e.item, "summary_index": 0, "text": t})
		part := map[string]any{"type": "summary_text", "text": t}
		e.send("response.reasoning_summary_part.done", map[string]any{"item_id": e.itemID, "output_index": e.item, "summary_index": 0, "part": part})
		item = map[string]any{"id": e.itemID, "type": "reasoning", "status": "completed", "summary": []map[string]any{part}}
	case ToolCall:
		args := strings.TrimSpace(e.text.String())
		if args == "" {
			args = "{}"
		}
		p := e.col.last(ToolCall)
		e.send("response.function_call_arguments.done", map[string]any{"item_id": e.itemID, "output_index": e.item, "call_id": p.ID, "name": p.Name, "arguments": args})
		item = map[string]any{"id": e.itemID, "type": "function_call", "status": "completed", "call_id": p.ID, "name": p.Name, "arguments": args}
	}
	e.send("response.output_item.done", map[string]any{"output_index": e.item, "item": item})
	e.output = append(e.output, item)
	e.open, e.text = "", strings.Builder{}
}

func (e *responsesEncoder) openItem(k Kind, prefix string, item map[string]any) {
	e.closeItem()
	e.item++
	e.open, e.itemID = k, prefix+newID()
	item["id"] = e.itemID
	item["status"] = "in_progress"
	e.send("response.output_item.added", map[string]any{"output_index": e.item, "item": item})
}

func (e *responsesEncoder) event(ev Event) {
	if ev.Kind != KStart && !e.started {
		e.start(Event{})
	}
	switch ev.Kind {
	case KStart:
		e.start(ev)
	case KText:
		if ev.Text == "" {
			return
		}
		if e.open != Text {
			e.openItem(Text, "msg_", map[string]any{"type": "message", "role": "assistant", "content": []any{}})
			e.send("response.content_part.added", map[string]any{"item_id": e.itemID, "output_index": e.item, "content_index": 0,
				"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}, "logprobs": []any{}}})
		}
		e.text.WriteString(ev.Text)
		e.send("response.output_text.delta", map[string]any{"item_id": e.itemID, "output_index": e.item, "content_index": 0, "delta": ev.Text, "logprobs": []any{}})
	case KThink:
		if ev.Text == "" {
			return
		}
		if e.open != Thinking {
			e.openItem(Thinking, "rs_", map[string]any{"type": "reasoning", "summary": []any{}})
			e.send("response.reasoning_summary_part.added", map[string]any{"item_id": e.itemID, "output_index": e.item, "summary_index": 0,
				"part": map[string]any{"type": "summary_text", "text": ""}})
		}
		e.text.WriteString(ev.Text)
		e.send("response.reasoning_summary_text.delta", map[string]any{"item_id": e.itemID, "output_index": e.item, "summary_index": 0, "delta": ev.Text})
	case KToolStart:
		if ev.ID == "" {
			ev.ID = "call_" + newID()
		}
		// Open (and so close the previous item) before recording this call:
		// closeItem reads the call ID from e.col.last(ToolCall).
		e.openItem(ToolCall, "fc_", map[string]any{"type": "function_call", "call_id": ev.ID, "name": ev.Name, "arguments": ""})
		e.col.add(ev)
		return
	case KToolArgs:
		if e.open == ToolCall && ev.Text != "" {
			e.text.WriteString(ev.Text)
			e.send("response.function_call_arguments.delta", map[string]any{"item_id": e.itemID, "output_index": e.item, "delta": ev.Text})
		}
	case KError:
		e.closeItem()
		e.send("response.failed", map[string]any{"response": e.response("failed", map[string]any{"error": map[string]any{"code": "server_error", "message": ev.Text}})})
	}
	e.col.add(ev)
}

func (e *responsesEncoder) finish() {
	if !e.started {
		e.start(Event{})
	}
	e.closeItem()
	res := e.col.finish()
	status, typ := "completed", "response.completed"
	extra := map[string]any{"usage": res.Usage.responses(), "incomplete_details": nil}
	if res.Stop == "length" || res.Stop == "filter" {
		status, typ = "incomplete", "response.incomplete"
		reason := "max_output_tokens"
		if res.Stop == "filter" {
			reason = "content_filter"
		}
		extra["incomplete_details"] = map[string]any{"reason": reason}
	}
	e.send(typ, map[string]any{"response": e.response(status, extra)})
}

// renderResponses is the non-streaming reply.
func renderResponses(res Result, model string) []byte {
	output := []map[string]any{}
	for _, p := range res.Parts {
		switch p.Kind {
		case Text:
			output = append(output, map[string]any{"id": "msg_" + newID(), "type": "message", "role": "assistant", "status": "completed",
				"content": []map[string]any{{"type": "output_text", "text": p.Text, "annotations": []any{}}}})
		case Thinking:
			output = append(output, map[string]any{"id": "rs_" + newID(), "type": "reasoning", "status": "completed",
				"summary": []map[string]any{{"type": "summary_text", "text": p.Text}}})
		case ToolCall:
			id := p.ID
			if id == "" {
				id = "call_" + newID()
			}
			output = append(output, map[string]any{"id": "fc_" + newID(), "type": "function_call", "status": "completed",
				"call_id": id, "name": p.Name, "arguments": argsString(p)})
		}
	}
	id := res.ID
	if id == "" {
		id = newID()
	}
	if !strings.HasPrefix(id, "resp_") {
		id = "resp_" + id
	}
	if res.Model != "" {
		model = res.Model
	}
	status := "completed"
	var incomplete any
	if res.Stop == "length" {
		status, incomplete = "incomplete", map[string]any{"reason": "max_output_tokens"}
	}
	b, _ := json.Marshal(map[string]any{"id": id, "object": "response", "created_at": time.Now().Unix(), "status": status,
		"model": model, "output": output, "usage": res.Usage.responses(), "incomplete_details": incomplete,
		"parallel_tool_calls": true, "tool_choice": "auto", "tools": []any{}, "error": nil})
	return b
}
