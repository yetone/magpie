package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ---- OpenAI Chat Completions --------------------------------------------------

type cToolCall struct {
	Index    *int   `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type cRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role             string          `json:"role"`
		Content          json.RawMessage `json:"content"`
		ReasoningContent string          `json:"reasoning_content,omitempty"`
		ToolCalls        []cToolCall     `json:"tool_calls,omitempty"`
		ToolCallID       string          `json:"tool_call_id,omitempty"`
	} `json:"messages"`
	Tools []struct {
		Type     string `json:"type"`
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description,omitempty"`
			Parameters  json.RawMessage `json:"parameters,omitempty"`
		} `json:"function"`
	} `json:"tools,omitempty"`
	ToolChoice          json.RawMessage `json:"tool_choice,omitempty"`
	WebSearchOptions    json.RawMessage `json:"web_search_options,omitempty"`
	MaxTokens           int             `json:"max_tokens,omitempty"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	TopP                *float64        `json:"top_p,omitempty"`
	Stop                json.RawMessage `json:"stop,omitempty"`
	Stream              bool            `json:"stream,omitempty"`
	ReasoningEffort     string          `json:"reasoning_effort,omitempty"`
	ParallelToolCalls   *bool           `json:"parallel_tool_calls,omitempty"`
	ServiceTier         string          `json:"service_tier,omitempty"`
	PromptCacheKey      string          `json:"prompt_cache_key,omitempty"`
}

func parseChat(body []byte) (*Request, error) {
	var c cRequest
	if err := json.Unmarshal(body, &c); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Model: c.Model, MaxTokens: c.MaxCompletionTokens, Temp: c.Temperature, TopP: c.TopP,
		Stream: c.Stream, Effort: effortOf(c.ReasoningEffort), Parallel: c.ParallelToolCalls, Fast: c.ServiceTier == "priority",
		CacheKey: c.PromptCacheKey}
	if r.MaxTokens == 0 {
		r.MaxTokens = c.MaxTokens
	}
	if r.Effort != "" {
		r.Thinking = true
	}
	var stop string
	if json.Unmarshal(c.Stop, &stop) == nil && stop != "" {
		r.Stop = []string{stop}
	} else {
		json.Unmarshal(c.Stop, &r.Stop)
	}
	var sys []string
	for _, m := range c.Messages {
		switch m.Role {
		case "system", "developer":
			sys = append(sys, stringOrText(m.Content))
		case "user":
			r.Messages = append(r.Messages, Message{Role: "user", Parts: chatParts(m.Content)})
		case "assistant":
			msg := Message{Role: "assistant"}
			if m.ReasoningContent != "" {
				msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: m.ReasoningContent})
			}
			msg.Parts = append(msg.Parts, chatParts(m.Content)...)
			for _, tc := range m.ToolCalls {
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: tc.ID, Name: tc.Function.Name, Args: parseArgs(tc.Function.Arguments)})
			}
			r.Messages = append(r.Messages, msg)
		case "tool":
			out, images := toolOutput(m.Content)
			r.Messages = append(r.Messages, Message{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: m.ToolCallID, Text: out, Images: images}}})
		}
	}
	r.System = strings.Join(sys, "\n\n")
	r.WebSearch = len(c.WebSearchOptions) > 0 && string(c.WebSearchOptions) != "null"
	for _, t := range c.Tools {
		if strings.HasPrefix(t.Type, "web_search") {
			r.WebSearch = true
		}
		if t.Type != "" && t.Type != "function" {
			continue
		}
		r.Tools = append(r.Tools, Tool{Name: t.Function.Name, Description: t.Function.Description, Schema: t.Function.Parameters})
	}
	var tc string
	if json.Unmarshal(c.ToolChoice, &tc) == nil {
		r.ToolChoice = tc
	} else {
		var o struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if json.Unmarshal(c.ToolChoice, &o) == nil && o.Function.Name != "" {
			r.ToolChoice = "name:" + o.Function.Name
		}
	}
	return r, nil
}

// chatParts reads a message's content: a string, or an array of parts.
func chatParts(raw json.RawMessage) []Part {
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
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	json.Unmarshal(raw, &items)
	var out []Part
	for _, it := range items {
		switch it.Type {
		case "text":
			out = append(out, Part{Kind: Text, Text: it.Text})
		case "image_url":
			out = append(out, imagePart(it.ImageURL.URL))
		}
	}
	return out
}

// imagePart reads a data: URL into an inline image, or keeps the URL.
func imagePart(u string) Part {
	if strings.HasPrefix(u, "data:") {
		if meta, data, ok := strings.Cut(strings.TrimPrefix(u, "data:"), ","); ok {
			mt := strings.TrimSuffix(meta, ";base64")
			return Part{Kind: Image, MediaType: mt, Data: data}
		}
	}
	return Part{Kind: Image, URL: u}
}

func dataURL(p Part) string {
	if p.URL != "" && p.Data == "" {
		return p.URL
	}
	return "data:" + p.MediaType + ";base64," + p.Data
}

// buildChat renders a request for a Chat Completions upstream.
func buildChat(r *Request, model, host string, rejectTemp bool) []byte {
	var msgs []map[string]any
	if r.System != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": r.System})
	}
	deepseek := strings.Contains(host, "deepseek")
	// A tool message holds text only, so the images tools returned go to
	// the model in a user message after the tool messages, as the start of
	// the user's own message when one comes next: some models' chat
	// templates turn away two user messages in a row.
	names := map[string]string{}
	var seen []map[string]any
	seeLater := func(p Part) {
		if len(p.Images) == 0 {
			return
		}
		of := "tool call " + p.CallID
		if name := names[p.CallID]; name != "" {
			of = name + " (" + of + ")"
		} else if p.Name != "" {
			of = p.Name + " (" + of + ")"
		}
		seen = append(seen, map[string]any{"type": "text", "text": "[From the result of " + of + ":]"})
		for _, im := range p.Images {
			seen = append(seen, map[string]any{"type": "image_url", "image_url": map[string]any{"url": dataURL(im)}})
		}
	}
	showSeen := func() {
		if len(seen) > 0 {
			msgs = append(msgs, map[string]any{"role": "user", "content": seen})
			seen = nil
		}
	}
	for _, m := range r.Messages {
		if m.Role == "assistant" {
			showSeen()
			am := map[string]any{"role": "assistant"}
			var calls []map[string]any
			var think string
			for _, p := range m.Parts {
				switch p.Kind {
				case ToolCall:
					id := p.ID
					if id == "" {
						id = "call_" + newID()
					}
					names[id] = p.Name
					calls = append(calls, map[string]any{"id": id, "type": "function",
						"function": map[string]any{"name": p.Name, "arguments": argsString(p)}})
				case Thinking:
					think += p.Text
				}
			}
			t := text(m.Parts)
			if t != "" || len(calls) == 0 {
				am["content"] = t
			}
			if len(calls) > 0 {
				am["tool_calls"] = calls
			}
			if deepseek && think != "" {
				am["reasoning_content"] = think
			}
			msgs = append(msgs, am)
			continue
		}
		var content []map[string]any
		plain := true
		flush := func() {
			if len(content) == 0 {
				return
			}
			if len(seen) > 0 {
				content, plain = append(seen, content...), false
				seen = nil
			}
			if plain {
				var b strings.Builder
				for _, c := range content {
					b.WriteString(c["text"].(string))
				}
				msgs = append(msgs, map[string]any{"role": "user", "content": b.String()})
			} else {
				msgs = append(msgs, map[string]any{"role": "user", "content": content})
			}
			content, plain = nil, true
		}
		for _, p := range m.Parts {
			switch p.Kind {
			case Text:
				content = append(content, map[string]any{"type": "text", "text": p.Text})
			case File:
				content = append(content, map[string]any{"type": "text", "text": attachmentText(p)})
			case Image:
				plain = false
				content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{"url": dataURL(p)}})
			case ToolResult:
				flush()
				out := p.Text
				if n := len(p.Images); n > 0 {
					note := fmt.Sprintf("[The tool returned %d images; they follow in the next message.]", n)
					if n == 1 {
						note = "[The tool returned an image; it follows in the next message.]"
					}
					if strings.TrimSpace(out) != "" {
						out += "\n\n"
					}
					out += note
				}
				msgs = append(msgs, map[string]any{"role": "tool", "tool_call_id": p.CallID, "content": out})
				seeLater(p)
			}
		}
		flush()
	}
	showSeen()
	out := map[string]any{"model": model, "messages": msgs, "stream": r.Stream}
	if r.CacheKey != "" {
		out["prompt_cache_key"] = r.CacheKey
	}
	if r.Stream {
		out["stream_options"] = map[string]any{"include_usage": true}
	}
	if r.MaxTokens > 0 {
		if strings.HasSuffix(host, "openai.com") {
			out["max_completion_tokens"] = r.MaxTokens
		} else {
			out["max_tokens"] = r.MaxTokens
		}
	}
	if !rejectTemp {
		if r.Temp != nil {
			out["temperature"] = *r.Temp
		}
		if r.TopP != nil {
			out["top_p"] = *r.TopP
		}
	}
	if len(r.Stop) > 0 {
		out["stop"] = r.Stop
	}
	if r.Effort != "" {
		out["reasoning_effort"] = r.Effort
	}
	if len(r.Tools) > 0 {
		var tools []map[string]any
		for _, t := range r.Tools {
			fn := map[string]any{"name": t.Name, "description": t.Description}
			if len(t.Schema) > 0 {
				fn["parameters"] = t.Schema
			}
			tools = append(tools, map[string]any{"type": "function", "function": fn})
		}
		out["tools"] = tools
		switch {
		case r.ToolChoice == "auto" || r.ToolChoice == "none" || r.ToolChoice == "required":
			out["tool_choice"] = r.ToolChoice
		case strings.HasPrefix(r.ToolChoice, "name:"):
			out["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": strings.TrimPrefix(r.ToolChoice, "name:")}}
		}
		if r.Parallel != nil {
			out["parallel_tool_calls"] = *r.Parallel
		}
	}
	if r.WebSearch && host == "openrouter.ai" {
		// OpenRouter's own search, for any of its models
		out["plugins"] = []map[string]any{{"id": "web"}}
	}
	b, _ := json.Marshal(out)
	return b
}

type cUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"` // OpenRouter's
	} `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details,omitempty"`
	// what others say was read from their cache, when prompt_tokens_details
	// doesn't: DeepSeek's hits, Moonshot's cached_tokens, and Anthropic's
	// own names from relays in front of Claude
	PromptCacheHitTokens     int `json:"prompt_cache_hit_tokens"`
	CachedTokens             int `json:"cached_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

func (u cUsage) usage() Usage {
	out := Usage{Output: u.CompletionTokens}
	if d := u.PromptTokensDetails; d != nil {
		out.CacheRead, out.CacheWrite = d.CachedTokens, d.CacheWriteTokens
	}
	for _, n := range []int{u.PromptCacheHitTokens, u.CachedTokens} {
		if out.CacheRead == 0 {
			out.CacheRead = n
		}
	}
	whole := true // prompt_tokens counts what was cached too, as OpenAI's does
	if out.CacheRead == 0 && out.CacheWrite == 0 {
		out.CacheRead, out.CacheWrite = u.CacheReadInputTokens, u.CacheCreationInputTokens
		// Anthropic's names, and less than they come to: counted as
		// Anthropic counts, only what was neither read nor written
		whole = u.PromptTokens >= out.CacheRead+out.CacheWrite
	}
	out.Input = u.PromptTokens
	if whole {
		out.Input = max(u.PromptTokens-out.CacheRead-out.CacheWrite, 0)
	}
	if u.CompletionTokensDetails != nil {
		out.Reasoning = u.CompletionTokensDetails.ReasoningTokens
	}
	return out
}

func (u Usage) chat() map[string]any {
	in := u.prompt()
	return map[string]any{"prompt_tokens": in, "completion_tokens": u.Output, "total_tokens": in + u.Output,
		"prompt_tokens_details":     map[string]any{"cached_tokens": u.CacheRead},
		"completion_tokens_details": map[string]any{"reasoning_tokens": u.Reasoning}}
}

// chatDecoder turns a Chat Completions stream into events. Tool calls come
// as indexed fragments; it tracks which one is open.
type chatDecoder struct {
	started bool
	tool    int    // index of the open tool call, -1 for none
	choice  string // index of the first choice seen; an empty string means none yet
}

func (d *chatDecoder) decode(data string, emit func(Event)) error {
	if strings.TrimSpace(data) == "[DONE]" {
		return nil
	}
	var ch struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Index json.RawMessage `json:"index"`
			Delta struct {
				Content          *string     `json:"content"`
				ReasoningContent string      `json:"reasoning_content"`
				Reasoning        string      `json:"reasoning"`
				ToolCalls        []cToolCall `json:"tool_calls"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *cUsage `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(data), &ch); err != nil {
		return nil
	}
	if ch.Error != nil {
		emit(Event{Kind: KError, Text: ch.Error.Message})
		return nil
	}
	if !d.started {
		d.started, d.tool = true, -1
		emit(Event{Kind: KStart, MsgID: ch.ID, Model: ch.Model})
	}
	for _, c := range ch.Choices {
		// Translation has one reply: lock onto the first readable choice
		// index. A missing, null, or malformed index says nothing about
		// which choice this chunk belongs to, so it cannot select or reject
		// one. Passthrough still relays every choice unchanged.
		if index, ok := chatChoiceIndex(c.Index); ok {
			if d.choice == "" {
				d.choice = index
			} else if index != d.choice {
				continue
			}
		}
		// Some relays send the same thought under both names; one is enough.
		t := c.Delta.ReasoningContent
		if t == "" {
			t = c.Delta.Reasoning
		}
		if t != "" {
			emit(Event{Kind: KThink, Text: t})
		}
		if c.Delta.Content != nil && *c.Delta.Content != "" {
			emit(Event{Kind: KText, Text: *c.Delta.Content})
		}
		for i, tc := range c.Delta.ToolCalls {
			idx := i
			if tc.Index != nil {
				idx = *tc.Index
			}
			if tc.ID != "" || tc.Function.Name != "" || idx != d.tool {
				if idx != d.tool || tc.ID != "" {
					d.tool = idx
					emit(Event{Kind: KToolStart, ID: tc.ID, Name: tc.Function.Name})
				}
			}
			if tc.Function.Arguments != "" {
				emit(Event{Kind: KToolArgs, Text: tc.Function.Arguments})
			}
		}
		if c.FinishReason != "" {
			emit(Event{Kind: KStop, Stop: stopFromChat(c.FinishReason)})
		}
	}
	if ch.Usage != nil && (ch.Usage.PromptTokens > 0 || ch.Usage.CompletionTokens > 0) {
		emit(Event{Kind: KUsage, Usage: ch.Usage.usage()})
	}
	return nil
}

// chatChoiceIndex accepts numeric indexes and numeric strings without
// changing how a missing or unreadable index is handled. JSON numbers such
// as 1.0 and 1 are the same choice.
func chatChoiceIndex(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		raw = json.RawMessage(text)
	}
	var n json.Number
	if json.Unmarshal(raw, &n) != nil {
		return "", false
	}
	index, err := n.Int64()
	if err != nil {
		var f float64
		if json.Unmarshal(raw, &f) != nil || f < 0 || f != float64(int64(f)) {
			return "", false
		}
		index = int64(f)
	}
	if index < 0 {
		return "", false
	}
	return fmt.Sprint(index), true
}

func stopFromChat(s string) string {
	switch s {
	case "length":
		return "length"
	case "tool_calls", "function_call":
		return "tool"
	case "content_filter":
		return "filter"
	}
	return "stop"
}

func stopToChat(s string) string {
	switch s {
	case "length":
		return "length"
	case "tool":
		return "tool_calls"
	case "filter":
		return "content_filter"
	}
	return "stop"
}

// chatEncoder writes events as a Chat Completions stream.
type chatEncoder struct {
	w       *sseWriter
	model   string
	id      string
	created int64
	started bool
	tool    int
	col     collector
}

func (e *chatEncoder) chunk(delta map[string]any, finish any, usage any) {
	choices := []map[string]any{}
	if delta != nil || finish != nil {
		if delta == nil {
			delta = map[string]any{}
		}
		choices = append(choices, map[string]any{"index": 0, "delta": delta, "finish_reason": finish})
	}
	e.w.event("", map[string]any{"id": e.id, "object": "chat.completion.chunk", "created": e.created,
		"model": e.model, "choices": choices, "usage": usage})
}

func (e *chatEncoder) start(ev Event) {
	if e.started {
		return
	}
	e.started, e.tool = true, -1
	e.id, e.created = ev.MsgID, time.Now().Unix()
	if e.id == "" {
		e.id = "chatcmpl-" + newID()
	}
	if !strings.HasPrefix(e.id, "chatcmpl-") {
		e.id = "chatcmpl-" + e.id
	}
	if ev.Model != "" {
		e.model = ev.Model
	}
	e.chunk(map[string]any{"role": "assistant", "content": ""}, nil, nil)
}

func (e *chatEncoder) event(ev Event) {
	if ev.Kind != KStart && !e.started {
		e.start(Event{})
	}
	switch ev.Kind {
	case KStart:
		e.start(ev)
	case KText:
		if ev.Text != "" {
			e.chunk(map[string]any{"content": ev.Text}, nil, nil)
		}
	case KThink:
		if ev.Text != "" {
			e.chunk(map[string]any{"reasoning_content": ev.Text}, nil, nil)
		}
	case KToolStart:
		e.tool++
		id := ev.ID
		if id == "" {
			id = "call_" + newID()
		}
		e.chunk(map[string]any{"tool_calls": []map[string]any{{"index": e.tool, "id": id, "type": "function",
			"function": map[string]any{"name": ev.Name, "arguments": ""}}}}, nil, nil)
	case KToolArgs:
		if e.tool >= 0 && ev.Text != "" {
			e.chunk(map[string]any{"tool_calls": []map[string]any{{"index": e.tool,
				"function": map[string]any{"arguments": ev.Text}}}}, nil, nil)
		}
	case KError:
		e.w.event("", map[string]any{"error": map[string]any{"message": ev.Text, "type": "api_error"}})
	}
	e.col.add(ev)
}

func (e *chatEncoder) finish() {
	if !e.started {
		e.start(Event{})
	}
	res := e.col.finish()
	e.chunk(nil, stopToChat(res.Stop), nil)
	e.chunk(nil, nil, res.Usage.chat())
	e.w.event("", "[DONE]")
}

// renderChat is the non-streaming reply.
func renderChat(res Result, model string) []byte {
	msg := map[string]any{"role": "assistant", "content": nil}
	var calls []map[string]any
	var think string
	for _, p := range res.Parts {
		switch p.Kind {
		case Text:
			if msg["content"] == nil {
				msg["content"] = p.Text
			} else {
				msg["content"] = msg["content"].(string) + p.Text
			}
		case Thinking:
			think += p.Text
		case ToolCall:
			id := p.ID
			if id == "" {
				id = "call_" + newID()
			}
			calls = append(calls, map[string]any{"id": id, "type": "function",
				"function": map[string]any{"name": p.Name, "arguments": argsString(p)}})
		}
	}
	if len(calls) > 0 {
		msg["tool_calls"] = calls
	}
	if think != "" {
		msg["reasoning_content"] = think
	}
	id := res.ID
	if id == "" {
		id = newID()
	}
	if !strings.HasPrefix(id, "chatcmpl-") {
		id = "chatcmpl-" + id
	}
	if res.Model != "" {
		model = res.Model
	}
	b, _ := json.Marshal(map[string]any{"id": id, "object": "chat.completion", "created": time.Now().Unix(), "model": model,
		"choices": []map[string]any{{"index": 0, "message": msg, "finish_reason": stopToChat(res.Stop)}},
		"usage":   res.Usage.chat()})
	return b
}
