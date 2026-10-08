package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
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
	Namespace string          `json:"namespace,omitempty"`
	Arguments rArgs           `json:"arguments,omitempty"`
	Input     string          `json:"input,omitempty"` // custom_tool_call's
	Output    json.RawMessage `json:"output,omitempty"`
	Status    string          `json:"status,omitempty"`
	// tool_search_output: the tools Codex's search found, which the model
	// may call from then on
	Tools []rTool `json:"tools,omitempty"`
	// reasoning
	Summary          []rText `json:"summary,omitempty"`
	EncryptedContent string  `json:"encrypted_content,omitempty"`
	// web_search_call
	Action *struct {
		Query   string `json:"query"`
		Sources []struct {
			URL string `json:"url"`
		} `json:"sources"`
	} `json:"action,omitempty"`
}

// rArgs is a call's arguments: a JSON string on a function_call, an object
// on Codex's tool_search_call.
type rArgs string

func (a *rArgs) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*a = rArgs(s)
	} else if string(b) != "null" {
		*a = rArgs(b)
	}
	return nil
}

type rText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type rTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
	Tools       []rTool         `json:"tools,omitempty"` // a namespace's
	Execution   string          `json:"execution,omitempty"`
	Format      *rFormat        `json:"format,omitempty"` // a custom tool's
}

// rFormat is what a custom (freeform) tool's input is: "text", or a
// "grammar" in a syntax ("lark", "regex") with its definition.
type rFormat struct {
	Type       string `json:"type"`
	Syntax     string `json:"syntax,omitempty"`
	Definition string `json:"definition,omitempty"`
}

// A custom tool takes free text, not JSON: Codex's apply_patch and, for
// the models its catalog puts in code mode (gpt-6.1-sol, gpt-6-luna…),
// exec, through which every other tool of Codex's is called (#534: offered
// none, the model said it had only the collaboration and wait tools). A
// model magpie translates for is offered it as a function taking the text
// as its one argument, and its call goes back to Codex as the
// custom_tool_call Codex runs, the text as its input.

// customSchema is the arguments a custom tool is offered as taking.
var customSchema = json.RawMessage(`{"type":"object","properties":{"input":{"type":"string","description":"The tool's free-form input, the whole of it, exactly as it is to be run."}},"required":["input"],"additionalProperties":false}`)

// customDescription is a custom tool's description as a function's: its
// own, what its input is, and the grammar the input follows.
func customDescription(t rTool) string {
	d := strings.TrimSpace(t.Description)
	if d != "" {
		d += "\n\n"
	}
	d += "Put the tool's whole free-form input, as it is to be run, in the `input` string; it is passed on as written, so it is not itself JSON."
	if f := t.Format; f != nil && f.Type == "grammar" && strings.TrimSpace(f.Definition) != "" {
		syntax := f.Syntax
		if syntax == "" {
			syntax = "the"
		}
		d += "\n\nThe input follows this " + syntax + " grammar:\n" + strings.TrimSpace(f.Definition)
	}
	return d
}

// customInput is the free-form input of a call the model made to a custom
// tool offered as a function: its input argument, else what it gave.
func customInput(args string) string {
	var a struct {
		Input *string `json:"input"`
	}
	if json.Unmarshal([]byte(args), &a) == nil && a.Input != nil {
		return *a.Input
	}
	var str string
	if json.Unmarshal([]byte(args), &str) == nil {
		return str
	}
	return args
}

// toolSearch is Codex's tool search. With it Codex names its MCP tools (the
// ChatGPT apps' among them) and its sub-agent tools only in the search's
// description, and hands the model the ones it searched for in a
// tool_search_output, instead of every schema on every request (#258).
// Codex runs the search itself ("execution": "client"): a model magpie
// translates for is offered it as the function it is, and its call goes
// back to Codex as the tool_search_call Codex runs.
const toolSearch = "tool_search"

// searchFound is what a model reads of a tool search's result: the tools it
// may call now, by the names it is offered them under.
func searchFound(tools []rTool) string {
	var names []string
	for _, t := range tools {
		switch t.Type {
		case "function":
			names = append(names, t.Name)
		case "namespace":
			for _, nt := range t.Tools {
				if nt.Type == "function" {
					names = append(names, flatName(t.Name, nt.Name))
				}
			}
		}
	}
	if len(names) == 0 {
		return "No tools matched the search."
	}
	return "These tools are now available to call: " + strings.Join(names, ", ")
}

// flatName is the name a namespaced tool is offered to a model under,
// namespace__name, the same a Grok subscription is offered it under.
// unsealed is a tool's parameters without the "encrypted" marks Codex puts
// on some (spawn_agent's message): a translated request's calls go back to
// Codex as unsealed (callTo's encrypted_function_args), so an upstream that
// honours the mark must not seal them (#613).
func unsealed(schema json.RawMessage) json.RawMessage {
	if !bytes.Contains(schema, []byte(`"encrypted"`)) {
		return schema
	}
	var v any
	if json.Unmarshal(schema, &v) != nil {
		return schema
	}
	var strip func(any)
	strip = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if _, ok := x["encrypted"].(bool); ok {
				delete(x, "encrypted")
			}
			for _, c := range x {
				strip(c)
			}
		case []any:
			for _, c := range x {
				strip(c)
			}
		}
	}
	strip(v)
	out, err := marshalPlain(v)
	if err != nil {
		return schema
	}
	return out
}

func flatName(namespace, name string) string {
	return provider.FlatName(namespace, name)
}

type rRequest struct {
	Model             string          `json:"model"`
	Instructions      string          `json:"instructions,omitempty"`
	Input             json.RawMessage `json:"input"`
	Tools             []rTool         `json:"tools,omitempty"`
	ToolChoice        json.RawMessage `json:"tool_choice,omitempty"`
	MaxOutputTokens   int             `json:"max_output_tokens,omitempty"`
	Temperature       *float64        `json:"temperature,omitempty"`
	TopP              *float64        `json:"top_p,omitempty"`
	Stream            bool            `json:"stream,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	ServiceTier       string          `json:"service_tier,omitempty"`
	PromptCacheKey    string          `json:"prompt_cache_key,omitempty"`
	Include           []string        `json:"include,omitempty"`
	ClientMetadata    json.RawMessage `json:"client_metadata,omitempty"`
	Text              json.RawMessage `json:"text,omitempty"`
	Reasoning         *struct {
		Effort  string `json:"effort,omitempty"`
		Summary string `json:"summary,omitempty"`
	} `json:"reasoning,omitempty"`
}

// sentRaw is a raw field the client sent, unless it sent null.
func sentRaw(v json.RawMessage) json.RawMessage {
	if t := strings.TrimSpace(string(v)); t == "" || t == "null" {
		return nil
	}
	return v
}

func parseResponses(body []byte) (*Request, error) {
	// Responses Lite's tools, sent as the first input item (#350)
	body = liftAdditionalTools(body)
	var q rRequest
	if err := json.Unmarshal(body, &q); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Model: q.Model, System: q.Instructions, MaxTokens: q.MaxOutputTokens, Temp: q.Temperature,
		TopP: q.TopP, Stream: q.Stream, Parallel: q.ParallelToolCalls, Fast: q.ServiceTier == "priority", CacheKey: q.PromptCacheKey, Include: q.Include,
		ClientMetadata: sentRaw(q.ClientMetadata), Text: sentRaw(q.Text)}
	if q.Reasoning != nil {
		r.Effort = effortOf(q.Reasoning.Effort)
		r.Thinking = true
		r.ThinkOff = strings.EqualFold(strings.TrimSpace(q.Reasoning.Effort), "none")
	}
	var found []rTool // what Codex's tool searches found
	var s string
	if json.Unmarshal(q.Input, &s) == nil {
		r.Messages = append(r.Messages, Message{Role: "user", Parts: []Part{{Kind: Text, Text: s}}})
	} else {
		var items []rItem
		if err := json.Unmarshal(q.Input, &items); err != nil {
			return nil, fmt.Errorf("invalid input: %v", err)
		}
		// replied: the conversation has had a turn answered. Codex adds a
		// developer message where its context changed (world state, settings,
		// a model switch) and keeps it there; lifted into the system prompt it
		// changed the prompt's very start, so the whole conversation was
		// written to the vendor's cache again (Anthropic's system comes
		// first; a Claude subscription's waiting run is found by it) (#502)
		replied := false
		var rawItems []json.RawMessage // decoded only for native standalone outputs
		for i, it := range items {
			if it.Role == "assistant" || strings.HasPrefix(it.Type, "function_call") || strings.HasPrefix(it.Type, "custom_tool_call") || strings.HasPrefix(it.Type, "tool_search") || it.Type == "reasoning" {
				replied = true
			}
			switch {
			case it.Type == "message" || (it.Type == "" && it.Role != ""):
				role := "user"
				if it.Role == "assistant" {
					role = "assistant"
				}
				parts := responsesParts(it.Content)
				if (it.Role == "system" || it.Role == "developer") && replied {
					// in place, told as the system's, not the user's words
					if t := text(parts); t != "" {
						r.Messages = append(r.Messages, Message{Role: "user", Parts: []Part{{Kind: Text, Text: "<system-reminder>\n" + t + "\n</system-reminder>"}}})
					}
					continue
				}
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
			case it.Type == "agent_message":
				// MultiAgentV2 hands a subagent its task, and agents their
				// messages to each other, as this item: what it says is the
				// user's turn for the agent that receives it.
				if parts := responsesParts(it.Content); len(parts) > 0 {
					r.Messages = append(r.Messages, Message{Role: "user", Parts: parts})
				}
			case it.Type == "function_call":
				name := it.Name
				if it.Namespace != "" {
					name = flatName(it.Namespace, it.Name)
				}
				r.Messages = append(r.Messages, Message{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: it.CallID, Name: name, Args: parseArgs(string(it.Arguments))}}})
			case it.Type == "custom_tool_call":
				name := it.Name
				if it.Namespace != "" {
					name = flatName(it.Namespace, it.Name)
				}
				args, _ := json.Marshal(map[string]string{"input": it.Input})
				r.Messages = append(r.Messages, Message{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: it.CallID, Name: name, Args: args}}})
			case it.Type == "tool_search_call":
				r.Messages = append(r.Messages, Message{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: it.CallID, Name: toolSearch, Args: parseArgs(string(it.Arguments))}}})
			case it.Type == "tool_search_output":
				found = append(found, it.Tools...)
				r.Messages = append(r.Messages, Message{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: it.CallID, Text: searchFound(it.Tools)}}})
			case it.Type == "function_call_output" || it.Type == "custom_tool_call_output":
				out, images := toolOutput(it.Output)
				part := Part{Kind: ToolResult, CallID: it.CallID, Text: out, Images: images}
				if it.CallID == "" {
					// Native account requests may still need translation to
					// collect a streamed response for a non-streaming client.
					// Retain the original notification, including absent IDs.
					if rawItems == nil {
						_ = json.Unmarshal(q.Input, &rawItems)
					}
					_ = json.Unmarshal(rawItems[i], &part.Standalone)
				}
				r.Messages = append(r.Messages, Message{Role: "user", Parts: []Part{part}})
			case it.Type == "reasoning":
				// the reasoning itself when the item carries it, else its
				// summary (all magpie gives a client of a translated reply)
				var b strings.Builder
				var content []rText
				_ = json.Unmarshal(it.Content, &content)
				for _, c := range content {
					if c.Type == "reasoning_text" {
						b.WriteString(c.Text)
					}
				}
				if b.Len() == 0 {
					for _, s := range it.Summary {
						b.WriteString(s.Text)
					}
				}
				if b.Len() > 0 {
					r.Messages = append(r.Messages, Message{Role: "assistant", Parts: []Part{{Kind: Thinking, Text: b.String()}}})
				}
			}
		}
	}
	r.Messages = mergeTurns(r.Messages)
	sent := len(q.Tools)
	offer := func(i int, t Tool, ns nsTool) {
		// a tool searched for twice, or sent as well, is offered once
		if i >= sent && slices.ContainsFunc(r.Tools, func(o Tool) bool { return o.Name == t.Name }) {
			return
		}
		if ns != (nsTool{}) {
			if r.Namespaced == nil {
				r.Namespaced = map[string]nsTool{}
			}
			r.Namespaced[t.Name] = ns
		}
		r.Tools = append(r.Tools, t)
	}
	// the tools Codex's searches found are offered after those it sent, as
	// Codex does not send them again
	for i, t := range append(q.Tools, found...) {
		if strings.HasPrefix(t.Type, "web_search") {
			r.WebSearch = true
		}
		switch t.Type {
		case "function":
			offer(i, Tool{Name: t.Name, Description: t.Description, Schema: unsealed(t.Parameters), Strict: t.Strict != nil && *t.Strict}, nsTool{})
		case "custom":
			offer(i, Tool{Name: t.Name, Description: customDescription(t), Schema: customSchema}, nsTool{Name: t.Name, Custom: true})
		case "namespace":
			// offered flat, as few models know namespaces; a call is given
			// its namespace back on the way out
			for _, nt := range t.Tools {
				flat := flatName(t.Name, nt.Name)
				switch nt.Type {
				case "function":
					offer(i, Tool{Name: flat, Description: nt.Description, Schema: unsealed(nt.Parameters), Strict: nt.Strict != nil && *nt.Strict}, nsTool{Namespace: t.Name, Name: nt.Name})
				case "custom":
					offer(i, Tool{Name: flat, Description: customDescription(nt), Schema: customSchema}, nsTool{Namespace: t.Name, Name: nt.Name, Custom: true})
				}
			}
		case toolSearch:
			if t.Execution == "client" {
				offer(i, Tool{Name: toolSearch, Description: t.Description, Schema: t.Parameters}, nsTool{Search: true})
			}
		}
	}
	var tc string
	if json.Unmarshal(q.ToolChoice, &tc) == nil {
		r.ToolChoice = tc
	} else {
		var o struct {
			Type  string `json:"type"`
			Name  string `json:"name"`
			Mode  string `json:"mode"`
			Tools []struct {
				Type      string `json:"type"`
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"tools"`
		}
		if json.Unmarshal(q.ToolChoice, &o) == nil {
			switch o.Type {
			case "allowed_tools":
				if o.Mode != "auto" && o.Mode != "required" {
					return nil, fmt.Errorf("invalid allowed_tools mode %q", o.Mode)
				}
				r.ToolChoice = o.Mode
				allowed := make(map[string]bool, len(o.Tools))
				webSearch := false
				for _, tool := range o.Tools {
					switch {
					case (tool.Type == "function" || tool.Type == "custom") && tool.Name != "":
						if tool.Namespace != "" {
							allowed[flatName(tool.Namespace, tool.Name)] = true
						} else {
							allowed[tool.Name] = true
						}
					case strings.HasPrefix(tool.Type, "web_search"):
						webSearch = true
					}
				}
				r.WebSearch = r.WebSearch && webSearch
				var tools []Tool
				for _, tool := range r.Tools {
					if allowed[tool.Name] {
						tools = append(tools, tool)
					}
				}
				r.Tools = tools
				for name := range r.Namespaced {
					if !allowed[name] {
						delete(r.Namespaced, name)
					}
				}
			case "function":
				if o.Name != "" {
					r.ToolChoice = "name:" + o.Name
				}
			}
		}
	}
	return r, nil
}

// orphanedToolOutputs turns a tool result with no call ID into a user message.
// Codex uses standalone outputs for cross-thread notifications. Backends
// requiring paired outputs still need the delivered text, without a fake call ID.
func orphanedToolOutputs(body []byte) []byte {
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return body
	}
	var input []json.RawMessage
	if json.Unmarshal(q["input"], &input) != nil {
		return body
	}
	changed := false
	out := make([]json.RawMessage, 0, len(input))
	var pending []json.RawMessage
	inCalls, hadResults := false, false
	flush := func() {
		out = append(out, pending...)
		pending = nil
	}
	for _, raw := range input {
		var item struct {
			Type   string          `json:"type"`
			CallID string          `json:"call_id"`
			Output json.RawMessage `json:"output"`
		}
		if json.Unmarshal(raw, &item) != nil {
			flush()
			inCalls = false
			out = append(out, raw)
			continue
		}
		isOutput := item.Type == "function_call_output" || item.Type == "custom_tool_call_output"
		if !isOutput || item.CallID != "" {
			switch item.Type {
			case "function_call", "custom_tool_call", "tool_search_call":
				if hadResults {
					flush()
				}
				inCalls, hadResults = true, false
			case "function_call_output", "custom_tool_call_output", "tool_search_output":
				// Keep a run of tool results directly after its calls.
				hadResults = true
			default:
				flush()
				inCalls = false
			}
			out = append(out, raw)
			continue
		}
		text, images := toolOutput(item.Output)
		content := []map[string]any{}
		if text != "" {
			content = append(content, map[string]any{"type": "input_text", "text": text})
		}
		for _, image := range images {
			content = append(content, map[string]any{"type": "input_image", "image_url": dataURL(image)})
		}
		if len(content) == 0 {
			content = append(content, map[string]any{"type": "input_text", "text": "Tool result received."})
		}
		message, _ := marshalPlain(map[string]any{"type": "message", "role": "user", "content": content})
		if inCalls {
			// Chat rejects a user message between tool_calls and results,
			// including between the results of parallel calls.
			pending = append(pending, message)
		} else {
			out = append(out, message)
		}
		changed = true
	}
	flush()
	if !changed {
		return body
	}
	q["input"], _ = marshalPlain(out)
	encoded, err := marshalPlain(q)
	if err != nil {
		return body
	}
	return encoded
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

// replaysReasoning is whether model, served by host's Responses API, wants
// a turn's reasoning text back between tool calls (DeepSeek: "The
// reasoning_text in the thinking mode must be passed back", #388). OpenAI's,
// xAI's, Copilot's and Azure's read only their own sealed reasoning.
func replaysReasoning(model, host string) bool {
	return strings.Contains(strings.ToLower(model), "deepseek") &&
		!slices.Contains([]string{"chatgpt.com", "api.openai.com", "api.x.ai", "api.githubcopilot.com"}, host) &&
		!strings.HasSuffix(host, ".openai.azure.com")
}

// withReasoningText gives each reasoning item in a Responses request that
// carries no reasoning_text its summary as one, for an upstream that wants
// the reasoning back (replaysReasoning). Codex sends an item back as it got
// it, and one from a turn magpie translated, or another provider served,
// has only a summary: passed on as it is, DeepSeek refuses the whole
// request with "The reasoning_text in the thinking mode must be passed back
// to the API" (#1104). An item with neither is left as it is.
func withReasoningText(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"reasoning"`)) {
		return body
	}
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return body
	}
	var items []json.RawMessage
	if json.Unmarshal(q["input"], &items) != nil {
		return body
	}
	changed := false
	for i, raw := range items {
		var it struct {
			Type    string  `json:"type"`
			Summary []rText `json:"summary"`
			Content []rText `json:"content"`
		}
		if json.Unmarshal(raw, &it) != nil || it.Type != "reasoning" ||
			slices.ContainsFunc(it.Content, func(c rText) bool { return c.Type == "reasoning_text" && c.Text != "" }) {
			continue
		}
		var b strings.Builder
		for _, s := range it.Summary {
			b.WriteString(s.Text)
		}
		if b.Len() == 0 {
			continue
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		m["content"], _ = json.Marshal([]map[string]string{{"type": "reasoning_text", "text": b.String()}})
		if nb, err := json.Marshal(m); err == nil {
			items[i] = nb
			changed = true
		}
	}
	if !changed {
		return body
	}
	q["input"], _ = json.Marshal(items)
	nb, err := json.Marshal(q)
	if err != nil {
		return body
	}
	return nb
}

// buildResponses renders a request for a Responses upstream.
func buildResponses(r *Request, model, host string, rejectTemp bool) []byte {
	// A turn's reasoning goes back as a reasoning item, as a model that
	// thinks between tool calls wants it (DeepSeek: "The reasoning_text in
	// the thinking mode must be passed back", #388). Only a DeepSeek model
	// gets it: OpenAI's and those in front of it read only their own
	// sealed reasoning, and may refuse an item without it.
	replay := replaysReasoning(model, host)
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
			case File:
				t := "input_text"
				if m.Role == "assistant" {
					t = "output_text"
				}
				content = append(content, map[string]any{"type": t, "text": attachmentText(p)})
			case Image:
				if m.Role != "assistant" {
					content = append(content, map[string]any{"type": "input_image", "image_url": dataURL(p)})
				}
			case Thinking:
				if replay && p.Text != "" {
					flushMsg()
					input = append(input, map[string]any{"type": "reasoning", "summary": []any{},
						"content": []map[string]any{{"type": "reasoning_text", "text": p.Text}}})
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
				if p.Standalone != nil {
					input = append(input, p.Standalone)
					continue
				}
				var output any = p.Text
				if len(p.Images) > 0 {
					// an output can be a list of text and images
					var items []map[string]any
					if strings.TrimSpace(p.Text) != "" {
						items = append(items, map[string]any{"type": "input_text", "text": p.Text})
					}
					for _, im := range p.Images {
						items = append(items, map[string]any{"type": "input_image", "image_url": dataURL(im)})
					}
					output = items
				}
				input = append(input, map[string]any{"type": "function_call_output", "call_id": p.CallID, "output": output})
			}
		}
		flushMsg()
	}
	if input == nil {
		input = []map[string]any{}
	}
	out := map[string]any{"model": model, "input": input, "stream": r.Stream, "store": false}
	if len(r.ClientMetadata) > 0 {
		out["client_metadata"] = r.ClientMetadata
	}
	if len(r.Text) > 0 {
		out["text"] = r.Text
	}
	if r.CacheKey != "" {
		out["prompt_cache_key"] = r.CacheKey
	}
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
	// Fast goes to OpenAI's own backends; another's may not know the tier
	if r.Fast && (host == "chatgpt.com" || host == "api.openai.com") {
		out["service_tier"] = "priority"
	}
	if r.Effort != "" {
		out["reasoning"] = map[string]any{"effort": r.Effort, "summary": "auto"}
	} else if r.Thinking {
		out["reasoning"] = map[string]any{"summary": "auto"}
	}
	// the client's include goes on as the request would have without
	// magpie. Sealed reasoning is asked for only with reasoning, as Codex
	// asks for it: OpenAI refuses it of a model that doesn't reason. What
	// comes back sealed goes no further than magpie, and the input's sealed
	// reasoning isn't sent on this way, so no account or vendor is handed
	// another's to refuse (withoutRefused, sealedKinds, on a relay).
	var include []string
	for _, v := range r.Include {
		if v == "" || slices.Contains(include, v) || (v == "reasoning.encrypted_content" && out["reasoning"] == nil) {
			continue
		}
		include = append(include, v)
	}
	if len(r.Tools) > 0 || r.WebSearch {
		var tools []map[string]any
		for _, t := range r.Tools {
			// strict is said, as Codex says it: left out, the ChatGPT
			// backend holds the schema to strict mode's rules and refuses a
			// pattern with a lookaround (MiniMax Code's path, #383)
			tool := map[string]any{"type": "function", "name": t.Name, "description": t.Description, "strict": t.Strict}
			if len(t.Schema) > 0 {
				tool["parameters"] = t.Schema
			}
			tools = append(tools, tool)
		}
		if r.WebSearch {
			tools = append(tools, map[string]any{"type": "web_search"})
			// the pages it found, which a client of another protocol is
			// told of (OpenAI's option; xAI's API isn't known to take it)
			if host != "api.x.ai" && !slices.Contains(include, "web_search_call.action.sources") {
				include = append(include, "web_search_call.action.sources")
			}
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
	if len(include) > 0 {
		out["include"] = include
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
		// what was written to the cache, which Codex reads too (#589)
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

// usage: input_tokens is the whole prompt, what was read from the cache and
// what was written to it among it.
func (u rUsage) usage() Usage {
	d := u.InputTokensDetails
	return Usage{Input: max(u.InputTokens-d.CachedTokens-d.CacheWriteTokens, 0), Output: u.OutputTokens,
		CacheRead: d.CachedTokens, CacheWrite: d.CacheWriteTokens, Reasoning: u.OutputTokensDetails.ReasoningTokens}
}

func (u Usage) responses() map[string]any {
	in := u.prompt()
	return map[string]any{"input_tokens": in, "output_tokens": u.Output, "total_tokens": in + u.Output,
		"input_tokens_details":  map[string]any{"cached_tokens": u.CacheRead, "cache_write_tokens": u.CacheWrite},
		"output_tokens_details": map[string]any{"reasoning_tokens": u.Reasoning}}
}

// responsesDecoder turns a Responses stream into events.
//
// A function call's arguments are given once the call is done, not as
// their deltas come: the deltas can leave out what the finished call holds
// (#613: Codex's spawn_agent came back with arguments {} where the request
// was translated, though the upstream's finished call had them), and what
// was sent of a call's arguments can't be taken back.
type responsesDecoder struct {
	started bool
	called  bool            // a function call was streamed
	calling bool            // a function call is open
	args    strings.Builder // the open call's argument deltas
	full    string          // the open call's arguments as its done events give them
}

// endCall gives the open call's arguments: the deltas, or what its done
// events give where that holds more.
func (d *responsesDecoder) endCall(emit func(Event)) {
	if !d.calling {
		return
	}
	d.calling = false
	args := pickArgs(d.args.String(), d.full)
	d.args.Reset()
	d.full = ""
	if args != "" {
		emit(Event{Kind: KToolArgs, Text: args})
	}
}

// pickArgs is the fuller of a call's arguments as streamed and as its done
// events give them, JSON first.
func pickArgs(streamed, done string) string {
	s, f := strings.TrimSpace(streamed), strings.TrimSpace(done)
	switch sv, fv := json.Valid([]byte(s)), json.Valid([]byte(f)); {
	case fv && (!sv || len(f) > len(s)):
		return done
	case sv || f == "":
		return streamed
	default:
		return done
	}
}

func (d *responsesDecoder) decode(data string, emit func(Event)) error {
	var ev struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
		Item  rItem  `json:"item"`
		// function_call_arguments.done's
		Arguments string `json:"arguments"`
		Response  struct {
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
		d.endCall(emit)
		if ev.Item.Type == "function_call" {
			d.called, d.calling = true, true
			emit(Event{Kind: KToolStart, ID: ev.Item.CallID, Name: ev.Item.Name})
		}
	case "response.output_text.delta":
		d.endCall(emit)
		emit(Event{Kind: KText, Text: ev.Delta})
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		d.endCall(emit)
		emit(Event{Kind: KThink, Text: ev.Delta})
	case "response.function_call_arguments.delta":
		if d.calling {
			d.args.WriteString(ev.Delta)
		} else {
			emit(Event{Kind: KToolArgs, Text: ev.Delta})
		}
	case "response.function_call_arguments.done":
		if d.calling && ev.Arguments != "" {
			d.full = ev.Arguments
		}
	case "response.output_item.done":
		if ev.Item.Type == "function_call" {
			if ev.Item.Arguments != "" {
				d.full = string(ev.Item.Arguments)
			}
			d.endCall(emit)
		}
		if a := ev.Item.Action; ev.Item.Type == "web_search_call" && a != nil && a.Query != "" {
			var hits []Hit
			for _, src := range a.Sources {
				if src.URL != "" {
					hits = append(hits, Hit{Title: src.URL, URL: src.URL})
				}
			}
			emit(Event{Kind: KSearch, Text: a.Query, Hits: hits})
		}
	case "response.completed", "response.incomplete", "response.failed":
		d.endCall(emit)
		if ev.Response.Error != nil {
			emit(Event{Kind: KError, Text: ev.Response.Error.Message, Code: refusedCode(data)})
			return nil
		}
		stop := "stop"
		if ev.Response.Status == "incomplete" {
			stop = "length"
			if ev.Response.IncompleteDetails != nil && ev.Response.IncompleteDetails.Reason == "content_filter" {
				stop = "filter"
			}
		} else if d.called {
			// the ChatGPT backend's completed response lists no output
			stop = "tool"
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
		d.endCall(emit)
		msg := ev.Message
		if ev.Error != nil {
			msg = ev.Error.Message
		}
		emit(Event{Kind: KError, Text: msg, Code: refusedCode(data)})
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
	named   map[string]nsTool // the request's namespaced tools
}

// callTo names the tool a function_call item is to as the client knows it:
// a namespaced tool by its name and namespace, not the flat name the model
// used.
func callTo(item map[string]any, name string, named map[string]nsTool) map[string]any {
	if q := named[name]; q.Search {
		// Codex runs its tool search itself, from the item it knows it by
		args, _ := item["arguments"].(string)
		delete(item, "name")
		item["type"], item["execution"], item["arguments"] = "tool_search_call", "client", parseArgs(args)
	} else if q.Custom {
		// a custom tool's call carries its free-form input, not arguments
		args, _ := item["arguments"].(string)
		delete(item, "arguments")
		item["type"], item["name"], item["input"] = "custom_tool_call", q.Name, ""
		if args != "" {
			item["input"] = customInput(args)
		}
		if q.Namespace != "" {
			item["namespace"] = q.Namespace
		}
	} else if q, ok := named[name]; ok {
		item["name"], item["namespace"] = q.Name, q.Namespace
		// Nothing magpie serves seals arguments. Codex reads a namespaced call
		// without this list as sealed: spawn_agent's message in MultiAgentV2
		// would reach the subagent as ciphertext, which is really plain text.
		item["encrypted_function_args"] = []any{}
	} else {
		item["name"] = name
	}
	return item
}

// itemPrefix is the prefix of a call item's id, by its type: OpenAI turns
// away a tool_search_call or custom_tool_call whose id isn't its own kind
// ("Invalid 'input[98].id': 'fc_…'. Expected an ID that begins with
// 'tsc'", openaiItemPrefix), and Codex hands the item back to it when the
// conversation goes there.
func itemPrefix(item map[string]any) string {
	if t, _ := item["type"].(string); openaiItemPrefix[t] != "" {
		return openaiItemPrefix[t]
	}
	return "fc_"
}

func (e *responsesEncoder) send(typ string, fields map[string]any) {
	fields["type"] = typ
	fields["sequence_number"] = e.seq
	e.seq++
	e.w.event(typ, fields)
}

func (e *responsesEncoder) response(status string, extra map[string]any) map[string]any {
	// a list, never null, before any item: Muse Code refuses a stream whose
	// response.created has output null (its nil slice in an any isn't nil)
	output := e.output
	if output == nil {
		output = []map[string]any{}
	}
	out := map[string]any{"id": e.id, "object": "response", "created_at": e.created, "status": status,
		"model": e.model, "output": output, "parallel_tool_calls": true, "tool_choice": "auto", "tools": []any{}}
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
		if q := e.named[p.Name]; !q.Search && !q.Custom {
			e.send("response.function_call_arguments.done", callTo(map[string]any{"item_id": e.itemID, "output_index": e.item, "call_id": p.ID, "arguments": args}, p.Name, e.named))
		}
		item = callTo(map[string]any{"id": e.itemID, "type": "function_call", "status": "completed", "call_id": p.ID, "arguments": args}, p.Name, e.named)
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
		item := callTo(map[string]any{"type": "function_call", "call_id": ev.ID, "arguments": ""}, ev.Name, e.named)
		e.openItem(ToolCall, itemPrefix(item), item)
		e.col.add(ev)
		return
	case KToolArgs:
		if e.open == ToolCall && ev.Text != "" {
			e.text.WriteString(ev.Text)
			// a custom tool's input is whole only once its arguments are:
			// it goes in the item when that is done
			if !e.named[e.col.last(ToolCall).Name].Custom {
				e.send("response.function_call_arguments.delta", map[string]any{"item_id": e.itemID, "output_index": e.item, "delta": ev.Text})
			}
		}
	case KError:
		e.closeItem()
		code := "server_error"
		if ev.Code != "" {
			code = ev.Code
		}
		e.send("response.failed", map[string]any{"response": e.response("failed", map[string]any{"error": map[string]any{"code": code, "message": ev.Text}})})
	}
	e.col.add(ev)
}

// keepalive is response.in_progress again: Codex's idle timeout counts
// events only, an SSE comment never reaching it, and skips this one.
func (e *responsesEncoder) keepalive() {
	if !e.started {
		e.start(Event{}) // response.created and response.in_progress
		return
	}
	e.send("response.in_progress", map[string]any{"response": e.response("in_progress", nil)})
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
func renderResponses(res Result, model string, named map[string]nsTool) []byte {
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
			item := callTo(map[string]any{"type": "function_call", "status": "completed",
				"call_id": id, "arguments": argsString(p)}, p.Name, named)
			item["id"] = itemPrefix(item) + newID()
			output = append(output, item)
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
	switch res.Stop {
	case "length":
		status, incomplete = "incomplete", map[string]any{"reason": "max_output_tokens"}
	case "filter":
		// as the stream says it (#248)
		status, incomplete = "incomplete", map[string]any{"reason": "content_filter"}
	}
	b, _ := json.Marshal(map[string]any{"id": id, "object": "response", "created_at": time.Now().Unix(), "status": status,
		"model": model, "output": output, "usage": res.Usage.responses(), "incomplete_details": incomplete,
		"parallel_tool_calls": true, "tool_choice": "auto", "tools": []any{}, "error": nil})
	return b
}
