package gateway

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ---- Anthropic Messages -----------------------------------------------------

type aBlock struct {
	Type string `json:"type"`
	// text
	Text string `json:"text,omitempty"`
	// image
	Source *struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
	} `json:"source,omitempty"`
	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	// thinking
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
}

type aRequest struct {
	Model    string          `json:"model"`
	System   json.RawMessage `json:"system,omitempty"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Type        string          `json:"type,omitempty"`
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		InputSchema json.RawMessage `json:"input_schema,omitempty"`
	} `json:"tools,omitempty"`
	ToolChoice *struct {
		Type                   string `json:"type"`
		Name                   string `json:"name,omitempty"`
		DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
	} `json:"tool_choice,omitempty"`
	MaxTokens     int      `json:"max_tokens"`
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"top_p,omitempty"`
	StopSequences []string `json:"stop_sequences,omitempty"`
	Stream        bool     `json:"stream,omitempty"`
	Thinking      *struct {
		Type         string `json:"type"`
		BudgetTokens int    `json:"budget_tokens,omitempty"`
	} `json:"thinking,omitempty"`
	OutputConfig *struct {
		Effort string `json:"effort,omitempty"`
	} `json:"output_config,omitempty"`
}

func parseAnthropic(body []byte) (*Request, error) {
	var a aRequest
	if err := json.Unmarshal(body, &a); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Model: a.Model, System: stringOrText(a.System), MaxTokens: a.MaxTokens,
		Temp: a.Temperature, TopP: a.TopP, Stop: a.StopSequences, Stream: a.Stream}
	for _, m := range a.Messages {
		msg := Message{Role: m.Role}
		var s string
		if json.Unmarshal(m.Content, &s) == nil {
			if s != "" {
				msg.Parts = append(msg.Parts, Part{Kind: Text, Text: s})
			}
		} else {
			var blocks []aBlock
			if err := json.Unmarshal(m.Content, &blocks); err != nil {
				return nil, fmt.Errorf("invalid message content: %v", err)
			}
			for _, b := range blocks {
				switch b.Type {
				case "text":
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: b.Text})
				case "image":
					if b.Source != nil {
						msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: b.Source.MediaType, Data: b.Source.Data, URL: b.Source.URL})
					}
				case "tool_use":
					msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: b.ID, Name: b.Name, Args: b.Input})
				case "tool_result":
					msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: b.ToolUseID, Text: stringOrText(b.Content), IsError: b.IsError})
				case "thinking":
					msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: b.Thinking, Signature: b.Signature})
				}
			}
		}
		r.Messages = append(r.Messages, msg)
	}
	for _, t := range a.Tools {
		if t.Type != "" && t.Type != "custom" && len(t.InputSchema) == 0 {
			// server-side tools mean nothing elsewhere; web search is
			// noted for those that can search by themselves
			if strings.HasPrefix(t.Type, "web_search") {
				r.WebSearch = true
			}
			continue
		}
		r.Tools = append(r.Tools, Tool{Name: t.Name, Description: t.Description, Schema: t.InputSchema})
	}
	if tc := a.ToolChoice; tc != nil {
		switch tc.Type {
		case "auto", "none":
			r.ToolChoice = tc.Type
		case "any":
			r.ToolChoice = "required"
		case "tool":
			r.ToolChoice = "name:" + tc.Name
		}
		if tc.DisableParallelToolUse {
			f := false
			r.Parallel = &f
		}
	}
	// output_config's effort sets how hard the model thinks only when it
	// was asked to think: Claude Code's title requests carry effort but no
	// thinking, and reasoning_effort would turn it on upstream
	if th := a.Thinking; th != nil && (th.Type == "enabled" || th.Type == "adaptive") {
		r.Thinking = true
		r.Effort = effortOfBudget(th.BudgetTokens)
		if oc := a.OutputConfig; oc != nil {
			if e := effortOf(oc.Effort); e != "" {
				r.Effort = e
			}
		}
	}
	return r, nil
}

// thinkingOffUnlessAsked says thinking is off when the request doesn't
// mention it. That is what Anthropic's API assumes, but DeepSeek and other
// vendors' Anthropic endpoints think by default, so Claude Code's requests
// for a session title, sent without thinking, spent their tokens thinking.
func thinkingOffUnlessAsked(body []byte) []byte {
	var v struct {
		Thinking json.RawMessage `json:"thinking"`
	}
	if json.Unmarshal(body, &v) != nil || v.Thinking != nil {
		return body
	}
	return withFields(body, map[string]any{"thinking": map[string]any{"type": "disabled"}})
}

// alwaysThinks is a vendor refusing to turn a model's thinking off: Z.ai's
// GLM-5.3 answers 1210, "…always engages in thinking…".
var alwaysThinks = regexp.MustCompile(`(?i)always engages in thinking|thinking (?:can ?not|can't) be (?:disabled|turned off)`)

// withoutThinkingOff is body with its thinking left to the model, when it
// says thinking is off; false when it doesn't.
func withoutThinkingOff(body []byte) ([]byte, bool) {
	var q map[string]json.RawMessage
	var th struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(body, &q) != nil || json.Unmarshal(q["thinking"], &th) != nil || th.Type != "disabled" {
		return nil, false
	}
	delete(q, "thinking")
	out, err := json.Marshal(q)
	return out, err == nil
}

// buildAnthropic renders a request for an Anthropic-style upstream.
// claudeVersion finds the family's version in a Claude model id however a
// relay spells it: claude-opus-4-6, claude-opus-5, anthropic.claude-sonnet-4.6-v1.
var claudeVersion = regexp.MustCompile(`claude-(?:opus|sonnet|haiku)-(\d+)(?:[-.](\d{1,2}))?(?:[^0-9]|$)`)

// adaptiveOnly is a Claude model from 4.6 on, which thinks adaptively:
// claude-opus-5-5 refuses thinking.type=enabled with a budget ("requires
// adaptive thinking"), so how hard it thinks goes in output_config.effort.
func adaptiveOnly(model string) bool {
	m := claudeVersion.FindStringSubmatch(strings.ToLower(model))
	if m == nil {
		return false
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return major > 4 || major == 4 && minor >= 6
}

func buildAnthropic(r *Request, model string) []byte {
	type msg struct {
		Role    string   `json:"role"`
		Content []aBlock `json:"content"`
	}
	var msgs []msg
	push := func(role string, blocks []aBlock) {
		if len(blocks) == 0 {
			return
		}
		if n := len(msgs); n > 0 && msgs[n-1].Role == role {
			msgs[n-1].Content = append(msgs[n-1].Content, blocks...)
			return
		}
		msgs = append(msgs, msg{Role: role, Content: blocks})
	}
	for _, m := range r.Messages {
		var results, rest []aBlock
		for _, p := range m.Parts {
			switch p.Kind {
			case Text:
				if strings.TrimSpace(p.Text) != "" {
					rest = append(rest, aBlock{Type: "text", Text: p.Text})
				}
			case Image:
				b := aBlock{Type: "image"}
				b.Source = &struct {
					Type      string `json:"type"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
					URL       string `json:"url"`
				}{}
				if p.URL != "" && p.Data == "" {
					b.Source.Type, b.Source.URL = "url", p.URL
				} else {
					b.Source.Type, b.Source.MediaType, b.Source.Data = "base64", p.MediaType, p.Data
				}
				rest = append(rest, b)
			case ToolCall:
				rest = append(rest, aBlock{Type: "tool_use", ID: p.ID, Name: p.Name, Input: argsOf(p)})
			case ToolResult:
				c, _ := json.Marshal(p.Text)
				results = append(results, aBlock{Type: "tool_result", ToolUseID: p.CallID, Content: c, IsError: p.IsError})
			case Thinking:
				if p.Signature != "" {
					rest = append(rest, aBlock{Type: "thinking", Thinking: p.Text, Signature: p.Signature})
				}
			}
		}
		role := m.Role
		if role != "assistant" {
			role = "user"
		}
		push(role, append(results, rest...))
	}
	// an assistant turn made only of unsigned thinking is nothing to Anthropic
	out := map[string]any{"model": model, "messages": msgs, "stream": r.Stream}
	if r.System != "" {
		out["system"] = r.System
	}
	maxTokens := r.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 16384
	}
	if (r.Thinking || r.Effort != "") && adaptiveOnly(model) {
		out["thinking"] = map[string]any{"type": "adaptive"}
		if e := r.Effort; e != "" {
			if e == "xhigh" {
				e = "max"
			}
			out["output_config"] = map[string]any{"effort": e}
		}
	} else if r.Thinking || r.Effort != "" {
		budget := budgetOf(r.Effort)
		if maxTokens < budget+4096 {
			maxTokens = budget + 4096
		}
		out["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
	} else if r.Temp != nil {
		out["temperature"] = *r.Temp
	} else if r.TopP != nil {
		out["top_p"] = *r.TopP
	}
	out["max_tokens"] = maxTokens
	if len(r.Stop) > 0 {
		out["stop_sequences"] = r.Stop
	}
	if len(r.Tools) > 0 || r.WebSearch {
		var tools []map[string]any
		for _, t := range r.Tools {
			schema := t.Schema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "input_schema": schema})
		}
		if r.WebSearch {
			tools = append(tools, map[string]any{"type": "web_search_20250305", "name": "web_search", "max_uses": 5})
		}
		out["tools"] = tools
		var tc map[string]any
		switch {
		case r.ToolChoice == "auto" || r.ToolChoice == "":
			if r.Parallel != nil && !*r.Parallel {
				tc = map[string]any{"type": "auto"}
			}
		case r.ToolChoice == "required":
			tc = map[string]any{"type": "any"}
		case r.ToolChoice == "none":
			tc = map[string]any{"type": "none"}
		case strings.HasPrefix(r.ToolChoice, "name:"):
			tc = map[string]any{"type": "tool", "name": strings.TrimPrefix(r.ToolChoice, "name:")}
		}
		if tc != nil {
			if r.Parallel != nil && !*r.Parallel && tc["type"] != "none" {
				tc["disable_parallel_tool_use"] = true
			}
			out["tool_choice"] = tc
		}
	}
	b, _ := json.Marshal(out)
	return b
}

// anthropicDecoder leaves out the blocks of Anthropic's own server tools —
// a web search it ran — whose input is no call of the client's.
type anthropicDecoder struct{ server map[int]bool }

func (d *anthropicDecoder) decode(data string, emit func(Event)) error {
	var ev struct {
		Type         string `json:"type"`
		Index        int    `json:"index"`
		ContentBlock struct {
			Type string `json:"type"`
		} `json:"content_block"`
	}
	if json.Unmarshal([]byte(data), &ev) == nil {
		switch ev.Type {
		case "content_block_start":
			d.server[ev.Index] = ev.ContentBlock.Type == "server_tool_use"
		case "content_block_delta", "content_block_stop":
			if d.server[ev.Index] {
				return nil
			}
		}
	}
	return decodeAnthropic(data, emit)
}

// decodeAnthropic turns an Anthropic event stream into events.
func decodeAnthropic(data string, emit func(Event)) error {
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			ID    string `json:"id"`
			Model string `json:"model"`
			Usage aUsage `json:"usage"`
		} `json:"message"`
		Index        int `json:"index"`
		ContentBlock struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
			Text string `json:"text"`
		} `json:"content_block"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			PartialJSON string `json:"partial_json"`
			Thinking    string `json:"thinking"`
			Signature   string `json:"signature"`
			StopReason  string `json:"stop_reason"`
		} `json:"delta"`
		Usage aUsage `json:"usage"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return nil // keep going: pings and unknown shapes are harmless
	}
	switch ev.Type {
	case "message_start":
		emit(Event{Kind: KStart, MsgID: ev.Message.ID, Model: ev.Message.Model, Usage: ev.Message.Usage.usage()})
	case "content_block_start":
		switch ev.ContentBlock.Type {
		case "tool_use":
			emit(Event{Kind: KToolStart, ID: ev.ContentBlock.ID, Name: ev.ContentBlock.Name})
		case "text":
			if ev.ContentBlock.Text != "" {
				emit(Event{Kind: KText, Text: ev.ContentBlock.Text})
			}
		}
	case "content_block_delta":
		switch ev.Delta.Type {
		case "text_delta":
			emit(Event{Kind: KText, Text: ev.Delta.Text})
		case "input_json_delta":
			emit(Event{Kind: KToolArgs, Text: ev.Delta.PartialJSON})
		case "thinking_delta":
			emit(Event{Kind: KThink, Text: ev.Delta.Thinking})
		case "signature_delta":
			emit(Event{Kind: KSig, Text: ev.Delta.Signature})
		}
	case "message_delta":
		if ev.Delta.StopReason != "" {
			emit(Event{Kind: KStop, Stop: stopFromAnthropic(ev.Delta.StopReason)})
		}
		emit(Event{Kind: KUsage, Usage: ev.Usage.usage()})
	case "error":
		emit(Event{Kind: KError, Text: ev.Error.Message})
	}
	return nil
}

type aUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

func (u aUsage) usage() Usage {
	return Usage{Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheReadInputTokens, CacheWrite: u.CacheCreationInputTokens}
}

func (u Usage) anthropic() aUsage {
	return aUsage{InputTokens: u.Input, OutputTokens: u.Output, CacheReadInputTokens: u.CacheRead, CacheCreationInputTokens: u.CacheWrite}
}

func stopFromAnthropic(s string) string {
	switch s {
	case "max_tokens", "model_context_window_exceeded":
		// the second is Claude 4.5+ running into its context window
		// before max_tokens: the reply is cut short all the same
		return "length"
	case "tool_use":
		return "tool"
	case "refusal":
		return "filter"
	}
	return "stop"
}

func stopToAnthropic(s string) string {
	switch s {
	case "length":
		return "max_tokens"
	case "tool":
		return "tool_use"
	case "filter":
		return "refusal"
	}
	return "end_turn"
}

// anthropicEncoder writes events as an Anthropic event stream.
type anthropicEncoder struct {
	w       *sseWriter
	model   string
	index   int
	open    Kind // kind of the open content block, "" when none
	args    bool // the open tool block got arguments
	started bool
	col     collector
}

func (e *anthropicEncoder) start(ev Event) {
	if e.started {
		return
	}
	e.started = true
	id := ev.MsgID
	if id == "" {
		id = "msg_" + newID()
	}
	model := ev.Model
	if model == "" {
		model = e.model
	}
	e.w.event("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": id, "type": "message", "role": "assistant", "model": model, "content": []any{},
		"stop_reason": nil, "stop_sequence": nil, "usage": ev.Usage.anthropic()}})
}

func (e *anthropicEncoder) close() {
	if e.open == "" {
		return
	}
	if e.open == ToolCall && !e.args {
		e.w.event("content_block_delta", map[string]any{"type": "content_block_delta", "index": e.index,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": "{}"}})
	}
	e.w.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": e.index})
	e.index++
	e.open = ""
}

func (e *anthropicEncoder) openBlock(k Kind, block map[string]any) {
	if e.open == k && k != ToolCall {
		return
	}
	e.close()
	e.args = false
	block["type"] = map[Kind]string{Text: "text", Thinking: "thinking", ToolCall: "tool_use"}[k]
	e.w.event("content_block_start", map[string]any{"type": "content_block_start", "index": e.index, "content_block": block})
	e.open = k
}

func (e *anthropicEncoder) delta(d map[string]any) {
	e.w.event("content_block_delta", map[string]any{"type": "content_block_delta", "index": e.index, "delta": d})
}

func (e *anthropicEncoder) event(ev Event) {
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
		e.openBlock(Text, map[string]any{"text": ""})
		e.delta(map[string]any{"type": "text_delta", "text": ev.Text})
	case KThink:
		if ev.Text == "" {
			return
		}
		e.openBlock(Thinking, map[string]any{"thinking": ""})
		e.delta(map[string]any{"type": "thinking_delta", "thinking": ev.Text})
	case KSig:
		if e.open == Thinking {
			e.delta(map[string]any{"type": "signature_delta", "signature": ev.Text})
		}
	case KToolStart:
		id := ev.ID
		if id == "" {
			id = "toolu_" + newID()
		}
		e.openBlock(ToolCall, map[string]any{"id": id, "name": ev.Name, "input": map[string]any{}})
	case KToolArgs:
		if e.open == ToolCall && ev.Text != "" {
			e.args = true
			e.delta(map[string]any{"type": "input_json_delta", "partial_json": ev.Text})
		}
	case KError:
		e.close()
		e.w.event("error", map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": ev.Text}})
	}
	e.col.add(ev)
}

func (e *anthropicEncoder) finish() {
	if !e.started {
		e.start(Event{})
	}
	e.close()
	res := e.col.finish()
	e.w.event("message_delta", map[string]any{"type": "message_delta",
		"delta": map[string]any{"stop_reason": stopToAnthropic(res.Stop), "stop_sequence": nil},
		"usage": res.Usage.anthropic()})
	e.w.event("message_stop", map[string]any{"type": "message_stop"})
}

// renderAnthropic is the non-streaming reply.
func renderAnthropic(res Result, model string) []byte {
	content := []map[string]any{}
	for _, p := range res.Parts {
		switch p.Kind {
		case Text:
			content = append(content, map[string]any{"type": "text", "text": p.Text})
		case Thinking:
			content = append(content, map[string]any{"type": "thinking", "thinking": p.Text, "signature": p.Signature})
		case ToolCall:
			id := p.ID
			if id == "" {
				id = "toolu_" + newID()
			}
			content = append(content, map[string]any{"type": "tool_use", "id": id, "name": p.Name, "input": argsOf(p)})
		}
	}
	id := res.ID
	if id == "" {
		id = "msg_" + newID()
	}
	if res.Model != "" {
		model = res.Model
	}
	b, _ := json.Marshal(map[string]any{"id": id, "type": "message", "role": "assistant", "model": model,
		"content": content, "stop_reason": stopToAnthropic(res.Stop), "stop_sequence": nil, "usage": res.Usage.anthropic()})
	return b
}

func newID() string {
	return fmt.Sprintf("%x", time.Now().UnixNano())
}
