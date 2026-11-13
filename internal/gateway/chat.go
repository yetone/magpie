package gateway

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"mime"
	"net"
	"net/http"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
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
		ReasoningContent json.RawMessage `json:"reasoning_content,omitempty"`
		Reasoning        json.RawMessage `json:"reasoning,omitempty"`
		ReasoningDetails json.RawMessage `json:"reasoning_details,omitempty"`
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
	// the level in the other places a relay or a client puts it: new-api
	// and OpenRouter's reasoning object, a Claude client's output_config
	// and thinking budget (chatEffort)
	Reasoning         json.RawMessage `json:"reasoning,omitempty"`
	OutputConfig      json.RawMessage `json:"output_config,omitempty"`
	Thinking          json.RawMessage `json:"thinking,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	ServiceTier       string          `json:"service_tier,omitempty"`
	PromptCacheKey    string          `json:"prompt_cache_key,omitempty"`
	ResponseFormat    json.RawMessage `json:"response_format,omitempty"`
	// not a Chat field: OpenCode merges a model's options into its body
	// whichever API it speaks, and a Codex model it asks on Chat is asked
	// on Responses (Request.AccessPrograms)
	AccessPrograms json.RawMessage `json:"access_programs,omitempty"`
}

// chatEffort is the level a Chat request asks: reasoning_effort, and when
// it names none, the places a relay puts the level of a Claude client's
// request it turned into Chat (reasoning.effort, output_config.effort, a
// thinking budget). reasoning_effort, when there, is the client's word.
func chatEffort(c *cRequest) string {
	if e := effortOf(c.ReasoningEffort); e != "" || strings.TrimSpace(c.ReasoningEffort) != "" {
		return e
	}
	var o struct {
		Effort string `json:"effort"`
	}
	for _, raw := range []json.RawMessage{c.Reasoning, c.OutputConfig} {
		if len(raw) > 0 && decodeJSON(raw, &o) == nil {
			if e := effortOf(o.Effort); e != "" {
				return e
			}
		}
	}
	var th struct {
		Type   string `json:"type"`
		Budget int    `json:"budget_tokens"`
	}
	if len(c.Thinking) > 0 && decodeJSON(c.Thinking, &th) == nil && th.Type == "enabled" {
		return effortOfBudget(th.Budget)
	}
	return ""
}

func parseChat(body []byte) (*Request, error) { return readChat(body, false) }

// readChat is parseChat; for facts (readFacts) it leaves out what they
// never read, as readResponses does: the assistant's words and calls, the
// tools' outputs, and the copies of raw values a parse kept would need. A
// plugin's request is read for its work folder on every try (tellWorkPlace),
// and a long conversation's history was most of what that allocated (#3472).
func readChat(body []byte, facts bool) (*Request, error) {
	decode := decodeJSON
	if facts {
		decode = decodeJSONView
	}
	body = liftMessageTools(body) // a request handle didn't see (#2322)
	body = shapeChatTools(body)   // and its tools in another API's shape (#2622)
	var c cRequest
	if err := decode(body, &c); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Model: c.Model, MaxTokens: c.MaxCompletionTokens, Temp: c.Temperature, TopP: c.TopP,
		Stream: c.Stream, Effort: chatEffort(&c), Parallel: c.ParallelToolCalls, Fast: c.ServiceTier == "priority" || c.ServiceTier == "ultrafast", Ultrafast: c.ServiceTier == "ultrafast", Tier: c.ServiceTier,
		CacheKey: c.PromptCacheKey, Format: openAIFormat(c.ResponseFormat), AccessPrograms: c.AccessPrograms}
	if r.MaxTokens == 0 {
		r.MaxTokens = c.MaxTokens
	}
	if r.Effort != "" {
		r.Thinking = true
	}
	r.ThinkOff = strings.EqualFold(strings.TrimSpace(c.ReasoningEffort), "none")
	var stop string
	if decodeJSON(c.Stop, &stop) == nil && stop != "" {
		r.Stop = []string{stop}
	} else {
		decodeJSON(c.Stop, &r.Stop)
	}
	var sys []string
	for _, m := range c.Messages {
		switch m.Role {
		case "system", "developer":
			sys = append(sys, stringOrText(m.Content))
			r.systemRaw = append(r.systemRaw, m.Content)
		case "user":
			r.Messages = append(r.Messages, Message{Role: "user", Parts: chatParts(m.Content)})
		case "assistant":
			if facts {
				r.Messages = append(r.Messages, Message{Role: "assistant"})
				continue
			}
			msg := Message{Role: "assistant"}
			// an earlier turn's thinking comes back in any of
			// messageReasoning's spellings, and every upstream that
			// thinks wants it as the thinking part
			if think := reasoningText(map[string]json.RawMessage{
				"reasoning_content": m.ReasoningContent,
				"reasoning":         m.Reasoning,
				"reasoning_details": m.ReasoningDetails,
			}); think != "" {
				msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: think})
			}
			msg.Parts = append(msg.Parts, chatParts(m.Content)...)
			for _, tc := range m.ToolCalls {
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: tc.ID, Name: tc.Function.Name, Args: parseArgs(tc.Function.Arguments),
					Signature: googleSignature(tc.ExtraContent)})
			}
			r.Messages = append(r.Messages, msg)
		case "tool":
			if facts {
				r.Messages = append(r.Messages, Message{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: m.ToolCallID}}})
				continue
			}
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
	if decodeJSON(c.ToolChoice, &tc) == nil {
		r.ToolChoice = tc
	} else {
		var o struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if decodeJSON(c.ToolChoice, &o) == nil && o.Function.Name != "" {
			r.ToolChoice = "name:" + o.Function.Name
		}
	}
	return r, nil
}

// chatParts reads a message's content: a string, or an array of parts.
func chatParts(raw json.RawMessage) []Part {
	var s string
	if decodeJSON(raw, &s) == nil {
		if s == "" {
			return nil
		}
		return []Part{{Kind: Text, Text: s}}
	}
	var items []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL    string `json:"url"`
			Detail string `json:"detail"`
		} `json:"image_url"`
		File struct {
			FileData string `json:"file_data"`
			FileID   string `json:"file_id"`
			Filename string `json:"filename"`
		} `json:"file"`
		VideoURL struct {
			URL string `json:"url"`
		} `json:"video_url"`
		// the AI SDK's file part, flat (#4101: ZCode's), whose video
		// alone is read here
		Data      string `json:"data"`
		MediaType string `json:"mediaType"`
		Filename  string `json:"filename"`
	}
	decodeJSON(raw, &items)
	var out []Part
	for _, it := range items {
		switch it.Type {
		case "text":
			out = append(out, Part{Kind: Text, Text: it.Text})
		case "image_url":
			im := imagePart(it.ImageURL.URL)
			im.Detail = it.ImageURL.Detail
			out = append(out, im)
		case "file":
			if p, ok := chatFile(it.File.FileData, it.File.FileID, it.File.Filename); ok {
				out = append(out, p)
			} else if strings.HasPrefix(strings.ToLower(it.MediaType), "video/") {
				if v, ok := videoPart(it.Data, it.MediaType, it.Filename); ok {
					out = append(out, v)
				}
			}
		case "video_url":
			// a video, as DashScope's, Kimi's and OpenRouter's Chat take
			// one (#4101, #6135): left out before, and the model answered
			// for the words alone
			if v, ok := videoPart(it.VideoURL.URL, "", ""); ok {
				out = append(out, v)
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

// chatImage is an image as a Chat Completions image_url, with the detail
// the client asked for: Chat takes low, high and auto as they are, and
// Responses' original, which Chat lacks, is high, the closest it has.
func chatImage(p Part) map[string]any {
	u := map[string]any{"url": dataURL(p)}
	if d := chatDetail(p.Detail); d != "" {
		u["detail"] = d
	}
	return map[string]any{"type": "image_url", "image_url": u}
}

// chatDetail is detail as Chat Completions takes it, or "" for none.
func chatDetail(detail string) string {
	switch d := strings.ToLower(strings.TrimSpace(detail)); d {
	case "low", "high", "auto":
		return d
	case "original":
		return "high"
	}
	return ""
}

// responsesDetail is detail as a Responses input_image takes it: the
// client's own when the API knows it, else auto.
func responsesDetail(detail string) string {
	switch d := strings.ToLower(strings.TrimSpace(detail)); d {
	case "low", "high", "auto", "original":
		return d
	}
	return "auto"
}

func dataURL(p Part) string {
	if p.URL != "" && p.Data == "" {
		return p.URL
	}
	return "data:" + p.MediaType + ";base64," + p.Data
}

// buildChat renders a request for a Chat Completions upstream.
// buildHost is the host a request for p is built for. Cursor's plugin is
// "cursor" (plugin://cursor) moved or not: a moved one's Host is the
// built-in's, for show, which Cursor's is none, and its fast mode was lost
// on every request magpie translated (#1360).
func buildHost(p provider.Provider) string {
	if p.IsPlugin() {
		switch id := p.PluginProvider(); id {
		case "cursor", provider.CommandCodePlanID, "devin", "qoder", "qoder-cn":
			// the plugin's own id, never the built-in's API host a moved
			// account shows (api.commandcode.ai), which lost a moved Command
			// Code Go conversation its reasoning
			return id
		}
	}
	return p.VendorHost()
}

// failedAsText are the plugins whose vendor has no failed mark on a tool
// result: the built-in said it in the text, "Error: " first (devin.go,
// qoder.go), and so the chat request to the plugin says it there too.
var failedAsText = map[string]bool{"devin": true, "qoder": true, "qoder-cn": true}

// chatReplaysReasoning is whether a Chat upstream wants a turn's reasoning
// back in reasoning_content: DeepSeek's models wherever they are served
// (#388), the model named as the provider names it (OpenCode Go's
// deepseek-v4.1-flash, whose host doesn't say so), Command Code's
// plugin, which replays it for a Go key as the built-in did to
// /alpha/generate, and Qoder's, which sends it on as Qoder's own client
// does (uk.jent on Discord; plugin 0.2.42).
func chatReplaysReasoning(host, model string) bool {
	return strings.Contains(host, "deepseek") || strings.Contains(strings.ToLower(model), "deepseek") || host == provider.CommandCodePlanID || host == "qoder" || host == "qoder-cn"
}

// joinTexts joins text parts once, at their joined size: a long turn
// is many parts, and a Builder grown part by part copied the turn
// several times over on every request
func joinTexts(content []map[string]any) string {
	if len(content) == 1 {
		return content[0]["text"].(string)
	}
	n := 0
	for _, c := range content {
		n += len(c["text"].(string))
	}
	var b strings.Builder
	b.Grow(n)
	for _, c := range content {
		b.WriteString(c["text"].(string))
	}
	return b.String()
}

func buildChat(r *Request, model, host string, rejectTemp bool) []byte {
	r = responseInstructions(r)
	var msgs []map[string]any
	if r.System != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": r.System})
	}
	replay := chatReplaysReasoning(host, model)
	// MiniMax's own Chat API gives a model's thinking in the text, between
	// <think> tags, and wants it back there on the turns after (its
	// interleaved thinking): the decoder takes it out as thinking (#1267),
	// so it goes back in where it came from. A relay serving MiniMax's
	// models may give the thinking apart and take it back otherwise.
	// api.minimax.cn is MiniMax China's host beside api.minimaxi.com.
	inlineThink := strings.HasSuffix(host, "minimax.io") || strings.HasSuffix(host, "minimaxi.com") || strings.HasSuffix(host, "minimax.cn")
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
			seen = append(seen, chatImage(im))
		}
	}
	showSeen := func() {
		if len(seen) > 0 {
			msgs = append(msgs, map[string]any{"role": "user", "content": seen})
			seen = nil
		}
	}
	turns := joinSplitCalls(r.Messages)
	// afterTool: the last message sent is a tool result, with no message
	// of the user's after it
	afterTool := false
	for i, m := range turns {
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
			if think != "" && inlineThink && !(r.Resume && i == len(r.Messages)-1) {
				t = "<think>" + think + "</think>\n\n" + t
			}
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
			if r.Resume && i == len(turns)-1 {
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
			} else if replay && (len(calls) > 0 || afterTool) {
				// a tool call with no thinking (another model's turn), or a
				// reply to a tool result with none, which DeepSeek turns the
				// whole request away for (#1462, #1822)
				am["reasoning_content"] = noReasoning
			}
			afterTool = false
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
				msgs = append(msgs, map[string]any{"role": "user", "content": joinTexts(content)})
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
				if isVideo(p) && !r.videoOff {
					plain = false
					content = append(content, chatVideo(p))
					break
				}
				content = append(content, map[string]any{"type": "text", "text": attachmentText(p)})
			case Image:
				plain = false
				content = append(content, chatImage(p))
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
				if p.IsError && failedAsText[host] {
					out = "Error: " + out
				}
				tool := map[string]any{"role": "tool", "tool_call_id": p.CallID, "content": out}
				if p.IsError && host == "cursor" {
					// Cursor's plugin reads it, as the built-in told Cursor
					// isError; another chat upstream has no such field
					tool["is_error"] = true
				}
				tools = append(tools, tool)
				seeLater(p)
			}
		}
		msgs = append(msgs, tools...)
		afterTool = len(tools) > 0 && len(content) == 0 && len(seen) == 0
		flush()
	}
	showSeen()
	msgs, _ = withoutSilentAssistants(msgs)
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
	if r.OwnTier && r.Tier != "" {
		out["service_tier"] = r.Tier
	}
	if r.MaxTokens > 0 {
		if provider.OpenAIHost(host) {
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
			if r.strictSaid {
				fn["strict"] = t.Strict
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
	b, _ := marshalBody(out, "messages")
	return b
}

// pairToolMessages mends the tool exchange of a Chat request's messages
// for upstreams that validate it strictly — Kimi answers a mismatch with
// 400 "tool_call_id is not found" or "an assistant message with
// 'tool_calls' must be followed by tool messages…", and OpenAI-style
// upstreams refuse the same shapes. It runs on every request built for a
// Chat upstream (Zed's xAI path included), and on a Chat client's own
// body relayed whose exchange doesn't pair (toolPairsOut).
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
//     can go on (an interrupted turn leaves its call pending); a turn's
//     calls split over consecutive assistant messages were joined into
//     one by joinSplitCalls before, so they are pending together (#1275);
//   - an assistant message with nothing in it — no text, no calls, no
//     reasoning — is dropped inside a pending exchange, where it would
//     sit between calls and their answers. One with neither text nor
//     calls was taken out before, wherever it stood
//     (withoutSilentAssistants, #2599).
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
		text := contentText(m["content"])
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

// contentText is a Chat message content's text: the string, or its text
// parts joined, as a relayed client's tool message may hold them.
func contentText(c any) string {
	switch c := c.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, p := range c {
			if pm, _ := p.(map[string]any); pm != nil {
				if t, _ := pm["text"].(string); t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
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

// silentAssistant reports whether a Chat message is an assistant turn
// that says nothing: no content or only blank text, and no tool_calls or
// function_call. It is what an agent keeps of a reply the user cut off
// before it said anything — only its thinking, an empty text, nothing —
// and DeepSeek, Kimi and the like turn the whole request away over it,
// "Invalid assistant message: content or tool_calls must be set", on
// every later turn of the conversation (#2599). reasoning_content is not
// content to them. A prefill to go on from (prefix, partial), a refusal
// and an audio reply say something and stay.
func silentAssistant(m map[string]any) bool {
	if m["role"] != "assistant" {
		return false
	}
	if m["prefix"] == true || m["partial"] == true || m["function_call"] != nil || m["audio"] != nil {
		return false
	}
	if s, _ := m["refusal"].(string); s != "" {
		return false
	}
	switch c := m["tool_calls"].(type) {
	case []map[string]any:
		if len(c) > 0 {
			return false
		}
	case []any:
		if len(c) > 0 {
			return false
		}
	case nil:
	default:
		return false
	}
	switch c := m["content"].(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(c) == ""
	case []any:
		for _, p := range c {
			pm, _ := p.(map[string]any)
			if t, _ := pm["text"].(string); pm == nil || pm["type"] != "text" || strings.TrimSpace(t) != "" {
				return false
			}
		}
		return true
	case []map[string]any:
		for _, pm := range c {
			if t, _ := pm["text"].(string); pm["type"] != "text" || strings.TrimSpace(t) != "" {
				return false
			}
		}
		return true
	}
	return false
}

// withoutSilentAssistants leaves out of a Chat request's messages every
// assistant turn that says nothing (silentAssistant), and joins the user
// messages it stood between into one, as toolAsUser joins: some models'
// chat templates turn away two user messages in a row. Nothing of the cut
// turn reaches the model, as nothing of it reached the user. Every
// strict-template rule stays kept: a tool message still follows the turn
// with its call, and a tool message followed by the user's is what an
// agent sends whenever the user writes after a tool result. It reports
// whether it changed anything; the messages are edited in place.
func withoutSilentAssistants(msgs []map[string]any) ([]map[string]any, bool) {
	if !slices.ContainsFunc(msgs, silentAssistant) {
		return msgs, false
	}
	out := make([]map[string]any, 0, len(msgs))
	dropped := false
	for _, m := range msgs {
		if silentAssistant(m) {
			dropped = true
			continue
		}
		if n := len(out); dropped && n > 0 && m["role"] == "user" && out[n-1]["role"] == "user" {
			if joined, ok := joinedUserContent(out[n-1]["content"], m["content"]); ok {
				prev := maps.Clone(out[n-1])
				prev["content"] = joined
				out[n-1] = prev
				continue
			}
		}
		dropped = false
		out = append(out, m)
	}
	return out, true
}

// joinedUserContent is two user messages' content as one: two texts with a
// blank line between them, else the parts of the first and then the
// second's, a text becoming a text part.
func joinedUserContent(a, b any) (any, bool) {
	as, aText := a.(string)
	bs, bText := b.(string)
	if aText && bText {
		return as + "\n\n" + bs, true
	}
	parts := func(c any) ([]any, bool) {
		switch c := c.(type) {
		case string:
			if strings.TrimSpace(c) == "" {
				return nil, true
			}
			return []any{map[string]any{"type": "text", "text": c}}, true
		case []any:
			return c, true
		case []map[string]any:
			out := make([]any, len(c))
			for i, p := range c {
				out[i] = p
			}
			return out, true
		}
		return nil, false
	}
	ap, aok := parts(a)
	bp, bok := parts(b)
	if !aok || !bok {
		return nil, false
	}
	return append(slices.Clip(ap), bp...), true
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
	if !bodyHas(body, `"effort"`) {
		return body
	}
	var v struct {
		ReasoningEffort *string        `json:"reasoning_effort"`
		Thinking        map[string]any `json:"thinking"`
	}
	if decodeJSON(body, &v) != nil || v.ReasoningEffort != nil {
		return body
	}
	effort, _ := v.Thinking["effort"].(string)
	if effort == "" {
		return body
	}
	delete(v.Thinking, "effort")
	return withFields(body, map[string]any{"reasoning_effort": effort, "thinking": v.Thinking})
}

// cacheKeyCamel names OpenCode 1's camelCase promptCacheKey, which it
// writes into a Chat body when its provider sets setCacheKey (#5468), as
// the prompt_cache_key every Chat API reads: a key of the request's own,
// for every route, and no field an upstream doesn't know. A
// prompt_cache_key the body has already is the one kept.
func cacheKeyCamel(body []byte) []byte {
	if !bodyHas(body, `"promptCacheKey"`) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil {
		return body
	}
	key, ok := m["promptCacheKey"]
	if !ok {
		return body
	}
	delete(m, "promptCacheKey")
	if _, has := m["prompt_cache_key"]; !has && key != nil {
		m["prompt_cache_key"] = key
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false) // <, > and & as the agent wrote them
	if enc.Encode(m) != nil {
		return body
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n"))
}

// thinkingConfigField is Gemini's thinking_config as unfit remembers a
// provider that refused it.
const thinkingConfigField = "thinking_config"

// gemini3Levels are the levels Gemini 3 thinks at, of which a model may
// take only some (gemini-3.8-flash has no minimal): those it is asked at
// where its own aren't known.
var gemini3Levels = []string{"minimal", "low", "medium", "high"}

// geminiLevelRefused is Gemini turning away a thinking level the model
// hasn't, which names thinking but isn't thinking_config turned away
// (refusesThinkingConfig): gemini-3.8-flash's 400 "Thinking level MINIMAL
// is not supported for this model. Please retry with other thinking level."
// at AI Studio's OpenAI-compatible API, and "Thinking level is unsupported:
// THINKING_LEVEL_MINIMAL" at Vertex AI's, for reasoning_effort minimal and
// thinking_level minimal alike; and gemini-3.1-pro-preview's
// "thinking_level MINIMAL is not supported by this model" at its
// generateContent.
var geminiLevelRefused = lazyre.New(`(?i)thinking[ _]level\W+(?:(minimal|low|medium|high)\W+is not supported|is unsupported\W+thinking_level_(minimal|low|medium|high))`)

// geminiRefusedLevel is the thinking level Gemini turned away in body
// (geminiLevelRefused), or "".
func geminiRefusedLevel(body []byte) string {
	m := geminiLevelRefused.FindSubmatch(body)
	if m == nil {
		return ""
	}
	return strings.ToLower(string(m[1]) + string(m[2]))
}

// refusesThinkingConfig recognizes an upstream turning a request away for
// the thinking_config it was sent, by its error naming it.
func refusesThinkingConfig(status int, body []byte) bool {
	if !badRequest(status) {
		return false
	}
	b := bytes.ToLower(body)
	return bodyHas(b, "extra_body") || bodyHas(b, "thinking") || bodyHas(b, "include_thoughts")
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
	// doesn't: DeepSeek's hits, Moonshot's cached_tokens, Mistral's
	// num_cached_tokens (its older top-level count, which its API still
	// sends beside or instead of prompt_tokens_details, counted in
	// prompt_tokens as OpenAI's is), and Anthropic's own names from relays
	// in front of Claude
	PromptCacheHitTokens     int `json:"prompt_cache_hit_tokens"`
	CachedTokens             int `json:"cached_tokens"`
	NumCachedTokens          int `json:"num_cached_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	// Credit is what WorkBuddy says the request cost in its plan's credits
	Credit *float64 `json:"credit"`
	// what a plugin's step collected of its conversation's earlier steps,
	// which their own replies couldn't count (the Cursor plugin's usage
	// events that showed late, yetone/magpie#1053): billed with the step,
	// never its prompt, which a client reads as how full its context is
	MagpieDeferred *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"magpie_deferred,omitempty"`
	// MagpieCacheUnknown: a plugin's usage saying its vendor gave no cache
	// counts, as aUsage's CacheUnknown, so the zero is not a miss: Trae's
	// token_usage naming no cache field (yetone/magpie#3991, #3979).
	// It is read, never told: chat() leaves it out.
	MagpieCacheUnknown bool `json:"magpie_cache_unknown"`
}

func (u cUsage) usage() Usage {
	out := Usage{Output: u.CompletionTokens, cacheUnknown: u.MagpieCacheUnknown}
	if d := u.PromptTokensDetails; d != nil {
		out.CacheRead, out.CacheWrite = d.CachedTokens, d.CacheWriteTokens
	}
	for _, n := range []int{u.PromptCacheHitTokens, u.CachedTokens, u.NumCachedTokens} {
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
	if c := u.Credit; c != nil && *c >= 0 {
		out.Credits = c
	}
	if d := u.MagpieDeferred; d != nil && d.PromptTokens+d.CompletionTokens > 0 {
		out.Deferred = &Usage{Input: max(d.PromptTokens-d.CachedTokens-d.CacheWriteTokens, 0), Output: d.CompletionTokens,
			CacheRead: d.CachedTokens, CacheWrite: d.CacheWriteTokens}
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
//
// Events give a call's arguments to the call started last, but Chat may
// send the fragments of several calls by turns (index 0, 1, 0, 1…): read
// as one call after another, two calls came out as six, the last four
// with no name and pieces of the arguments (mysterious_dove). So the open
// call streams as it comes, and a call that starts while the open one's
// arguments aren't yet whole JSON is held, by its index, and given whole,
// in index order, when the reply ends.
type chatDecoder struct {
	started   bool
	tool      int       // index of the open tool call, -1 for none
	toolID    string    // id of the open tool call, as some relays repeat it on every fragment
	toolWhole jsonWhole // where the open call's arguments stand in their JSON
	toolDone  bool      // they are whole JSON: a call that starts now comes after it
	held      []*heldCall
	choice    string // index of the first choice seen; an empty string means none yet
	// Gemini's OpenAI-compatible API, asked for thoughts, may give them
	// in the text as a leading <thought>…</thought>, and MiniMax's Chat
	// API (#1267), like the open models served without a reasoning
	// parser, as a leading <think>…</think>: lead holds the text while it
	// could still be such a tag's start, thought is being inside one, and
	// thoughtClose is the tag that ends it
	lead         string
	thought      bool
	thoughtClose string
	// code is where the thinking is in Markdown code, where a close tag is
	// an example written out, not the close (#2634: "使用 `<think>…</think>`
	// 标签" cut the reply at the example's close)
	code mdCode
	past bool // the reply's text has begun; no tag is looked for now
	// trim is a thinking block just closed: the line breaks after its
	// close are left out of the answer, in whatever chunk they come
	trim bool
}

// leadSpace is the white space a leading block of thinking may come
// after: a relay writing Windows line ends puts "\r\n" before its <think>
// (grok2api_python's grok-3-search: "\r\n<think>…</think>\r\n").
const leadSpace = " \t\r\n"

// thoughtTags are the tags a leading block of thinking comes between in a
// Chat reply's text, by its opening tag.
var thoughtTags = map[string]string{"<thought>": "</thought>", "<thinking>": "</thinking>", "<think>": "</think>"}

// text sends a piece of the reply's text, a leading <thought>, <thinking> or
// <think> block of it as thinking.
func (d *chatDecoder) text(s string, emit func(Event)) {
	if !d.past && !d.thought {
		d.lead += s
		lead := strings.TrimLeft(d.lead, leadSpace)
		for open := range thoughtTags {
			if len(lead) < len(open) && strings.HasPrefix(open, lead) {
				return
			}
		}
		open := ""
		for o := range thoughtTags {
			if strings.HasPrefix(lead, o) {
				open = o
			}
		}
		if open == "" {
			d.past = true
			s, d.lead = d.lead, ""
			emit(Event{Kind: KText, Text: s})
			return
		}
		s, d.lead, d.thought, d.thoughtClose = strings.TrimPrefix(lead, open), "", true, thoughtTags[open]
	}
	thoughtClose := d.thoughtClose
	if d.thought {
		s = d.lead + s
		d.lead = ""
		if i, keep := d.code.find(s, thoughtClose, false); i >= 0 {
			if i > 0 {
				emit(Event{Kind: KThink, Text: s[:i]})
			}
			d.thought, d.past, d.trim = false, true, true
			s = s[i+len(thoughtClose):]
		} else {
			// the end of it may be the close tag, or a run of backticks,
			// begun
			if t := s[:len(s)-keep]; t != "" {
				emit(Event{Kind: KThink, Text: t})
			}
			d.lead = s[len(s)-keep:]
			return
		}
	}
	if d.trim {
		if s = strings.TrimLeft(s, "\r\n"); s != "" {
			d.trim = false
		}
	}
	if s != "" {
		emit(Event{Kind: KText, Text: s})
	}
}

// mdCode follows whether text read piece by piece is in Markdown code: an
// inline span, taken to end with its line, or a fenced block.
type mdCode struct {
	span  int  // the backticks the open inline span began with; 0 for none
	fence int  // the backticks the open fenced block began with; 0 for none
	mid   bool // past the line's leading spaces
}

// find gives where close is first outside code in s, read on from the
// pieces before, or -1 and how many bytes at the end of s are held back
// for the next piece: a close tag or a run of backticks that may go on
// there. Unless last, that is; then nothing is held.
func (m *mdCode) find(s, close string, last bool) (at, hold int) {
	for j := 0; j < len(s); {
		switch c := s[j]; {
		case c == '`':
			k := j
			for k < len(s) && s[k] == '`' {
				k++
			}
			if k == len(s) && !last {
				return -1, len(s) - j
			}
			switch n := k - j; {
			case m.fence > 0:
				if !m.mid && n >= m.fence {
					m.fence = 0
				}
			case m.span > 0:
				if n == m.span {
					m.span = 0
				}
			case !m.mid && n >= 3:
				m.fence = n
			default:
				m.span = n
			}
			m.mid = true
			j = k
		case c == '\n':
			m.span, m.mid = 0, false
			j++
		case c == ' ' || c == '\t':
			j++
		case c == '<' && m.span == 0 && m.fence == 0:
			if strings.HasPrefix(s[j:], close) {
				return j, 0
			}
			if !last && strings.HasPrefix(close, s[j:]) {
				return -1, len(s) - j
			}
			m.mid = true
			j++
		default:
			m.mid = true
			j++
		}
	}
	return -1, 0
}

// afterThought is t without a leading thinking block (any of thoughtTags,
// as a streamed reply's is read), a whole reply's thinking where its API
// leaves it in the text; t when it has none. No opening tag is a prefix of
// another's whole tag, so at most one matches.
func afterThought(t string) string {
	lead := strings.TrimLeft(t, leadSpace)
	for open, end := range thoughtTags {
		rest, ok := strings.CutPrefix(lead, open)
		if !ok {
			continue
		}
		var m mdCode
		if i, _ := m.find(rest, end, true); i >= 0 {
			return strings.TrimSpace(rest[i+len(end):])
		}
		return t
	}
	return t
}

// markedThought sends a piece of text Gemini marked as a thought
// (extra_content.google.thought) as thinking. AI Studio's OpenAI-compatible
// API may still wrap it in <thought> tags, and mark only the chunk with the
// opening one: "<thought>…" marked, then "</thought>接下来我将调用 probe"
// not (#2098). The tags are read as text's are, so that the close is
// found in the unmarked chunk and left out of the answer; a marked chunk
// that could be no tag's start is thinking as it is.
func (d *chatDecoder) markedThought(s string, emit func(Event)) {
	if s == "" {
		return
	}
	tagged := d.thought || d.lead != ""
	if lead := strings.TrimLeft(s, leadSpace); !tagged && !d.past {
		tagged = lead == ""
		for open := range thoughtTags {
			tagged = tagged || strings.HasPrefix(lead, open) || strings.HasPrefix(open, lead)
		}
	}
	if !tagged {
		emit(Event{Kind: KThink, Text: s})
		return
	}
	d.text(s, func(ev Event) {
		if ev.Kind == KText {
			ev.Kind = KThink
		}
		emit(ev)
	})
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
		d.giveHeld(emit)
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
	if err := decodeJSON([]byte(data), &ch); err != nil {
		// an error in a shape of its own, {"error":"…"}, is still the
		// reply's end, not a chunk to pass over (#1933)
		if e, ok := carriedError(data); ok {
			emit(e)
		}
		return nil
	}
	if ch.Error != nil {
		code, status, said := filterRefused(data)
		emit(Event{Kind: KError, Text: cmp.Or(ch.Error.Message, streamErrorText(data)), Code: code, Status: status, Said: said})
		return nil
	}
	if !d.started {
		d.started, d.tool = true, -1
		// most streams count the prompt only at their end, but one that
		// counts it on its first chunk tells it at the start, as
		// Anthropic's message_start does (#1887)
		var prompt Usage
		if ch.Usage != nil && ch.Usage.PromptTokens > 0 {
			u := ch.Usage.usage()
			prompt = Usage{Input: u.Input, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}
		}
		emit(Event{Kind: KStart, MsgID: ch.ID, Model: ch.Model, Usage: prompt})
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
			decodeJSON(c.Delta.Content, &content)
		}
		if t != "" {
			emit(Event{Kind: KThink, Text: t})
			if !d.thought && d.lead == "" {
				// a reply whose thinking has a field of its own doesn't
				// also give it in its text: a <think> there is the
				// model's words (#2634)
				d.past = true
			}
		}
		if c.Delta.ExtraContent.Google.Thought {
			d.markedThought(content, emit)
		} else if content != "" {
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
			d.toolCall(idx, tc, emit)
		}
		if c.FinishReason != "" {
			d.end(emit)
			d.giveHeld(emit)
			emit(Event{Kind: KStop, Stop: stopFromChat(c.FinishReason)})
		}
	}
	if ch.Usage != nil && (ch.Usage.PromptTokens > 0 || ch.Usage.CompletionTokens > 0 || ch.Usage.MagpieDeferred != nil || ch.Usage.Credit != nil) {
		emit(Event{Kind: KUsage, Usage: ch.Usage.usage()})
	}
	return nil
}

// heldCall is a tool call whose fragments came by turns with another's,
// held until the reply ends.
type heldCall struct {
	index    int
	id, sent string // the id the upstream gave, and the one the client is given
	name     string
	args     strings.Builder
}

// toolCall reads one fragment of a tool call, the call at index idx. A
// fragment carrying the id its call already goes by (some relays repeat it
// on every fragment, where the spec sends it only on the first) or none
// continues the latest call of its index; a new id or a new index starts
// the next one.
func (d *chatDecoder) toolCall(idx int, tc cToolCall, emit func(Event)) {
	args := tc.Function.Arguments
	newID := func(id string) bool { return tc.ID != "" && tc.ID != id }
	for _, h := range slices.Backward(d.held) {
		if h.index == idx {
			if newID(h.id) {
				break
			}
			h.args.WriteString(args)
			return
		}
	}
	if d.tool >= 0 && idx == d.tool && !newID(d.toolID) {
		d.toolArgs(args, emit)
		return
	}
	// a call Gemini signed goes to the client with an id carrying the
	// signature, which comes back with it
	sent := signedID(tc.ID, googleSignature(tc.ExtraContent))
	if d.tool >= 0 && (!d.toolDone || len(d.held) > 0) {
		h := &heldCall{index: idx, id: tc.ID, sent: sent, name: tc.Function.Name}
		h.args.WriteString(args)
		d.held = append(d.held, h)
		return
	}
	d.tool, d.toolID, d.toolWhole, d.toolDone = idx, tc.ID, jsonWhole{}, false
	emit(Event{Kind: KToolStart, ID: sent, Name: tc.Function.Name})
	d.toolArgs(args, emit)
}

// toolArgs gives a piece of the open call's arguments.
func (d *chatDecoder) toolArgs(args string, emit func(Event)) {
	if args == "" {
		return
	}
	d.toolDone = d.toolWhole.feed(args)
	emit(Event{Kind: KToolArgs, Text: args})
}

// giveHeld gives the calls held, each whole, in index order.
func (d *chatDecoder) giveHeld(emit func(Event)) {
	if len(d.held) == 0 {
		return
	}
	slices.SortStableFunc(d.held, func(a, b *heldCall) int { return a.index - b.index })
	for _, h := range d.held {
		emit(Event{Kind: KToolStart, ID: h.sent, Name: h.name})
		if h.args.Len() > 0 {
			emit(Event{Kind: KToolArgs, Text: h.args.String()})
		}
	}
	d.held, d.tool, d.toolID, d.toolWhole, d.toolDone = nil, -1, "", jsonWhole{}, false
}

// chatChoiceIndex accepts numeric indexes and numeric strings without
// changing how a missing or unreadable index is handled. JSON numbers such
// as 1.0 and 1 are the same choice.
func chatChoiceIndex(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var text string
	if decodeJSON(raw, &text) == nil {
		raw = json.RawMessage(text)
	}
	var n json.Number
	if decodeJSON(raw, &n) != nil {
		return "", false
	}
	index, err := n.Int64()
	if err != nil {
		var f float64
		if decodeJSON(raw, &f) != nil || f < 0 || f != float64(int64(f)) {
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
	case "content_filter", "sensitive": // "sensitive": Zhipu's filter
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
		typ := "api_error"
		if ev.Status == http.StatusRequestEntityTooLarge && ev.Code != "context_length_exceeded" {
			// the same type a 413 JSON body carries (writeErrorSaid)
			typ = "request_too_large"
		}
		failed := map[string]any{"message": ev.Text, "type": typ}
		if ev.Code != "" {
			failed["code"] = ev.Code // preserve the upstream error type, including refusals
		}
		saidError(failed, ev.Said)
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
			if p.Text == "" && p.Signature != "" {
				continue // only Gemini's signature on its text, which is Gemini's
			}
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
