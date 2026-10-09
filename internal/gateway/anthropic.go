package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// ---- Anthropic Messages -----------------------------------------------------

type aBlock struct {
	Type string `json:"type"`
	// text
	Text string `json:"text,omitempty"`
	// image
	Source *struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type,omitempty"`
		Data      string `json:"data,omitempty"`
		URL       string `json:"url,omitempty"`
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
	// a prompt-cache breakpoint: the prompt up to here is cached
	CacheControl map[string]string `json:"cache_control,omitempty"`
}

// ephemeral marks a prompt-cache breakpoint.
var ephemeral = map[string]string{"type": "ephemeral"}

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
		Format *struct {
			Type   string          `json:"type"`
			Schema json.RawMessage `json:"schema"`
		} `json:"format,omitempty"`
	} `json:"output_config,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
	Speed      string          `json:"speed,omitempty"` // "fast": Claude's fast mode
	Safeguards json.RawMessage `json:"safeguards,omitempty"`
}

func parseAnthropic(body []byte) (*Request, error) {
	var a aRequest
	if err := json.Unmarshal(body, &a); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Model: a.Model, System: stringOrText(a.System), MaxTokens: a.MaxTokens,
		Temp: a.Temperature, TopP: a.TopP, Stop: a.StopSequences, Stream: a.Stream, Fast: a.Speed == "fast"}
	r.Safeguards = a.Safeguards
	if string(r.Safeguards) == "null" {
		r.Safeguards = nil
	}
	if len(a.Metadata) > 0 && string(a.Metadata) != "null" {
		r.Metadata = a.Metadata
	}
	if oc := a.OutputConfig; oc != nil && oc.Format != nil && oc.Format.Type == "json_schema" && len(oc.Format.Schema) > 0 {
		r.Format = &Format{Type: "json_schema", Name: formatName, Schema: oc.Format.Schema}
	}
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
					msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: anthropicInID(b.ID), Name: b.Name, Args: b.Input})
				case "tool_result":
					out, images := toolOutput(b.Content)
					msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: anthropicInID(b.ToolUseID), Text: out, Images: images, IsError: b.IsError})
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
		if t.Name == "DeferredToolPlaceholder" {
			// only keeps Anthropic's deferred loading on; never to be called
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
	if th := a.Thinking; th != nil && th.Type == "disabled" {
		r.ThinkOff = true
	} else if th != nil && (th.Type == "enabled" || th.Type == "adaptive") {
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
	// added at the end, the rest left byte for byte as the agent sent it
	b := bytes.TrimRight(body, " \t\r\n")
	if len(b) < 2 || b[len(b)-1] != '}' {
		return withFields(body, map[string]any{"thinking": map[string]any{"type": "disabled"}})
	}
	out := append([]byte{}, b[:len(b)-1]...)
	if len(bytes.TrimSpace(out)) > 1 {
		out = append(out, ',')
	}
	return append(out, `"thinking":{"type":"disabled"}}`...)
}

// anthropicModel is one of Anthropic's own models, which think only when asked,
// so a request for one is left as the agent sent it.
var anthropicModel = regexp.MustCompile(`(?i)(?:^|[/.:-])claude-`)

// alwaysThinks is a vendor refusing to turn a model's thinking off: Z.ai's
// GLM-5.3 answers 1210, "…always engages in thinking…", or, in Chinese
// (ZCode's GLM-5.3-Flash), "该模型始终支持思考，不可关闭" (#699); DashScope's
// own glm-5.3 answers "The value of the enable_thinking parameter is
// restricted to True."
var alwaysThinks = regexp.MustCompile(`(?i)always engages in thinking|thinking (?:can ?not|can't) be (?:disabled|turned off)|enable_thinking[^"]{0,60}restricted to true|始终(?:支持|开启|启用)?思考|思考[^"]{0,20}(?:不可|无法|不能)关闭`)

// ThinksOnlyWhenAsked is a model that takes thinking turned off on the
// Messages API whatever its levels: one of Anthropic's own (anthropicModel,
// or a relay's opus-5.5). Another vendor's model there takes it off only
// when none is among its levels; without it, it always thinks, and GLM-5.3
// turns thinking disabled away (#699).
func ThinksOnlyWhenAsked(model string) bool {
	return anthropicModel.MatchString(model) || claudeVersion.MatchString(strings.ToLower(model))
}

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

// claudeVersion finds the family's version in a Claude model id however a
// relay spells it: claude-opus-4-6, claude-opus-5, anthropic.claude-sonnet-4.6-v1,
// a relay's opus-5.5, or the old order, claude-3-7-sonnet.
var claudeVersion = regexp.MustCompile(`(?:^|[^a-z0-9])(?:claude-)?(?:opus|sonnet|haiku)-(\d{1,2})(?:[-.](\d{1,2}))?(?:[^0-9]|$)|claude-(\d+)(?:[-.](\d))?-(?:opus|sonnet|haiku)`)

// adaptiveOnly is a Claude model from 4.6 on, which thinks adaptively:
// claude-opus-5-5 refuses thinking.type=enabled with a budget ("requires
// adaptive thinking"), so how hard it thinks goes in output_config.effort.
func adaptiveOnly(model string) bool {
	m := claudeVersion.FindStringSubmatch(strings.ToLower(model))
	if m == nil {
		return false
	}
	v := m[1:3]
	if m[3] != "" {
		v = m[3:5]
	}
	major, _ := strconv.Atoi(v[0])
	minor, _ := strconv.Atoi(v[1])
	return major > 4 || major == 4 && minor >= 6
}

// adaptiveThinking is an Anthropic request as a model that thinks only
// adaptively takes it: thinking.type=enabled with a budget — sent by an
// agent that doesn't know the model (its name in magpie, an alias, or a
// group's member, mapped to the vendor's later), or by magpie fitting an
// effort to it — goes as thinking.type=adaptive, with the budget as the
// effort it is nearest in output_config.effort unless one is there (Keenc
// on Discord: claude-opus-5-5 answered 400). Read off the model the body
// is sent with, the vendor's own name; any other request, older Claudes'
// included, goes as it came, and "disabled" stays.
func adaptiveThinking(body []byte) []byte {
	th := gjson.GetBytes(body, "thinking")
	if th.Get("type").String() != "enabled" || !adaptiveOnly(gjson.GetBytes(body, "model").String()) {
		return body
	}
	thinking := map[string]any{"type": "adaptive"}
	if d := th.Get("display"); d.Exists() {
		thinking["display"] = d.Value()
	}
	fields := map[string]any{"thinking": thinking}
	if gjson.GetBytes(body, "output_config.effort").String() == "" {
		if e := effortOfBudget(int(th.Get("budget_tokens").Int())); e != "" {
			if e == "xhigh" {
				e = "max" // as buildAnthropic asks it: 4.6 has no xhigh
			}
			oc, _ := gjson.GetBytes(body, "output_config").Value().(map[string]any)
			if oc == nil {
				oc = map[string]any{}
			}
			oc["effort"] = e
			fields["output_config"] = oc
		}
	}
	return withFields(body, fields)
}

// AdaptiveThinking is adaptiveOnly for agents told how to ask a model: a
// Claude that takes thinking.type=adaptive and an effort, never a budget.
func AdaptiveThinking(model string) bool { return adaptiveOnly(model) }

// effortInOutputConfig is a model that takes how hard it thinks from
// output_config.effort, beside thinking turned on, as ZCode asks it: Z.ai's
// GLM-5.2 and GLM-5.3 on their Anthropic endpoints. A budget alone leaves
// them at their own default.
var effortInOutputConfig = regexp.MustCompile(`(?i)glm-5\.[23](?:$|[-.:/\[])`)

// withOutputEffort is body asking, in output_config.effort, for the level
// of levels nearest the reasoning it asks for; body when it asks for none.
func withOutputEffort(body []byte, levels []string) []byte {
	e := requestEffort(provider.Anthropic, body)
	var v struct {
		OutputConfig map[string]any `json:"output_config"`
	}
	if e == "" || json.Unmarshal(body, &v) != nil {
		return body
	}
	oc := v.OutputConfig
	if oc == nil {
		oc = map[string]any{}
	}
	oc["effort"] = fitEffort(e, levels)
	return withFields(body, map[string]any{"output_config": oc})
}

// imageBlock is an image as an Anthropic block: inline, or by its URL.
func imageBlock(p Part) aBlock {
	b := aBlock{Type: "image"}
	b.Source = &struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type,omitempty"`
		Data      string `json:"data,omitempty"`
		URL       string `json:"url,omitempty"`
	}{}
	if p.URL != "" && p.Data == "" {
		b.Source.Type, b.Source.URL = "url", p.URL
	} else {
		b.Source.Type, b.Source.MediaType, b.Source.Data = "base64", p.MediaType, p.Data
	}
	return b
}

// buildAnthropic renders a request for an Anthropic-style upstream.
func buildAnthropic(r *Request, model string) []byte {
	if r.Format != nil && r.Format.schema() == nil {
		// output_config.format takes a schema only: any JSON object is
		// asked for in words
		r = r.inSystem()
	}
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
			case File:
				rest = append(rest, aBlock{Type: "text", Text: attachmentText(p)})
			case Image:
				rest = append(rest, imageBlock(p))
			case ToolCall:
				rest = append(rest, aBlock{Type: "tool_use", ID: p.ID, Name: p.Name, Input: argsOf(p)})
			case ToolResult:
				c, _ := json.Marshal(p.Text)
				if len(p.Images) > 0 {
					// a tool_result holds images beside its text
					var blocks []aBlock
					if strings.TrimSpace(p.Text) != "" {
						blocks = append(blocks, aBlock{Type: "text", Text: p.Text})
					}
					for _, im := range p.Images {
						blocks = append(blocks, imageBlock(im))
					}
					c, _ = json.Marshal(blocks)
				}
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
	// Anthropic caches a prompt only up to a block marked for it, which a
	// request from another API (Codex's, a Chat client's) never has: the
	// conversation so far is marked at its last block, so the next turn,
	// which only adds to it, reads it from the cache; thinking can't be
	// marked. Two marks at most, with the one Claude's sign-in adds kept
	// within Anthropic's four.
	if n := len(msgs); n > 0 {
		c := msgs[n-1].Content
		for i := len(c) - 1; i >= 0; i-- {
			if c[i].Type != "thinking" {
				c[i].CacheControl = ephemeral
				break
			}
		}
	}
	// an assistant turn made only of unsigned thinking is nothing to Anthropic
	out := map[string]any{"model": model, "messages": msgs, "stream": r.Stream}
	if r.System != "" {
		// the tools and system prompt, the same every turn, are cached
		// apart from the conversation
		out["system"] = []aBlock{{Type: "text", Text: r.System, CacheControl: ephemeral}}
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
	} else if think, budget := r.Thinking || r.Effort != "", budgetOf(r.Effort); think && (r.MaxTokens <= 0 || r.MaxTokens > 2048) {
		if r.MaxTokens > 0 {
			// the client's cap is what the model can write (Pi sends the
			// model's own output limit): the budget fits under it, leaving
			// room for the answer, as a raised max_tokens past what the
			// model takes is refused with a 400
			budget = min(budget, maxTokens-1024)
		} else if maxTokens < budget+4096 {
			maxTokens = budget + 4096
		}
		out["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
		if r.Effort != "" && effortInOutputConfig.MatchString(model) {
			out["output_config"] = map[string]any{"effort": r.Effort}
		}
	} else if r.Temp != nil {
		out["temperature"] = *r.Temp
	} else if r.TopP != nil {
		out["top_p"] = *r.TopP
	}
	if s := r.Format.schema(); len(s) > 0 {
		oc, _ := out["output_config"].(map[string]any)
		if oc == nil {
			oc = map[string]any{}
		}
		oc["format"] = map[string]any{"type": "json_schema", "schema": s}
		out["output_config"] = oc
	}
	out["max_tokens"] = maxTokens
	if len(r.Stop) > 0 {
		out["stop_sequences"] = r.Stop
	}
	if len(r.Metadata) > 0 {
		out["metadata"] = r.Metadata
	}
	if len(r.Tools) > 0 || r.WebSearch {
		var tools []map[string]any
		for _, t := range r.Tools {
			schema := t.Schema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			} else {
				schema = objectSchema(schema)
			}
			tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "input_schema": schema})
		}
		if r.System == "" && len(tools) > 0 {
			tools[len(tools)-1]["cache_control"] = ephemeral
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
// a web search it ran — whose input is no call of the client's. A
// tool_use block whose start carries its whole input, as a relay in front
// of another vendor's model may send it with no input_json_delta after
// (蓝猫 on Discord), keeps that input; Anthropic's own start has an empty
// one, its deltas after.
type anthropicDecoder struct {
	server map[int]bool
	whole  map[int]bool // the tool_use blocks whose start carried their input
}

func (d *anthropicDecoder) decode(data string, emit func(Event)) error {
	var ev struct {
		Type         string `json:"type"`
		Index        int    `json:"index"`
		ContentBlock struct {
			Type  string          `json:"type"`
			Input json.RawMessage `json:"input"`
		} `json:"content_block"`
		Delta struct {
			Type string `json:"type"`
		} `json:"delta"`
	}
	if json.Unmarshal([]byte(data), &ev) == nil {
		switch ev.Type {
		case "content_block_start":
			d.server[ev.Index] = ev.ContentBlock.Type == "server_tool_use"
			d.whole[ev.Index] = false
			if ev.ContentBlock.Type == "tool_use" && fullInput(ev.ContentBlock.Input) {
				if err := decodeAnthropic(data, emit); err != nil {
					return err
				}
				d.whole[ev.Index] = true
				emit(Event{Kind: KToolArgs, Text: string(ev.ContentBlock.Input)})
				return nil
			}
		case "content_block_delta", "content_block_stop":
			if d.server[ev.Index] {
				return nil
			}
			if d.whole[ev.Index] && ev.Delta.Type == "input_json_delta" {
				return nil // the input came whole already
			}
		}
	}
	return decodeAnthropic(data, emit)
}

// fullInput says whether a tool_use start's input is the call's own: an
// object with something in it.
func fullInput(in json.RawMessage) bool {
	var m map[string]any
	return json.Unmarshal(in, &m) == nil && len(m) > 0
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
		emit(Event{Kind: KError, Text: ev.Error.Message, Code: refusedCode(data)})
	}
	return nil
}

type aUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	// CacheCreation splits the cache writes by how long they are kept,
	// which Anthropic bills apart: 1.25× input for 5 minutes, 2× for an
	// hour
	CacheCreation *aCacheCreation `json:"cache_creation,omitempty"`
}

type aCacheCreation struct {
	Ephemeral5m int `json:"ephemeral_5m_input_tokens"`
	Ephemeral1h int `json:"ephemeral_1h_input_tokens"`
}

func (u aUsage) usage() Usage {
	out := Usage{Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheReadInputTokens, CacheWrite: u.CacheCreationInputTokens}
	if c := u.CacheCreation; c != nil {
		out.CacheWrite = max(out.CacheWrite, c.Ephemeral5m+c.Ephemeral1h)
		out.CacheWrite1h = c.Ephemeral1h
	}
	return out
}

// anthropic is the usage as Anthropic says it, the cache writes split by
// how long they are kept where some were for an hour.
func (u Usage) anthropic() aUsage {
	out := aUsage{InputTokens: u.Input, OutputTokens: u.Output, CacheReadInputTokens: u.CacheRead, CacheCreationInputTokens: u.CacheWrite}
	if h := min(u.CacheWrite1h, u.CacheWrite); h > 0 {
		out.CacheCreation = &aCacheCreation{Ephemeral5m: u.CacheWrite - h, Ephemeral1h: h}
	}
	return out
}

func stopFromAnthropic(s string) string {
	switch s {
	case "max_tokens", "model_context_window_exceeded":
		// the second is Claude 4.5+ running into its context window
		// before max_tokens: the reply is cut short all the same
		return "length"
	case "tool_use",
		// an Anthropic-shaped relay in front of another vendor's model,
		// passing on OpenAI's reasons as they came (蓝猫 on Discord)
		"tool_calls", "function_call":
		return "tool"
	case "length":
		return "length"
	case "refusal", "content_filter":
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
	id      string
	w       *sseWriter
	model   string
	index   int
	open    Kind // kind of the open content block, "" when none
	args    bool // the open tool block got arguments
	started bool
	col     collector
}

// anthropicID is a reply's id as Anthropic's API gives one, msg_…: an
// OpenAI-shaped upstream's chatcmpl-… (a plugin's provider, a relay) reads
// as a built-in's does.
func anthropicID(id string) string {
	if id == "" {
		return "msg_" + newID()
	}
	if strings.HasPrefix(id, "msg_") {
		return id
	}
	return "msg_" + strings.TrimPrefix(id, "chatcmpl-")
}

// Anthropic's Messages API, and Claude Code with it, takes a tool_use id
// only of [A-Za-z0-9_-]: Claude Code drops a call whose id has another
// character, and the turn fails "could not be parsed" (#1304). Other
// vendors' ids can have one (Devin's "Bash:0#a65b…", a plugin's or a
// relay's), so a call's id goes to an Anthropic client as anthropicOutID
// makes it, and an id that comes back in the client's request is turned
// back by anthropicInID, so the upstream that made the call gets its own
// id on the next turn. An id that is already
// safe goes both ways byte for byte, and Devin's "dv_…" ids, safe already,
// are not encoded twice.
const anthropicIDMark = "mp_"

func anthropicOutID(id string) string {
	if id == "" {
		return id
	}
	return safeCallID(anthropicIDMark, id)
}

func anthropicInID(id string) string { return rawCallID(anthropicIDMark, id) }

// safeCallID is id as one of [A-Za-z0-9_-] only: an id with another
// character, or one beginning with mark, is mark and its base64url, which
// rawCallID turns back; any other id is itself. It keeps no state, so an
// id comes back the same from any request, after any restart.
func safeCallID(mark, id string) string {
	if safeIDChars.MatchString(id) && !strings.HasPrefix(id, mark) {
		return id
	}
	return mark + base64.RawURLEncoding.EncodeToString([]byte(id))
}

// rawCallID is the id safeCallID was given for id, or id itself when
// safeCallID didn't make it.
func rawCallID(mark, id string) string {
	if !strings.HasPrefix(id, mark) || !safeIDChars.MatchString(id) {
		return id
	}
	raw, err := base64.RawURLEncoding.DecodeString(id[len(mark):])
	if err != nil || len(raw) == 0 || !utf8.Valid(raw) || safeCallID(mark, string(raw)) != id {
		return id
	}
	return string(raw)
}

var safeIDChars = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func (e *anthropicEncoder) start(ev Event) {
	if e.started {
		return
	}
	e.started = true
	id := anthropicID(ev.MsgID)
	e.id = id
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
		e.openBlock(ToolCall, map[string]any{"id": anthropicOutID(id), "name": ev.Name, "input": map[string]any{}})
	case KToolArgs:
		if e.open == ToolCall && ev.Text != "" {
			e.args = true
			e.delta(map[string]any{"type": "input_json_delta", "partial_json": ev.Text})
		}
	case KSearch:
		// as Anthropic's own web search tells it, which Claude Code's
		// WebSearch counts and takes its links from
		e.close()
		id := "srvtoolu_" + newID()
		query, _ := json.Marshal(map[string]string{"query": ev.Text})
		e.w.event("content_block_start", map[string]any{"type": "content_block_start", "index": e.index,
			"content_block": map[string]any{"type": "server_tool_use", "id": id, "name": "web_search", "input": map[string]any{}}})
		e.delta(map[string]any{"type": "input_json_delta", "partial_json": string(query)})
		e.w.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": e.index})
		e.index++
		e.w.event("content_block_start", map[string]any{"type": "content_block_start", "index": e.index,
			"content_block": searchResultBlock(id, ev.Hits)})
		e.w.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": e.index})
		e.index++
	case KError:
		e.close()
		failed := map[string]any{"type": "api_error", "message": ev.Text}
		if ev.Code != "" {
			failed["code"] = ev.Code // preserve the upstream error type, including refusals
		}
		e.w.event("error", map[string]any{"type": "error", "error": failed})
	}
	e.col.add(ev)
}

// keepalive is Anthropic's own: a ping event, which its SDKs skip and
// Claude Code's stream watchdog counts.
func (e *anthropicEncoder) keepalive() {
	e.w.event("ping", map[string]any{"type": "ping"})
}

func (e *anthropicEncoder) finish() {
	if !e.started {
		e.start(Event{})
	}
	e.close()
	res := e.col.finish()
	delta := map[string]any{"stop_reason": stopToAnthropic(res.Stop), "stop_sequence": nil}
	if len(res.SafeguardResults) > 0 {
		delta["safeguard_results"] = res.SafeguardResults
	}
	e.w.event("message_delta", map[string]any{"type": "message_delta",
		"delta": delta,
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
			content = append(content, map[string]any{"type": "tool_use", "id": anthropicOutID(id), "name": p.Name, "input": argsOf(p)})
		case Search:
			id := "srvtoolu_" + newID()
			content = append(content,
				map[string]any{"type": "server_tool_use", "id": id, "name": "web_search", "input": map[string]any{"query": p.Text}},
				searchResultBlock(id, p.Hits))
		}
	}
	id := anthropicID(res.ID)
	if res.Model != "" {
		model = res.Model
	}
	reply := map[string]any{"id": id, "type": "message", "role": "assistant", "model": model,
		"content": content, "stop_reason": stopToAnthropic(res.Stop), "stop_sequence": nil, "usage": res.Usage.anthropic()}
	if len(res.SafeguardResults) > 0 {
		reply["safeguard_results"] = res.SafeguardResults
	}
	b, _ := json.Marshal(reply)
	return b
}

// searchResultBlock is a web_search_tool_result block of the pages a
// search found.
func searchResultBlock(id string, hits []Hit) map[string]any {
	results := []map[string]any{}
	for _, h := range hits {
		// never the searcher's encrypted_content: it is sealed for the
		// vendor that searched, and the client would send it to another
		var age any
		if h.PageAge != "" {
			age = h.PageAge
		}
		results = append(results, map[string]any{"type": "web_search_result", "title": h.Title, "url": h.URL,
			"encrypted_content": "", "page_age": age})
	}
	return map[string]any{"type": "web_search_tool_result", "tool_use_id": id, "content": results}
}

// idSeq keeps ids made within one tick of the clock apart: Windows'
// clock moves in steps of up to 15.6ms and macOS's in microseconds, so
// the parallel calls of one Gemini chunk all got the same id.
var idSeq atomic.Uint32

// idClock is where newID reads the time; a test holds it still to stand in
// for a coarse clock.
var idClock = time.Now

func newID() string {
	return fmt.Sprintf("%x%08x", idClock().UnixNano(), idSeq.Add(1))
}

// objectSchema is a tool's input schema with no anyOf, oneOf or allOf at
// its root, which Anthropic's API refuses ("input_schema does not support
// oneOf, allOf, or anyOf at the top level"): Codex's codex_app
// automation_update has one, and Claude models behind Factory answered 400
// to every request offering it (#646). A schema that has none is sent as
// it came.
func objectSchema(schema json.RawMessage) json.RawMessage {
	if !bytes.Contains(schema, []byte(`Of"`)) {
		return schema
	}
	var m map[string]any
	if json.Unmarshal(schema, &m) != nil || !provider.ObjectRoot(m) {
		return schema
	}
	b, err := json.Marshal(m)
	if err != nil {
		return schema
	}
	return b
}
