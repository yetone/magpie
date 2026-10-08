package gateway

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"mime"
	"net"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
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
	// Gemini's OpenAI-compatible API gives a call's thought signature
	// here, and wants it back (gemini_signature.go)
	ExtraContent json.RawMessage `json:"extra_content,omitempty"`
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
			Strict      *bool           `json:"strict,omitempty"`
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
	ResponseFormat      json.RawMessage `json:"response_format,omitempty"`
}

func parseChat(body []byte) (*Request, error) {
	var c cRequest
	if err := json.Unmarshal(body, &c); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Model: c.Model, MaxTokens: c.MaxCompletionTokens, Temp: c.Temperature, TopP: c.TopP,
		Stream: c.Stream, Effort: effortOf(c.ReasoningEffort), Parallel: c.ParallelToolCalls, Fast: c.ServiceTier == "priority",
		CacheKey: c.PromptCacheKey, Format: openAIFormat(c.ResponseFormat)}
	if r.MaxTokens == 0 {
		r.MaxTokens = c.MaxTokens
	}
	if r.Effort != "" {
		r.Thinking = true
	}
	r.ThinkOff = strings.EqualFold(strings.TrimSpace(c.ReasoningEffort), "none")
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
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: tc.ID, Name: tc.Function.Name, Args: parseArgs(tc.Function.Arguments),
					Signature: googleSignature(tc.ExtraContent)})
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
		r.Tools = append(r.Tools, Tool{Name: t.Function.Name, Description: t.Function.Description, Schema: t.Function.Parameters, Strict: t.Function.Strict != nil && *t.Function.Strict})
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
		File struct {
			FileData string `json:"file_data"`
			FileID   string `json:"file_id"`
			Filename string `json:"filename"`
		} `json:"file"`
	}
	json.Unmarshal(raw, &items)
	var out []Part
	for _, it := range items {
		switch it.Type {
		case "text":
			out = append(out, Part{Kind: Text, Text: it.Text})
		case "image_url":
			out = append(out, imagePart(it.ImageURL.URL))
		case "file":
			if p, ok := chatFile(it.File.FileData, it.File.FileID, it.File.Filename); ok {
				out = append(out, p)
			}
		}
	}
	return out
}

// chatFile is a Chat Completions file part (a PDF, say) as a file of the
// request's, which a Gemini upstream is given as inline data and the
// others are told of (attachmentText), rather than left out for the model
// to answer as if it had read it (#934). Its data is a data: URL, or the
// bare base64 some clients send; one named only by its OpenAI file id is
// kept as that id.
func chatFile(data, id, name string) (Part, bool) {
	mt, _, _ := strings.Cut(mime.TypeByExtension(strings.ToLower(path.Ext(name))), ";")
	switch {
	case strings.HasPrefix(data, "data:"):
		if p := imagePart(data); p.Data != "" {
			return Part{Kind: File, MediaType: cmp.Or(p.MediaType, mt, "application/octet-stream"), Data: p.Data}, true
		}
	case data != "":
		return Part{Kind: File, MediaType: cmp.Or(mt, "application/octet-stream"), Data: data}, true
	}
	if id != "" {
		return Part{Kind: File, MediaType: cmp.Or(mt, "application/octet-stream"), URL: id}, true
	}
	return Part{}, false
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
	// DeepSeek takes a turn's reasoning back, wherever its models are
	// served (#388), as Command Code's plugin does for a Go key, as the
	// built-in replayed it to /alpha/generate
	replay := strings.Contains(host, "deepseek") || strings.Contains(strings.ToLower(model), "deepseek") || host == provider.CommandCodePlanID
	// Gemini wants each step's thought signature back on its first call
	// (#687), and one it can't check for a step it didn't sign
	gemini := geminiCompat(host, model)
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
	for i, m := range r.Messages {
		if m.Role == "assistant" {
			showSeen()
			am := map[string]any{"role": "assistant"}
			var calls []map[string]any
			var think string
			signed := false
			for _, p := range m.Parts {
				if p.Kind == ToolCall && p.Signature != "" {
					signed = true
				}
			}
			for _, p := range m.Parts {
				switch p.Kind {
				case ToolCall:
					id := p.ID
					if id == "" {
						id = "call_" + newID()
					}
					names[id] = p.Name
					call := map[string]any{"id": id, "type": "function",
						"function": map[string]any{"name": p.Name, "arguments": argsString(p)}}
					if gemini {
						// a later call of a signed step goes without: Gemini
						// signs the first
						if sig := p.Signature; sig != "" {
							call["extra_content"] = googleExtra(sig)
						} else if !signed {
							call["extra_content"] = googleExtra(skipSignature)
						}
					}
					calls = append(calls, call)
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
			// a reply cut short and sent back to go on from (Resume) is marked
			// with the upstream's own prefill mode — DeepSeek's prefix, Kimi's
			// partial (continuation.go; an upstream without one is never asked
			// to go on) — and its reasoning goes with it where the upstream
			// reads reasoning_content back, so the going on picks the thought
			// up where it was cut
			if r.Resume && i == len(r.Messages)-1 {
				mode := chatPrefill(host, model)
				switch mode {
				case "prefix":
					am["prefix"] = true
				case "partial":
					am["partial"] = true
				}
				if think != "" && mode != "" {
					am["reasoning_content"] = think
				}
			} else if think != "" && replay {
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
		// A turn's tool results all go ahead of its own text and images: a
		// tool message must immediately follow the assistant message whose
		// tool_calls it answers — a strict upstream (Kimi) refuses the
		// request otherwise, 400 "tool_call_id is not found".
		var tools []map[string]any
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
				tools = append(tools, map[string]any{"role": "tool", "tool_call_id": p.CallID, "content": out})
				seeLater(p)
			}
		}
		msgs = append(msgs, tools...)
		flush()
	}
	showSeen()
	msgs = pairToolMessages(msgs)
	out := map[string]any{"model": model, "messages": msgs, "stream": r.Stream}
	if r.CacheKey != "" {
		out["prompt_cache_key"] = r.CacheKey
	}
	if r.Stream {
		out["stream_options"] = map[string]any{"include_usage": true}
	}
	// Cursor's plugin reads fast mode here, as the built-in told Cursor;
	// another's chat upstream may not know the tier
	if r.Fast && host == "cursor" {
		out["service_tier"] = "priority"
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
	if r.GeminiCompat {
		// Gemini thinks silently unless asked for its thoughts, and a long
		// think read as the first word coming late (Claude Desktop waited
		// 20 s for 你好); reasoning_effort can't be sent with them
		if tc := aiStudioThinking(r, model); tc != nil {
			out["extra_body"] = map[string]any{"google": map[string]any{"thinking_config": tc}}
		} else if r.ThinkOff {
			// Gemini 3 can't stop thinking; it thinks least at minimal,
			// or at its lowest level where it has no minimal
			out["reasoning_effort"] = cmp.Or(r.OffLevel, "minimal")
		} else if r.Effort != "" {
			out["reasoning_effort"] = r.Effort
		}
	} else if r.Effort != "" {
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
	if r.Format != nil {
		out["response_format"] = r.Format.chat()
	}
	if r.WebSearch && host == "openrouter.ai" {
		// OpenRouter's own search, for any of its models
		out["plugins"] = []map[string]any{{"id": "web"}}
	}
	b, _ := json.Marshal(out)
	return b
}

// pairToolMessages mends the tool exchange of a Chat request's messages
// for upstreams that validate it strictly — Kimi answers a mismatch with
// 400 "tool_call_id is not found" or "an assistant message with
// 'tool_calls' must be followed by tool messages…", and OpenAI-style
// upstreams refuse the same shapes. It runs on every request built for a
// Chat upstream (Zed's xAI path included); Chat→Chat traffic relays
// as-is and never reaches this path.
//
//   - the tool messages answering an assistant's tool_calls go in the
//     calls' order (an agent returns parallel results out of order);
//   - a second answer to the same call is dropped: the first answer
//     stands;
//   - a tool message answering no pending call — its call was answered
//     and flushed already, compacted away, or never there — becomes a
//     user message, so the result survives with no made-up call and no
//     id used twice, the way the Responses path's orphanedToolOutputs
//     turns an output without a call into a user message;
//   - a call left unanswered gets a synthetic error result, so the turn
//     can go on (an interrupted turn leaves its call pending);
//   - an assistant message with nothing in it — no text, no calls, no
//     reasoning, what a thinking-only turn becomes — is dropped inside a
//     pending exchange, where it would sit between calls and their
//     answers; outside an exchange it passes through, as on main.
func pairToolMessages(msgs []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	var pending []string          // calls of the last assistant message with tool_calls
	order := map[string]int{}     // a pending call's place among them
	answered := map[string]bool{} // pending calls a tool message answered
	var tools []map[string]any    // answers to pending, held for sorting

	flushTools := func() {
		if len(tools) == 0 {
			return
		}
		sort.SliceStable(tools, func(i, j int) bool {
			return order[toolMsgID(tools[i])] < order[toolMsgID(tools[j])]
		})
		out = append(out, tools...)
		tools = nil
	}
	flushPending := func() {
		flushTools()
		for _, id := range pending {
			if !answered[id] {
				out = append(out, map[string]any{"role": "tool", "tool_call_id": id,
					"content": "[The result of this tool call is unavailable: the turn was interrupted.]"})
			}
		}
		pending, order, answered = nil, map[string]int{}, map[string]bool{}
	}
	// toolAsUser turns a tool message that answers no pending call into a
	// user message carrying its result. A user message just emitted takes
	// the text in, so two user messages never stand in a row (some
	// models' chat templates turn them away).
	toolAsUser := func(m map[string]any) {
		text, _ := m["content"].(string)
		if strings.TrimSpace(text) == "" {
			text = "Tool result received."
		}
		if n := len(out); n > 0 && out[n-1]["role"] == "user" {
			if s, ok := out[n-1]["content"].(string); ok {
				out[n-1]["content"] = s + "\n\n" + text
				return
			}
		}
		out = append(out, map[string]any{"role": "user", "content": text})
	}

	for _, m := range msgs {
		switch m["role"] {
		case "assistant":
			calls, _ := m["tool_calls"].([]map[string]any)
			if len(calls) == 0 {
				if len(pending) > 0 && emptyAssistant(m) {
					// dropped before it can sit between the pending
					// calls and their answers; the exchange stays open
					continue
				}
				flushPending()
				out = append(out, m)
				continue
			}
			flushPending()
			out = append(out, m)
			for i, c := range calls {
				id, _ := c["id"].(string)
				pending = append(pending, id)
				order[id] = i
			}
		case "tool":
			id := toolMsgID(m)
			if _, ok := order[id]; !ok {
				// answers no pending call: the exchange in flight
				// closes first, so the user message never breaks its
				// adjacency, and the id is used nowhere else
				flushPending()
				toolAsUser(m)
				continue
			}
			if answered[id] {
				continue // a second answer: the first stands
			}
			answered[id] = true
			tools = append(tools, m)
		default:
			flushPending()
			out = append(out, m)
		}
	}
	flushPending()
	return out
}

// toolMsgID is a tool message's tool_call_id.
func toolMsgID(m map[string]any) string {
	id, _ := m["tool_call_id"].(string)
	return id
}

// emptyAssistant reports whether an assistant message carries nothing:
// no text, no tool calls, no reasoning.
func emptyAssistant(m map[string]any) bool {
	if calls, ok := m["tool_calls"].([]map[string]any); ok && len(calls) > 0 {
		return false
	}
	if s, _ := m["reasoning_content"].(string); strings.TrimSpace(s) != "" {
		return false
	}
	s, _ := m["content"].(string)
	return strings.TrimSpace(s) == ""
}

// aiStudioHost is Google AI Studio's Gemini API, whose OpenAI-compatible
// endpoint gives the model's thoughts only when asked for them.
const aiStudioHost = "generativelanguage.googleapis.com"

// geminiCompat reports whether a Chat upstream at host serving model is
// Gemini's OpenAI-compatible API: AI Studio's own, or for a Gemini model a
// proxy on this machine or the LAN, which is most often one in front of it
// (X @saoyan25's). A relay elsewhere is not assumed to pass thinking_config
// on, or to take it.
func geminiCompat(host, model string) bool {
	if host == aiStudioHost {
		return true
	}
	if !strings.Contains(strings.ToLower(model), "gemini") {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" || strings.HasSuffix(host, ".local") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// thinkingEffort moves the level the new Kimi Code (2.x) asks a Chat
// request for, inside its thinking switch ({"type":"enabled","effort":
// "high"}, #333), to reasoning_effort: where kimi-cli put it, beside
// thinking's type, and where the gateway and every other Chat API read it.
// A request that says reasoning_effort itself is left as it is.
func thinkingEffort(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"effort"`)) {
		return body
	}
	var v struct {
		ReasoningEffort *string        `json:"reasoning_effort"`
		Thinking        map[string]any `json:"thinking"`
	}
	if json.Unmarshal(body, &v) != nil || v.ReasoningEffort != nil {
		return body
	}
	effort, _ := v.Thinking["effort"].(string)
	if effort == "" {
		return body
	}
	delete(v.Thinking, "effort")
	return withFields(body, map[string]any{"reasoning_effort": effort, "thinking": v.Thinking})
}

// thinkingConfigField is Gemini's thinking_config as unfit remembers a
// provider that refused it.
const thinkingConfigField = "thinking_config"

// geminiMinimalRefused is Gemini turning minimal away for a model that
// doesn't have it, which names thinking but isn't thinking_config turned
// away (refusesThinkingConfig): gemini-3.8-flash's 400 "Thinking level is
// unsupported: THINKING_LEVEL_MINIMAL", for reasoning_effort minimal and
// thinking_level minimal alike, at Vertex AI's OpenAI-compatible API; and
// gemini-3.1-pro-preview's "thinking_level MINIMAL is not supported by this
// model" at its generateContent.
var geminiMinimalRefused = regexp.MustCompile(`(?i)thinking[ _]level.*minimal`)

// refusesThinkingConfig recognizes an upstream turning a request away for
// the thinking_config it was sent, by its error naming it.
func refusesThinkingConfig(status int, body []byte) bool {
	if !badRequest(status) {
		return false
	}
	b := bytes.ToLower(body)
	return bytes.Contains(b, []byte("extra_body")) || bytes.Contains(b, []byte("thinking")) || bytes.Contains(b, []byte("include_thoughts"))
}

// aiStudioThinking is the thinking_config asking Gemini for its thoughts at
// the effort the client asked, for a client that asked to see them: a level
// for Gemini 3 and later, a budget for 2.x (the steps Google maps
// reasoning_effort to).
func aiStudioThinking(r *Request, model string) map[string]any {
	if !r.Thinking || r.ThinkOff || r.Effort == "none" {
		return nil
	}
	tc := map[string]any{"include_thoughts": true}
	level := r.Effort
	switch level {
	case "xhigh", "max":
		level = "high"
	case "minimal", "low", "medium", "high":
	default:
		return tc // the model's own
	}
	if strings.Contains(strings.ToLower(model), "gemini-2") {
		tc["thinking_budget"] = map[string]int{"minimal": 1024, "low": 1024, "medium": 8192, "high": 24576}[level]
	} else {
		tc["thinking_level"] = level
	}
	return tc
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
		"prompt_tokens_details":     map[string]any{"cached_tokens": u.CacheRead, "cache_write_tokens": u.CacheWrite},
		"completion_tokens_details": map[string]any{"reasoning_tokens": u.Reasoning}}
}

// chatDecoder turns a Chat Completions stream into events. Tool calls come
// as indexed fragments; it tracks which one is open.
type chatDecoder struct {
	started bool
	tool    int    // index of the open tool call, -1 for none
	toolID  string // id of the open tool call, as some relays repeat it on every fragment
	choice  string // index of the first choice seen; an empty string means none yet
	// Gemini's OpenAI-compatible API, asked for thoughts, may give them
	// in the text as a leading <thought>…</thought>: lead holds the text
	// while it could still be that tag's start, thought is being inside it
	lead    string
	thought bool
	past    bool // the reply's text has begun; no tag is looked for now
}

const thoughtOpen, thoughtClose = "<thought>", "</thought>"

// text sends a piece of the reply's text, a leading <thought> block of it
// as thinking.
func (d *chatDecoder) text(s string, emit func(Event)) {
	if !d.past && !d.thought {
		d.lead += s
		lead := strings.TrimLeft(d.lead, " \n")
		if len(lead) < len(thoughtOpen) && strings.HasPrefix(thoughtOpen, lead) {
			return
		}
		if !strings.HasPrefix(lead, thoughtOpen) {
			d.past = true
			s, d.lead = d.lead, ""
			emit(Event{Kind: KText, Text: s})
			return
		}
		s, d.lead, d.thought = strings.TrimPrefix(lead, thoughtOpen), "", true
	}
	if d.thought {
		s = d.lead + s
		d.lead = ""
		if i := strings.Index(s, thoughtClose); i >= 0 {
			if i > 0 {
				emit(Event{Kind: KThink, Text: s[:i]})
			}
			d.thought, d.past = false, true
			s = strings.TrimLeft(s[i+len(thoughtClose):], "\n")
		} else {
			// the end of it may be the close tag begun
			keep := 0
			for n := min(len(thoughtClose)-1, len(s)); n > 0; n-- {
				if strings.HasSuffix(s, thoughtClose[:n]) {
					keep = n
					break
				}
			}
			if t := s[:len(s)-keep]; t != "" {
				emit(Event{Kind: KThink, Text: t})
			}
			d.lead = s[len(s)-keep:]
			return
		}
	}
	if s != "" {
		emit(Event{Kind: KText, Text: s})
	}
}

// end gives back what text was held to see whether a tag began.
func (d *chatDecoder) end(emit func(Event)) {
	if d.lead == "" {
		return
	}
	k := KText
	if d.thought {
		k = KThink
	}
	emit(Event{Kind: k, Text: d.lead})
	d.lead = ""
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
				Content          json.RawMessage `json:"content"`
				ReasoningContent string          `json:"reasoning_content"`
				Reasoning        string          `json:"reasoning"`
				ToolCalls        []cToolCall     `json:"tool_calls"`
				// Vertex AI's OpenAI-compatible API marks a chunk of
				// Gemini's thoughts here, the thought its content
				ExtraContent struct {
					Google struct {
						Thought bool `json:"thought"`
					} `json:"google"`
				} `json:"extra_content"`
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
		emit(Event{Kind: KError, Text: ch.Error.Message, Code: refusedCode(data)})
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
		// Mistral's content may be typed parts, its thinking among them
		// (#483): a chunk that couldn't be read as a string lost both
		var content string
		if text, think, ok := partsText(c.Delta.Content); ok {
			t += think
			content = text
		} else {
			json.Unmarshal(c.Delta.Content, &content)
		}
		if c.Delta.ExtraContent.Google.Thought {
			t, content = t+content, ""
		}
		if t != "" {
			emit(Event{Kind: KThink, Text: t})
		}
		if content != "" {
			d.text(content, emit)
		}
		if len(c.Delta.ToolCalls) > 0 {
			d.end(emit)
		}
		for i, tc := range c.Delta.ToolCalls {
			idx := i
			if tc.Index != nil {
				idx = *tc.Index
			}
			// A delta carrying the id the open call already goes by — some
			// relays repeat it on every fragment, where the spec sends it
			// only on the first — or one more fragment of the open index,
			// continues that call; only a new id or a new index starts the
			// next one.
			if idx != d.tool || (tc.ID != "" && tc.ID != d.toolID) {
				d.tool, d.toolID = idx, tc.ID
				// a call Gemini signed goes to the client with an id
				// carrying the signature, which comes back with it
				emit(Event{Kind: KToolStart, ID: signedID(tc.ID, googleSignature(tc.ExtraContent)), Name: tc.Function.Name})
			}
			if tc.Function.Arguments != "" {
				emit(Event{Kind: KToolArgs, Text: tc.Function.Arguments})
			}
		}
		if c.FinishReason != "" {
			d.end(emit)
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
		failed := map[string]any{"message": ev.Text, "type": "api_error"}
		if ev.Code != "" {
			failed["code"] = ev.Code // preserve the upstream error type, including refusals
		}
		e.w.event("", map[string]any{"error": failed})
	}
	e.col.add(ev)
}

// keepalive is an SSE comment, as OpenAI-compatible servers keep a Chat
// Completions stream alive; its readers skip one.
func (e *chatEncoder) keepalive() { e.w.comment("keepalive") }

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
