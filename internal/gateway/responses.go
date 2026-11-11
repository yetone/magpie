package gateway

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/claudeseal"
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
	// configuration_update's: an effort changed mid-thread
	Reasoning *struct {
		Effort string `json:"effort"`
	} `json:"reasoning,omitempty"`
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
	if decodeJSON(b, &s) == nil {
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
	// ExternalWebAccess is a web_search tool's mode: false is Codex's
	// "cached", true its "live"
	ExternalWebAccess json.RawMessage `json:"external_web_access,omitempty"`
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
	if decodeJSON([]byte(args), &a) == nil && a.Input != nil {
		return *a.Input
	}
	var str string
	if decodeJSON([]byte(args), &str) == nil {
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
	if decodeJSON(schema, &v) != nil {
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

// asksSealing is a Responses request whose tools mark a parameter
// "encrypted" (MultiAgentV2's spawn_agent message): an upstream that
// honours the mark answers the call with that argument sealed.
func asksSealing(body []byte) bool {
	if !bytes.Contains(body, []byte(`"encrypted"`)) {
		return false
	}
	var q struct {
		Tools []struct {
			Parameters json.RawMessage `json:"parameters"`
			Tools      []struct {
				Parameters json.RawMessage `json:"parameters"`
			} `json:"tools"`
		} `json:"tools"`
	}
	if decodeJSON(body, &q) != nil {
		return false
	}
	var mark func(any) bool
	mark = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			if b, ok := x["encrypted"].(bool); ok && b {
				return true
			}
			for _, c := range x {
				if mark(c) {
					return true
				}
			}
		case []any:
			for _, c := range x {
				if mark(c) {
					return true
				}
			}
		}
		return false
	}
	marked := func(schema json.RawMessage) bool {
		var v any
		return bytes.Contains(schema, []byte(`"encrypted"`)) && decodeJSON(schema, &v) == nil && mark(v)
	}
	for _, t := range q.Tools {
		if marked(t.Parameters) {
			return true
		}
		for _, nt := range t.Tools {
			if marked(nt.Parameters) {
				return true
			}
		}
	}
	return false
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
	// PromptCacheRetention is OpenAI's "in_memory" or "24h": the client's
	// own say over how long its prompt stays cached (hourMarks)
	PromptCacheRetention string          `json:"prompt_cache_retention,omitempty"`
	Include              []string        `json:"include,omitempty"`
	ClientMetadata       json.RawMessage `json:"client_metadata,omitempty"`
	AccessPrograms       json.RawMessage `json:"access_programs,omitempty"`
	Text                 json.RawMessage `json:"text,omitempty"`
	Reasoning            *struct {
		Effort  string `json:"effort,omitempty"`
		Summary string `json:"summary,omitempty"`
	} `json:"reasoning,omitempty"`
}

// textFormat splits a Responses client's text into the rest of it and
// the format it asks the answer in (text.format), which is asked for again
// in each upstream's own words. A format of plain text stays where it was.
func textFormat(text json.RawMessage) (json.RawMessage, *Format) {
	var fields map[string]json.RawMessage
	if decodeJSON(text, &fields) != nil {
		return text, nil
	}
	f := openAIFormat(fields["format"])
	if f == nil {
		return text, nil
	}
	delete(fields, "format")
	if len(fields) == 0 {
		return nil, f
	}
	rest, _ := json.Marshal(fields)
	return rest, f
}

// sentRaw is a raw field the client sent, unless it sent null.
func sentRaw(v json.RawMessage) json.RawMessage {
	if t := strings.TrimSpace(string(v)); t == "" || t == "null" {
		return nil
	}
	return v
}

func parseResponses(body []byte) (*Request, error) { return readResponses(body, false) }

// readResponses is parseResponses; for facts (readFacts) it leaves out what
// they never read: the assistant's words, its calls' arguments and the
// tools' outputs, each decoded into strings of their own, and the copies
// of raw values a parse kept would need. Run on every request, a long
// Codex conversation's tool outputs were most of the parse (#3394).
func readResponses(body []byte, facts bool) (*Request, error) {
	return readResponsesFor(body, facts, false)
}

// readResponsesFor keeps instruction roles and positions on Responses-to-
// Responses turns. Other APIs keep their existing system/cache mapping.
func readResponsesFor(body []byte, facts, keepInstructions bool) (*Request, error) {
	decode := decodeJSON
	if facts {
		decode = decodeJSONView
	}
	// Responses Lite's tools, sent as the first input item (#350)
	body = liftAdditionalTools(body)
	var q rRequest
	if err := decode(body, &q); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Model: q.Model, System: q.Instructions, MaxTokens: q.MaxOutputTokens, Temp: q.Temperature,
		TopP: q.TopP, Stream: q.Stream, Parallel: q.ParallelToolCalls, Fast: q.ServiceTier == "priority" || q.ServiceTier == "ultrafast", Ultrafast: q.ServiceTier == "ultrafast", Tier: q.ServiceTier, CacheKey: q.PromptCacheKey, Include: q.Include,
		ClientMetadata: sentRaw(q.ClientMetadata), AccessPrograms: q.AccessPrograms}
	r.Text, r.Format = textFormat(sentRaw(q.Text))
	if q.Instructions != "" {
		r.systemParts = []string{q.Instructions}
	}
	// a Responses client's turns go to Claude with hour-long cache marks,
	// unless it asked for its prompt to be kept in memory only
	r.hourMarks = q.PromptCacheRetention != "in_memory"
	// the instruction roles the client sent stand where they stood, and go
	// back as they came (#2532)
	r.instructionsKept = keepInstructions
	if q.Reasoning != nil {
		r.Effort = effortOf(q.Reasoning.Effort)
		r.Thinking = true
		r.ThinkOff = strings.EqualFold(strings.TrimSpace(q.Reasoning.Effort), "none")
	}
	var found []rTool // what Codex's tool searches found
	var s string
	if decodeJSON(q.Input, &s) == nil {
		r.Messages = append(r.Messages, Message{Role: "user", Parts: []Part{{Kind: Text, Text: s}}})
	} else {
		var items []rItem
		if err := decode(q.Input, &items); err != nil {
			return nil, fmt.Errorf("invalid input: %v", err)
		}
		// replied: the conversation has had a turn answered. Codex adds a
		// developer message where its context changed (world state, settings,
		// a model switch) and keeps it there; lifted into the system prompt it
		// changed the prompt's very start, so the whole conversation was
		// written to the vendor's cache again (Anthropic's system comes
		// first; a Claude subscription's waiting run is found by it) (#502)
		replied := false
		var rawItems []json.RawMessage // decoded only for native standalone outputs and sealed reasoning
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
				var parts []Part
				if !facts || role != "assistant" {
					parts = responsesParts(it.Content)
				}
				if it.Role == "system" || it.Role == "developer" {
					for _, p := range parts {
						if p.Kind == Text && p.Text != "" {
							r.systemParts = append(r.systemParts, p.Text)
						}
					}
				}
				if keepInstructions && (it.Role == "system" || it.Role == "developer") {
					r.Messages = append(r.Messages, Message{Role: it.Role, Parts: parts})
					continue
				}
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
			case it.Type == "configuration_update":
				// the agent changed its effort mid-thread: the last
				// change is the one the turn is asked at (effortUpdated)
				if q.Reasoning != nil && it.Reasoning != nil && it.Reasoning.Effort != "" {
					r.Effort = effortOf(it.Reasoning.Effort)
					r.ThinkOff = strings.EqualFold(strings.TrimSpace(it.Reasoning.Effort), "none")
				}
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
			case (it.Type == "function_call_output" || it.Type == "custom_tool_call_output") && facts:
				r.Messages = append(r.Messages, Message{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: it.CallID}}})
			case it.Type == "function_call_output" || it.Type == "custom_tool_call_output":
				out, images := toolOutput(it.Output)
				part := Part{Kind: ToolResult, CallID: it.CallID, Text: out, Images: images}
				if it.CallID == "" {
					// Native account requests may still need translation to
					// collect a streamed response for a non-streaming client.
					// Retain the original notification, including absent IDs.
					if rawItems == nil {
						_ = decodeJSON(q.Input, &rawItems)
					}
					_ = decodeJSON(rawItems[i], &part.Standalone)
				}
				r.Messages = append(r.Messages, Message{Role: "user", Parts: []Part{part}})
			case it.Type == "reasoning":
				// the reasoning itself when the item carries it, else its
				// summary (all magpie gives a client of a translated reply)
				var b strings.Builder
				var content []rText
				_ = decodeJSON(it.Content, &content)
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
				part := Part{Kind: Thinking, Text: b.String()}
				if block, ours := claudeseal.Open(it.EncryptedContent); ours {
					// Claude's block, which magpie gave the client to carry
					// back: as Claude wrote it, and never to a Responses API
					// (withoutClaudeSeals). One that can't be read is its
					// summary, as before.
					part = claudePart(block, part)
				} else if it.EncryptedContent != "" {
					// sealed: the item goes back to a Responses API as it
					// came, id, summary and all (#1445)
					if rawItems == nil {
						_ = decodeJSON(q.Input, &rawItems)
					}
					if i < len(rawItems) {
						part.Sealed, part.SealedBy = rawItems[i], sealResponses
					}
				}
				if part.Text != "" || part.Sealed != nil || part.Signature != "" {
					r.Messages = append(r.Messages, Message{Role: "assistant", Parts: []Part{part}})
				}
			case it.Type == "web_search_call" && it.Action != nil && strings.TrimSpace(it.Action.Query) != "":
				// where a search ran in the turn: magpie's own search round
				// for a Claude reply is put back (searchRoundsBack), and
				// otherwise it goes, as before
				var hits []Hit
				for _, s := range it.Action.Sources {
					if s.URL != "" {
						hits = append(hits, Hit{URL: s.URL})
					}
				}
				r.Messages = append(r.Messages, Message{Role: "assistant", Parts: []Part{{Kind: Search, ID: it.ID, Text: it.Action.Query, Hits: hits}}})
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
			if len(t.ExternalWebAccess) > 0 {
				r.WebAccess = t.ExternalWebAccess
			}
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
	if decodeJSON(q.ToolChoice, &tc) == nil {
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
		if decodeJSON(q.ToolChoice, &o) == nil {
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
	r.Messages = searchRoundsBack(r.Messages, searchTool(r.Tools).Name)
	return r, nil
}

// searchRoundsBack puts back the search rounds magpie ran for a Claude
// reply (searchRounds), which its client keeps only as web_search_call
// items. Claude wrote the thinking before a search in a message that
// ended with magpie's web_search call, and what it wrote after the
// result in a message of its own; told as one message, the thinking
// blocks of the latest assistant message aren't where Claude wrote them,
// and an upstream that checks them refuses the turn: Claude on Bedrock
// answered 400 "thinking or redacted_thinking blocks in the latest
// assistant message cannot be modified", and Codex retried it until the
// stream gave up (oreoft on Discord). An assistant message with Claude's
// signed thinking is split at each search into the call and its result,
// the query and the pages found, as what the search said was never kept.
// A search with no Claude thinking beside it is left out, as before.
func searchRoundsBack(msgs []Message, name string) []Message {
	if !slices.ContainsFunc(msgs, func(m Message) bool { return slices.ContainsFunc(m.Parts, isSearch) }) {
		return msgs
	}
	out := make([]Message, 0, len(msgs))
	n := 0
	for _, m := range msgs {
		if !slices.ContainsFunc(m.Parts, isSearch) {
			out = append(out, m)
			continue
		}
		if m.Role != "assistant" || !slices.ContainsFunc(m.Parts, claudeSigned) {
			m.Parts = slices.DeleteFunc(slices.Clone(m.Parts), isSearch)
			if len(m.Parts) > 0 {
				out = append(out, m)
			}
			continue
		}
		var cur []Part
		for _, p := range m.Parts {
			if !isSearch(p) {
				cur = append(cur, p)
				continue
			}
			n++
			id := p.ID
			if id == "" {
				id = fmt.Sprintf("call_search_back_%d", n)
			}
			args, _ := json.Marshal(map[string]string{"query": p.Text})
			result := "Searched the web for: " + p.Text
			if len(p.Hits) > 0 {
				result += "\nPages found:"
				for _, h := range p.Hits {
					result += "\n- " + h.URL
				}
			}
			cur = append(cur, Part{Kind: ToolCall, ID: id, Name: name, Args: args})
			out = append(out, Message{Role: "assistant", Parts: cur},
				Message{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: id, Text: result}}})
			cur = nil
		}
		if len(cur) > 0 {
			out = append(out, Message{Role: "assistant", Parts: cur})
		}
	}
	return mergeTurns(out)
}

func isSearch(p Part) bool { return p.Kind == Search }

// claudeSigned is thinking Claude signed, or its redacted_thinking.
func claudeSigned(p Part) bool {
	return p.Kind == Thinking && (p.SignedBy == sealAnthropic && p.Signature != "" || p.sealedBy(sealAnthropic))
}

// toolCallItem is a Responses input item that makes a tool call, and
// toolResultItem one that answers a call.
func toolCallItem(t string) bool {
	switch t {
	case "function_call", "custom_tool_call", "tool_search_call":
		return true
	}
	return false
}

func toolResultItem(t string) bool {
	switch t {
	case "function_call_output", "custom_tool_call_output", "tool_search_output":
		return true
	}
	return false
}

// joinToolRuns moves an item that sits inside a run of tool calls and
// results — after a result of the run and before the last result that
// answers one of its calls — to the end of that run. An upstream that
// checks the exchange, on the chat form it turns a relayed request into or
// on the Responses input itself, reads the run as ended there and refuses
// it: Volcengine's coding plan answers 400 "An assistant message with
// 'tool_calls' must be followed by tool messages responding to each
// 'tool_call_id'", the same refusal pairToolItems mends a missing result
// for (#1341), and OpenCode Zen/Go answers "No tool output found for tool
// call <id>". An agent writes an item between one batch's results itself:
// Codex writes its <image_resize_notice> right after each result that held
// an image it resized (image_preparation.rs, prepare_response_items), so a
// batch of two calls has it between their results (#1752, #1822).
//
// An item that carries a call_id is part of an exchange and never moves,
// whatever its type. Before the run's first result, what the model wrote
// between its own calls is its step: reasoning and a hosted tool's call
// stay, and its text (an assistant message) goes ahead of the run's first
// call, with the reasoning right before it, so the step's calls stand
// together. OpenCode Go turns a relayed request into chat with each
// assistant message after a call opening a new assistant message, so
// [text, call_a, text, call_b, out_a, out_b], what Codex keeps when the
// model wrote a line before each call of a batch, reached it as two
// messages with tool_calls and the first answered by nothing: "No tool
// output found for tool call call_a" (#2100). Anything else there (a
// subagent's agent_message, a note) moves as it would between the results. A call opens the run only when a
// result after it answers it: a call nothing answers is pairToolItems'
// (#1341), and one answered before it is not waited on. An item between two
// whole exchanges stays where it is, and a request with nothing inside a
// run comes back byte for byte.
func joinToolRuns(body []byte) []byte {
	if !bytes.Contains(body, []byte(`_call"`)) {
		return body
	}
	var q map[string]json.RawMessage
	if decodeJSONView(body, &q) != nil {
		return body
	}
	input, isList := inputList(q["input"])
	if !isList {
		return body
	}
	type item struct {
		Type   string `json:"type"`
		Role   string `json:"role"`
		CallID string `json:"call_id"`
	}
	items := make([]item, len(input))
	for i, raw := range input {
		if decodeJSONView(raw, &items[i]) != nil {
			items[i] = item{}
		}
	}
	result := func(it item) bool { return it.CallID != "" && strings.HasSuffix(it.Type, "_output") }
	// answeredLater[i]: the call at i has a result after it
	answeredLater := make([]bool, len(input))
	ahead := map[string]bool{}
	for i := len(items) - 1; i >= 0; i-- {
		switch it := items[i]; {
		case result(it):
			ahead[it.CallID] = true
		case it.CallID != "":
			answeredLater[i] = ahead[it.CallID]
		}
	}
	modelsOwn := func(it item) bool {
		return it.Type == "reasoning" || it.Type == "message" && it.Role == "assistant" ||
			strings.HasSuffix(it.Type, "_call")
	}
	out := make([]json.RawMessage, 0, len(input))
	kinds := make([]item, 0, len(input)) // what each item of out is
	var held []json.RawMessage
	moved := false
	// open: calls of the run in progress whose result is still ahead;
	// answering: a result of the run has come; first: where in out the
	// run's first call is
	open := map[string]bool{}
	answering := false
	first := 0
	for i, raw := range input {
		it := items[i]
		switch {
		case result(it):
			if open[it.CallID] {
				delete(open, it.CallID)
				answering = true
			}
		case it.CallID != "":
			if answeredLater[i] {
				if len(open) == 0 {
					first = len(out)
				}
				open[it.CallID] = true
			}
		case len(open) > 0 && (answering || !modelsOwn(it)):
			held, moved = append(held, raw), true
			continue
		case len(open) > 0 && it.Type == "message":
			// the model's text between its calls goes ahead of the
			// first, the reasoning it follows with it
			from := len(out)
			for from > first && kinds[from-1].Type == "reasoning" {
				from--
			}
			seg := append(append([]json.RawMessage(nil), out[from:]...), raw)
			segKinds := append(append([]item(nil), kinds[from:]...), it)
			out = slices.Insert(out[:from], first, seg...)
			kinds = slices.Insert(kinds[:from], first, segKinds...)
			first += len(seg)
			moved = true
			continue
		}
		out = append(out, raw)
		kinds = append(kinds, it)
		if len(open) == 0 {
			out = append(out, held...)
			kinds = append(kinds, make([]item, len(held))...)
			held, answering = nil, false
		}
	}
	out = append(out, held...)
	if !moved {
		return body
	}
	q["input"], _ = marshalPlain(out)
	encoded, err := marshalPlain(q)
	if err != nil {
		return body
	}
	return encoded
}

// orphanedToolOutputs turns a tool result with no call ID into a user message.
// Codex uses standalone outputs for cross-thread notifications. Backends
// requiring paired outputs still need the delivered text, without a fake call ID.
func orphanedToolOutputs(body []byte) []byte {
	var q map[string]json.RawMessage
	if decodeJSONView(body, &q) != nil {
		return body
	}
	input, isList := inputList(q["input"])
	if !isList {
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
		if decodeJSONView(raw, &item) != nil {
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
			content = append(content, map[string]any{"type": "input_image", "detail": responsesDetail(image.Detail), "image_url": dataURL(image)})
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

// pairToolItems gives a Responses request's tool call that no result in
// the request answers a synthetic one, as pairToolMessages does for a Chat
// request: an upstream that checks the exchange on the chat form it turns
// the request into refuses a lone call — Volcengine's coding plan answers
// 400 "An assistant message with 'tool_calls' must be followed by tool
// messages responding to each 'tool_call_id'" (#1341), and OpenAI's own
// Responses API "No tool output found for function call". The result goes
// at the end of the run of calls and results the call is in, where a chat
// form has its tool messages. A result with no call is left to
// orphanedToolOutputs, and one naming a call another request carried
// (previous_response_id) stays as it is.
func pairToolItems(body []byte) []byte {
	if !bytes.Contains(body, []byte(`_call"`)) {
		return body
	}
	var q map[string]json.RawMessage
	if decodeJSONView(body, &q) != nil {
		return body
	}
	input, isList := inputList(q["input"])
	if !isList {
		return body
	}
	type item struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
	}
	items := make([]item, len(input))
	answered := map[string]bool{}
	for i, raw := range input {
		if decodeJSONView(raw, &items[i]) != nil {
			items[i] = item{}
		}
		if t := items[i].Type; (t == "function_call_output" || t == "custom_tool_call_output") && items[i].CallID != "" {
			answered[items[i].CallID] = true
		}
	}
	exchange := func(t string) bool {
		return toolCallItem(t) || toolResultItem(t)
	}
	out := make([]json.RawMessage, 0, len(input)+1)
	var synthetic []json.RawMessage // results owed by the run in progress
	for i, raw := range input {
		out = append(out, raw)
		it := items[i]
		if (it.Type == "function_call" || it.Type == "custom_tool_call") && it.CallID != "" && !answered[it.CallID] {
			kind := "function_call_output"
			if it.Type == "custom_tool_call" {
				kind = "custom_tool_call_output"
			}
			result, _ := marshalPlain(map[string]any{"type": kind, "call_id": it.CallID,
				"output": "[The result of this tool call is unavailable: the turn was interrupted.]"})
			synthetic = append(synthetic, result)
			answered[it.CallID] = true
		}
		if len(synthetic) > 0 && exchange(it.Type) && (i+1 == len(input) || !exchange(items[i+1].Type)) {
			out = append(out, synthetic...)
			synthetic = nil
		}
	}
	if len(out) == len(input) {
		return body
	}
	q["input"], _ = marshalPlain(out)
	encoded, err := marshalPlain(q)
	if err != nil {
		return body
	}
	return encoded
}

// withoutSearchCalls is a Responses request with its input's
// web_search_call items left out, for a relay to a provider that doesn't
// run web search itself (relaySearchCalls). Such a provider turns the
// request into chat for its model, and a web_search_call becomes an
// assistant tool call that no tool message answers: Command Code's
// Provider API answered Codex's summary request for a thread that had
// searched (52 function calls and their outputs, 8 web searches magpie
// ran for the model) 400 "An assistant message with 'tool_calls' must be
// followed by tool messages responding to each 'tool_call_id'"
// (mxmllln on Discord, deepseek-v4-flash-vision-exp). A turn offering
// web_search is translated for magpie's search and leaves them out there
// already (searchedForIt, searchRoundsBack); a request with no web_search
// offered — Codex's compaction, magpie's summary request, a turn with
// search off — was relayed with them. The item holds the query and the
// pages, never what they said, and the reply after it says what was
// found, so nothing the model read is lost.
func withoutSearchCalls(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"web_search_call"`)) {
		return body
	}
	var q map[string]json.RawMessage
	if decodeJSONView(body, &q) != nil {
		return body
	}
	input, isList := inputList(q["input"])
	if !isList {
		return body
	}
	out := make([]json.RawMessage, 0, len(input))
	for _, raw := range input {
		var it struct {
			Type string `json:"type"`
		}
		if decodeJSONView(raw, &it) == nil && it.Type == "web_search_call" {
			continue
		}
		out = append(out, raw)
	}
	if len(out) == len(input) {
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
		if n := len(out); n > 0 && out[n-1].Role == m.Role && (m.Role == "user" || m.Role == "assistant") {
			out[n-1].Parts = append(out[n-1].Parts, m.Parts...)
			continue
		}
		out = append(out, m)
	}
	return out
}

// responseInstructions applies the existing cache-preserving mapping only
// at an API that cannot keep native Responses instruction items.
func responseInstructions(r *Request) *Request {
	if !slices.ContainsFunc(r.Messages, func(m Message) bool { return m.Role == "system" || m.Role == "developer" }) {
		return r
	}
	out := *r
	out.Messages = nil
	replied := false
	for _, m := range r.Messages {
		if m.Role == "assistant" || slices.ContainsFunc(m.Parts, func(p Part) bool {
			return p.Kind == ToolCall || p.Kind == ToolResult || p.Kind == Thinking
		}) {
			replied = true
		}
		if m.Role == "system" || m.Role == "developer" {
			if t := text(m.Parts); t != "" {
				if replied {
					out.Messages = append(out.Messages, Message{Role: "user", Parts: []Part{{Kind: Text, Text: "<system-reminder>\n" + t + "\n</system-reminder>"}}})
				} else {
					if out.System != "" {
						out.System += "\n\n"
					}
					out.System += t
				}
			}
			continue
		}
		out.Messages = append(out.Messages, m)
	}
	out.Messages = mergeTurns(out.Messages)
	return &out
}

func responsesParts(raw json.RawMessage) []Part {
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
		ImageURL string `json:"image_url"`
		Detail   string `json:"detail"`
		FileData string `json:"file_data"`
		FileURL  string `json:"file_url"`
		Filename string `json:"filename"`
	}
	decodeJSON(raw, &items)
	var out []Part
	for _, it := range items {
		switch it.Type {
		case "input_text", "output_text", "text":
			out = append(out, Part{Kind: Text, Text: it.Text})
		case "input_image":
			if it.ImageURL != "" {
				im := imagePart(it.ImageURL)
				im.Detail = it.Detail
				out = append(out, im)
			}
		case "input_file":
			// a video alone (#6135); another file is still left out (#5452)
			src := cmp.Or(it.FileData, it.FileURL)
			if mt := videoFileType(src, it.Filename); mt != "" {
				if v, ok := videoPart(src, mt, it.Filename); ok {
					out = append(out, v)
				}
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
// carries no reasoning_text its summary as one, else noReasoning, and has
// each step that needs one lead with a reasoning item (reasonFirst), for an
// upstream that wants the reasoning back (replaysReasoning). Codex sends an
// item back as it got it: one from a turn magpie translated has only a
// summary, one the ChatGPT backend sealed has only encrypted_content, and a
// turn another model in a mixed group served may have no reasoning item at
// all. DeepSeek refuses the whole request over any of them, and over an
// empty reasoning_text, with "The `reasoning_text` in the thinking mode must
// be passed back to the API" (#1104, #1592, #1822). A body with nothing to
// give comes back byte for byte.
func withReasoningText(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"reasoning"`)) && !bytes.Contains(body, []byte(`_call"`)) &&
		!bytes.Contains(body, []byte(`_output"`)) {
		return body
	}
	var q map[string]json.RawMessage
	if decodeJSONView(body, &q) != nil {
		return body
	}
	items, isList := inputList(q["input"])
	if !isList {
		return body
	}
	changed := false
	kinds := make([]stepKind, len(items))
	for i, raw := range items {
		var it struct {
			Type    string  `json:"type"`
			Role    string  `json:"role"`
			Summary []rText `json:"summary"`
			Content []rText `json:"content"`
		}
		if decodeJSONView(raw, &it) != nil {
			continue
		}
		kinds[i] = stepKindOf(it.Type, it.Role)
		if it.Type != "reasoning" ||
			slices.ContainsFunc(it.Content, func(c rText) bool { return c.Type == "reasoning_text" && c.Text != "" }) {
			continue
		}
		var b strings.Builder
		for _, s := range it.Summary {
			b.WriteString(s.Text)
		}
		if b.Len() == 0 {
			b.WriteString(noReasoning)
		}
		var m map[string]json.RawMessage
		if decodeJSONView(raw, &m) != nil {
			continue
		}
		m["content"], _ = json.Marshal([]map[string]string{{"type": "reasoning_text", "text": b.String()}})
		if nb, err := marshalJSON(m); err == nil {
			items[i], changed = nb, true
		}
	}
	none, _ := json.Marshal(noReasoningItem())
	if led, ok := reasonFirst(items, kinds, json.RawMessage(none)); ok {
		items, changed = led, true
	}
	if !changed {
		return body
	}
	q["input"], _ = marshalJSON(items)
	nb, err := marshalJSON(q)
	if err != nil {
		return body
	}
	return nb
}

// stepKind is what a Responses input item is to a step of the model's: its
// reasoning, something it said or a call it made, what answered a call, or
// a message of the user's (or the system's) that starts a turn.
type stepKind int

const (
	stepOther stepKind = iota
	stepReasoning
	stepSaid
	stepCall
	stepOutput
	stepTurn
)

func stepKindOf(typ, role string) stepKind {
	switch {
	case typ == "reasoning":
		return stepReasoning
	case strings.HasSuffix(typ, "_output"):
		return stepOutput
	case strings.HasSuffix(typ, "_call"):
		return stepCall
	case (typ == "message" || typ == "") && role == "assistant":
		return stepSaid
	case typ == "message" || typ == "" && role != "":
		return stepTurn
	}
	return stepOther
}

// noReasoningItem is the reasoning item a step leads with when it has none
// of its own (reasonFirst).
func noReasoningItem() map[string]any {
	return map[string]any{"type": "reasoning", "summary": []any{},
		"content": []map[string]any{{"type": "reasoning_text", "text": noReasoning}}}
}

// reasonFirst has each step of the model's (a run of its reasoning, what it
// said and the calls it made, between a user message or a tool output and
// the next) that makes a call, or that follows a tool output, lead with a
// reasoning item: the step's first one moved ahead of the rest, else none.
// DeepSeek's /v1/responses reads a step as one assistant message whose
// thinking is the reasoning item it starts with, and turns the whole
// request away ("The `reasoning_text` in the thinking mode must be passed
// back to the API") when a step of the turn being continued starts with
// anything else: a call (#1592), or what the model said, as Codex sends a
// step that said something before its call with its reasoning between them,
// or with none, which magpie put between them (#1822). Checked against
// api.deepseek.com on 2026-10-12: [text, reasoning, call] and [output,
// text] are refused, [reasoning, text, call] and [output, reasoning, text]
// are taken. ok is false, and items comes back as it is, when every such
// step leads with one already.
func reasonFirst[T any](items []T, kinds []stepKind, none T) (led []T, ok bool) {
	step := func(k stepKind) bool { return k == stepReasoning || k == stepSaid || k == stepCall }
	afterOutput := false
	for i := 0; i < len(items); {
		if !step(kinds[i]) {
			switch kinds[i] {
			case stepOutput:
				afterOutput = true
			case stepTurn:
				afterOutput = false
			}
			if ok {
				led = append(led, items[i])
			}
			i++
			continue
		}
		j, calls, first := i, false, -1
		for ; j < len(items) && step(kinds[j]); j++ {
			calls = calls || kinds[j] == stepCall
			if kinds[j] == stepReasoning && first < 0 {
				first = j
			}
		}
		if (calls || afterOutput) && first != i {
			if !ok {
				led, ok = append(make([]T, 0, len(items)+1), items[:i]...), true
			}
			if first < 0 {
				led = append(led, none)
			} else {
				led = append(led, items[first])
			}
			for x := i; x < j; x++ {
				if x != first {
					led = append(led, items[x])
				}
			}
		} else if ok {
			led = append(led, items[i:j]...)
		}
		i = j
	}
	if !ok {
		return items, false
	}
	return led, true
}

// sealedReasoning is a reasoning item a Responses API sealed, fit to go
// back to one as it came: encrypted_content in it, and no reasoning text
// (which OpenAI and the ChatGPT backend refuse in input, #1411; one with
// text goes as before).
func sealedReasoning(p Part) bool {
	if !p.sealedBy(sealResponses) {
		return false
	}
	var it struct {
		Enc     string            `json:"encrypted_content"`
		Content []json.RawMessage `json:"content"`
	}
	return decodeJSON(p.Sealed, &it) == nil && it.Enc != "" && len(it.Content) == 0
}

// buildResponses renders a request for a Responses upstream.
func buildResponses(r *Request, model, host string, rejectTemp bool) []byte {
	// A translated request's system or developer message (Claude Code's
	// environment as a {role: "system"} message) is the harness's, not an
	// instruction item of a Responses client's own: it is folded into the
	// instructions as the Chat and Anthropic builds fold it
	// (responseInstructions) — a backend that takes user, assistant and
	// developer alone refuses the role with a 400 the agent sees (#4632).
	// A Responses client's own instruction items were kept by the read
	// (#2532) and go where they stood; so do a codex account's, which its
	// send says as developer ones in place (codexBody, buildFor).
	if !r.instructionsKept {
		r = responseInstructions(r)
	}
	// A turn's reasoning goes back as a reasoning item, as a model that
	// thinks between tool calls wants it (DeepSeek: "The reasoning_text in
	// the thinking mode must be passed back", #388). Only a DeepSeek model
	// gets it: OpenAI's and those in front of it read only their own
	// sealed reasoning, and may refuse an item without it.
	replay := replaysReasoning(model, host)
	var input []any
	// a deferred tool a result loaded goes in an additional_tools item
	// after that result and the results beside it, once, where the
	// upstream takes one (Request.LoadedInPlace, #2660)
	deferred := map[string]Tool{}
	placed := map[string]bool{}
	if r.LoadedInPlace {
		for _, t := range r.Tools {
			if t.Deferred {
				deferred[t.Name] = t
			}
		}
	}
	var loaded []any
	flushLoaded := func() {
		if len(loaded) > 0 {
			input = append(input, map[string]any{"type": "additional_tools", "role": "developer", "tools": loaded})
			loaded = nil
		}
	}
	// a second answer to one call is left out, as the Chat and Messages
	// builds leave it (dropSecondAnswers)
	for _, m := range dropSecondAnswers(r.Messages) {
		var content []map[string]any
		flushMsg := func() {
			if len(content) == 0 {
				return
			}
			flushLoaded()
			input = append(input, map[string]any{"type": "message", "role": m.Role, "content": content})
			content = nil
		}
		for _, p := range m.Parts {
			switch p.Kind {
			case Text:
				if p.Text == "" && p.Signature != "" {
					continue // only Gemini's signature, which is Gemini's
				}
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
					content = append(content, map[string]any{"type": "input_image", "detail": responsesDetail(p.Detail), "image_url": dataURL(p)})
				}
			case Thinking:
				if !replay && sealedReasoning(p) {
					// a Responses API's own sealed reasoning goes back as
					// it came: id, summary and encrypted_content (#1445)
					flushMsg()
					flushLoaded()
					input = append(input, p.Sealed)
				} else if replay && p.Text != "" {
					flushMsg()
					flushLoaded()
					input = append(input, map[string]any{"type": "reasoning", "summary": []any{},
						"content": []map[string]any{{"type": "reasoning_text", "text": p.Text}}})
				}
			case ToolCall:
				flushMsg()
				flushLoaded()
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
						items = append(items, map[string]any{"type": "input_image", "detail": responsesDetail(im.Detail), "image_url": dataURL(im)})
					}
					output = items
				}
				input = append(input, map[string]any{"type": "function_call_output", "call_id": p.CallID, "output": output})
				for _, name := range p.Loads {
					if t, ok := deferred[name]; ok && !placed[name] {
						placed[name] = true
						loaded = append(loaded, responsesTool(t))
					}
				}
			}
		}
		flushMsg()
	}
	flushLoaded()
	if replay {
		// each step that makes a call, or follows a tool output, leads with
		// its reasoning, or with a space's (#1592, #1822)
		kinds := make([]stepKind, len(input))
		for i, it := range input {
			var k struct {
				Type string `json:"type"`
				Role string `json:"role"`
			}
			switch v := it.(type) {
			case map[string]any:
				k.Type, _ = v["type"].(string)
				k.Role, _ = v["role"].(string)
			case json.RawMessage:
				decodeJSON(v, &k)
			}
			kinds[i] = stepKindOf(k.Type, k.Role)
		}
		input, _ = reasonFirst(input, kinds, any(noReasoningItem()))
	}
	if input == nil {
		input = []any{}
	}
	out := map[string]any{"model": model, "input": input, "stream": r.Stream, "store": false}
	if len(r.ClientMetadata) > 0 {
		out["client_metadata"] = r.ClientMetadata
	}
	if len(r.AccessPrograms) > 0 {
		out["access_programs"] = r.AccessPrograms
	}
	if len(r.Text) > 0 || r.Format != nil {
		text := map[string]any{}
		decodeJSON(r.Text, &text)
		if r.Format != nil {
			text["format"] = r.Format.responses()
		}
		out["text"] = text
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
	// Ultrafast only the ChatGPT backend offers
	if r.Ultrafast && host == "chatgpt.com" {
		out["service_tier"] = "ultrafast"
	}
	if r.OwnTier && r.Tier != "" {
		out["service_tier"] = r.Tier
	}
	if r.Effort != "" {
		out["reasoning"] = map[string]any{"effort": r.Effort, "summary": "auto"}
	} else if r.Thinking {
		out["reasoning"] = map[string]any{"summary": "auto"}
	}
	// the client's include goes on as the request would have without
	// magpie. Sealed reasoning is asked for only with reasoning, as Codex
	// asks for it: OpenAI refuses it of a model that doesn't reason. What
	// comes back sealed goes to a Responses client as it came, and the
	// input's goes back to a Responses upstream, as it would without magpie
	// (#1445); an account or vendor that can't read another's turns it away,
	// and is asked again without it (foreignReasoning, withoutRefused).
	// Output token replay's include is OpenAI's alone, as Codex sends it
	// (forVendor): Copilot refuses it (echo_ts).
	var include []string
	for _, v := range r.Include {
		if v == "" || slices.Contains(include, v) || (v == "reasoning.encrypted_content" && out["reasoning"] == nil) ||
			v == outputReplay && host != "chatgpt.com" && host != "api.openai.com" || !includeTaken(host, v) {
			continue
		}
		include = append(include, v)
	}
	if len(r.Tools) > 0 || r.WebSearch {
		var tools []map[string]any
		for _, t := range r.Tools {
			if placed[t.Name] {
				continue // offered where it was loaded
			}
			tools = append(tools, responsesTool(t))
		}
		if r.WebSearch {
			tools = append(tools, map[string]any{"type": "web_search"})
			// the pages it found, which a client of another protocol is
			// told of (OpenAI's option; xAI's API isn't known to take it,
			// and Ark's refuses it, arkIncludes)
			if host != "api.x.ai" && includeTaken(host, "web_search_call.action.sources") && !slices.Contains(include, "web_search_call.action.sources") {
				include = append(include, "web_search_call.action.sources")
			}
		}
		if len(tools) > 0 {
			out["tools"] = tools
		}
		switch {
		case r.ToolChoice == "auto" || r.ToolChoice == "none" || r.ToolChoice == "required":
			out["tool_choice"] = r.ToolChoice
		case strings.HasPrefix(r.ToolChoice, "name:"):
			out["tool_choice"] = map[string]any{"type": "function", "name": strings.TrimPrefix(r.ToolChoice, "name:")}
		}
		if r.Parallel != nil {
			out["parallel_tool_calls"] = *r.Parallel
		}
	} else if r.ToolChoice == "none" {
		// tools turned off with none declared (Chat's "none", Anthropic's
		// {"type":"none"}, Gemini's NONE) still says so here, as a
		// Responses client's own request would, for a provider plugin that
		// adds a server-side tool (Grok's x_search) to tell it from no
		// choice (#3110). Only a plugin is sent it (the gateway leaves it
		// out for a vendor, which has no use for it: xAI turns it away).
		out["tool_choice"] = "none"
	}
	if len(include) > 0 {
		out["include"] = include
	}
	b, _ := marshalBody(out, "input")
	// a call the client left unanswered (a turn it interrupted) gets its
	// synthetic result, as buildChat's pairToolMessages and
	// buildAnthropic's pairToolBlocks give one: the Responses API refuses
	// the lone call, "No tool output found for function call <id>", on
	// every turn after it too (#2222)
	return pairToolItems(b)
}

// responsesTool is t as a Responses function tool. strict is said, as
// Codex says it: left out, the ChatGPT backend holds the schema to strict
// mode's rules and refuses a pattern with a lookaround (MiniMax Code's
// path, #383).
func responsesTool(t Tool) map[string]any {
	tool := map[string]any{"type": "function", "name": t.Name, "description": t.Description, "strict": t.Strict}
	if len(t.Schema) > 0 {
		tool["parameters"] = t.Schema
	}
	return tool
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
// A function call's arguments are given as their deltas come: a model
// writing a patch or a command for minutes is seen writing it (echo_ts on
// Discord: Codex on Copilot, translated for its web_search, saw nothing
// until the call was done). The deltas can leave out what the finished
// call holds (#613: Codex's spawn_agent came back with arguments {} where
// the request was translated, though the upstream's finished call had
// them), and what was sent of a call's arguments can't be taken back. So
// the deltas that would make the arguments whole JSON wait for the call's
// done events (jsonWhole), and the call is given what they add to it.
type responsesDecoder struct {
	started bool
	called  bool            // a function call was streamed
	calling bool            // a function call is open
	sent    strings.Builder // the open call's argument deltas given so far
	held    string          // its deltas since they made whole JSON, not given yet
	whole   jsonWhole       // where its deltas stand in their JSON
	full    string          // the open call's arguments as its done events give them
	// waiting are the calls added but not given yet, in the order added;
	// giving, while it lasts, is the call whose lines are being given
	waiting []*rCall
	giving  *rCall
	// addedID is the id each reasoning item went out added by, by its
	// output_index: an upstream whose ids change per event (Copilot's)
	// does the same item done by another (#2974)
	addedID map[int]string
}

// rCall is a function_call item added before its arguments came (#3985):
// an upstream may add every call of a turn before any is argued, and a
// start given then would close the call before it with none of its own.
// Its lines are kept as they came and read when its own first arguments
// or its item done arrives.
type rCall struct {
	itemID string
	index  int      // the output_index its events name
	lines  []string // its added, argument and item done lines
	ended  bool     // its output_item.done came
}

// waitingCall is the call waiting whose item an event names, by item_id
// or, when it names none waiting, by output_index. An event that names
// nothing at all goes, while no call is open, to the first waiting one: a
// stream whose events name nothing streams its calls in order. nil when
// none is, and a delta naming no call waiting then goes on to the open
// one as before, so a stream whose events name nothing still reads.
func (d *responsesDecoder) waitingCall(itemID string, index *int) *rCall {
	if itemID != "" {
		for _, c := range d.waiting {
			if c.itemID == itemID {
				return c
			}
		}
	}
	if index != nil {
		for _, c := range d.waiting {
			if c.index == *index {
				return c
			}
		}
	}
	if itemID == "" && index == nil && !d.calling && len(d.waiting) > 0 {
		return d.waiting[0]
	}
	return nil
}

// give hands the client a waiting call: its lines as they came, read one
// call at a time through the decoder as before. A call whose item hasn't
// done is then the open one, the rest of its arguments going on as they
// come.
func (d *responsesDecoder) give(c *rCall, emit func(Event)) error {
	d.waiting = slices.DeleteFunc(d.waiting, func(w *rCall) bool { return w == c })
	d.giving = c
	defer func() { d.giving = nil }()
	for _, line := range c.lines {
		if err := d.decode(line, emit); err != nil {
			return err
		}
	}
	return nil
}

// giveWaiting gives the calls still waiting, in their items' order: every
// one when the reply ends, else those whose items did while another call
// was open.
func (d *responsesDecoder) giveWaiting(all bool, emit func(Event)) error {
	calls := slices.Clone(d.waiting)
	slices.SortStableFunc(calls, func(a, b *rCall) int { return cmp.Compare(a.index, b.index) })
	for _, c := range calls {
		if all || c.ended {
			if err := d.give(c, emit); err != nil {
				return err
			}
		}
	}
	if all {
		d.endCall(emit) // the last one given may still be open
	}
	return nil
}

// argsDelta gives an open call's argument delta, or holds it when with
// it the deltas are whole JSON, which the done events may yet replace.
func (d *responsesDecoder) argsDelta(delta string, emit func(Event)) {
	if d.whole.feed(delta) {
		d.held += delta
		return
	}
	text := d.held + delta
	d.held = ""
	d.sent.WriteString(text)
	if text != "" {
		emit(Event{Kind: KToolArgs, Text: text})
	}
}

// endCall gives the rest of the open call's arguments: what was held, or
// what its done events give past what was sent where they hold more.
func (d *responsesDecoder) endCall(emit func(Event)) {
	if !d.calling {
		return
	}
	d.calling = false
	sent := d.sent.String()
	rest := d.held
	if args := pickArgs(sent+d.held, d.full); strings.HasPrefix(args, sent) {
		rest = args[len(sent):]
	}
	d.sent.Reset()
	d.held, d.full, d.whole = "", "", jsonWhole{}
	if rest != "" {
		emit(Event{Kind: KToolArgs, Text: rest})
	}
}

// jsonWhole follows a stream of JSON text, a piece at a time, for whether
// what came so far is a whole object or array.
type jsonWhole struct {
	depth    int
	str, esc bool
	began    bool
}

// feed reads the next piece and says whether the text is whole after it.
func (j *jsonWhole) feed(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case j.esc:
			j.esc = false
		case j.str:
			if c == '\\' {
				j.esc = true
			} else if c == '"' {
				j.str = false
			}
		case c == '"':
			j.str = true
			if j.depth == 0 {
				j.began = false
			}
		case c == '{' || c == '[':
			j.depth++
			j.began = true
		case c == '}' || c == ']':
			j.depth--
		case c != ' ' && c != '\t' && c != '\n' && c != '\r' && j.depth == 0:
			j.began = false // a value that isn't one, or after one
		}
	}
	return j.began && j.depth == 0 && !j.str
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
		// the item an argument delta or done names, and where it sits
		ItemID      string `json:"item_id"`
		OutputIndex *int   `json:"output_index"`
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
	if err := decodeJSON([]byte(data), &ev); err != nil {
		// an error in a shape of its own is still the reply's end (#1933)
		if e, ok := carriedError(data); ok {
			d.endCall(emit)
			emit(e)
		}
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
			if d.giving == nil {
				// the call waits for its own arguments (#3985)
				c := &rCall{itemID: ev.Item.ID, lines: []string{data}}
				if ev.OutputIndex != nil {
					c.index = *ev.OutputIndex
				}
				d.waiting = append(d.waiting, c)
				return nil
			}
			d.called, d.calling = true, true
			emit(Event{Kind: KToolStart, ID: ev.Item.CallID, Name: ev.Item.Name})
		}
		if ev.Item.Type == "reasoning" {
			// an item of its own, by the id the upstream gave it (#1445)
			emit(Event{Kind: KThinkStart, ID: ev.Item.ID})
			if d.addedID == nil {
				d.addedID = map[int]string{}
			}
			if ev.OutputIndex != nil {
				d.addedID[*ev.OutputIndex] = ev.Item.ID
			}
		}
	case "response.output_text.delta":
		d.endCall(emit)
		emit(Event{Kind: KText, Text: ev.Delta})
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		d.endCall(emit)
		emit(Event{Kind: KThink, Text: ev.Delta})
	case "response.function_call_arguments.delta":
		if d.giving == nil {
			if c := d.waitingCall(ev.ItemID, ev.OutputIndex); c != nil {
				c.lines = append(c.lines, data)
				if d.calling {
					return nil // the open call keeps its own; this one waits its turn
				}
				return d.give(c, emit)
			}
		}
		if d.calling {
			d.argsDelta(ev.Delta, emit)
		} else {
			emit(Event{Kind: KToolArgs, Text: ev.Delta})
		}
	case "response.function_call_arguments.done":
		if d.giving == nil {
			if c := d.waitingCall(ev.ItemID, ev.OutputIndex); c != nil {
				c.lines = append(c.lines, data)
				return nil
			}
		}
		if d.calling && ev.Arguments != "" {
			d.full = ev.Arguments
		}
	case "response.output_item.done":
		if ev.Item.Type == "function_call" {
			if d.giving == nil {
				if c := d.waitingCall(ev.Item.ID, ev.OutputIndex); c != nil {
					c.lines = append(c.lines, data)
					c.ended = true
					if d.calling {
						return nil // it goes when the open call is done with its own
					}
					return d.give(c, emit)
				}
			}
			if ev.Item.Arguments != "" {
				d.full = string(ev.Item.Arguments)
			}
			d.endCall(emit)
			// the calls whose items ended while the open one streamed go
			// now — not while a call is being given, whose lines are still
			// reading; those go when the reply ends
			if d.giving == nil {
				if err := d.giveWaiting(false, emit); err != nil {
					return err
				}
			}
			return nil
		}
		if ev.Item.Type == "reasoning" && ev.Item.EncryptedContent != "" {
			// sealed: the item as the upstream wrote it, for a Responses
			// client and for its next request (#1445). An encoder knows
			// the item by the id it went out added by, so when the ids
			// change per event the done one is told by its own (#2974)
			if ev.OutputIndex != nil {
				if a := d.addedID[*ev.OutputIndex]; a != "" {
					ev.Item.ID = a
				}
			}
			var whole struct {
				Item json.RawMessage `json:"item"`
			}
			if decodeJSON([]byte(data), &whole) == nil && len(whole.Item) > 0 {
				emit(Event{Kind: KSealed, Name: sealResponses, ID: ev.Item.ID, Text: string(whole.Item)})
			}
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
		if ev.Response.Error != nil || ev.Type == "response.failed" || ev.Response.Status == "failed" {
			// a failed response is an error with or without the words
			// for it, never a whole reply (#1933)
			msg := ""
			if ev.Response.Error != nil {
				msg = ev.Response.Error.Message
			}
			status, said := requestRefused(data)
			code, fs, fsaid := filterRefused(data)
			if status == 0 {
				status, said = fs, fsaid
			}
			emit(Event{Kind: KError, Text: cmp.Or(msg, streamErrorText(data)), Code: code, Status: status, Said: said})
			return nil
		}
		// the reply ends: every call still waiting goes before its stop
		if err := d.giveWaiting(true, emit); err != nil {
			return err
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
			msg = cmp.Or(ev.Error.Message, msg)
		}
		status, said := requestRefused(data)
		code, fs, fsaid := filterRefused(data)
		if status == 0 {
			status, said = fs, fsaid
		}
		emit(Event{Kind: KError, Text: cmp.Or(msg, streamErrorText(data)), Code: code, Status: status, Said: said})
	}
	return nil
}

// requestRefused is 400 and the vendor's error, as {"error":{…}}, when a
// Responses stream's error event or failed response says the request
// itself is at fault (type invalid_request_error): the ChatGPT backend
// answers an access program the workspace doesn't have with a 200 stream
// whose error event is access_program_not_enabled (magpie-releases#18).
// Asked again, on this account or another, it is refused the same way, so
// it is neither retried nor resumed, and a client answered whole gets its
// code and param. Read by the error object's type, never by its words.
func requestRefused(data string) (int, json.RawMessage) {
	for _, path := range []string{"error", "response.error"} {
		e := gjson.Get(data, path)
		if e.IsObject() && e.Get("type").String() == "invalid_request_error" {
			return http.StatusBadRequest, json.RawMessage(`{"error":` + e.Raw + `}`)
		}
	}
	return 0, nil
}

// saidError puts the type, code and param of the vendor's own error (an
// Event's Said) on the error a stream encoder writes, so a request the
// vendor refused reads as refused (invalid_request_error) to the agent and
// to the hold that decides whether to ask again, not as magpie's api_error
// (magpie-releases#18). Without Said it is left as it was.
func saidError(failed map[string]any, said json.RawMessage) {
	e := gjson.GetBytes(said, "error")
	for _, k := range []string{"type", "code", "param"} {
		if v := e.Get(k); v.Type == gjson.String && v.String() != "" {
			failed[k] = v.String()
		}
	}
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
	// a reasoning item began upstream (KThinkStart): the next thinking
	// opens an item of its own, by thinkID when the upstream named it
	// rs_ … . thinkUp is the id it came with whatever its shape: the
	// sealed item done by it is the one that streamed, not another (#2974)
	thinkNew bool
	thinkID  string
	thinkUp  string
	// sig is Claude's signature of the open reasoning item's block, which
	// its encrypted_content carries back (claudeseal); bare is an item of
	// a block signed with no text, opened with no summary part
	sig    strings.Builder
	bare   bool
	output []any
	col    collector
	named  map[string]nsTool // the request's namespaced tools
	// call is the open function_call item's id and name, kept here: the
	// collector's last part isn't it once a hidden thinking block (signed
	// with no text, or redacted) has come after the call (#1786)
	call Part
	// input reads a custom tool's input out of its call's arguments as
	// they come
	input customStream
	// a message's output_item.done, and the events after it, wait here
	// until its phase is known (phase.go, #2437)
	heldMsg map[string]any
	held    []heldEvent
}

type heldEvent struct {
	typ    string
	fields map[string]any
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
	if e.heldMsg != nil {
		e.held = append(e.held, heldEvent{typ, fields})
		if len(e.held) > heldMost {
			e.release("")
		}
		return
	}
	e.write(typ, fields)
}

func (e *responsesEncoder) write(typ string, fields map[string]any) {
	fields["type"] = typ
	fields["sequence_number"] = e.seq
	e.seq++
	e.w.event(typ, fields)
}

// release sends the waiting message's events, the message given phase
// (or none, for "").
func (e *responsesEncoder) release(phase string) {
	if e.heldMsg == nil {
		return
	}
	if phase != "" {
		e.heldMsg["phase"] = phase
	}
	held := e.held
	e.heldMsg, e.held = nil, nil
	for _, h := range held {
		e.write(h.typ, h.fields)
	}
}

func (e *responsesEncoder) response(status string, extra map[string]any) map[string]any {
	// a list, never null, before any item: Muse Code refuses a stream whose
	// response.created has output null (its nil slice in an any isn't nil)
	output := e.output
	if output == nil {
		output = []any{}
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
	e.closeAs(nil)
}

// closeAs closes the open item, as sealed when the upstream wrote it
// whole (a reasoning item it sealed).
func (e *responsesEncoder) closeAs(sealed json.RawMessage) {
	if e.open == "" {
		return
	}
	var item any
	switch e.open {
	case Text:
		t := e.text.String()
		e.send("response.output_text.done", map[string]any{"item_id": e.itemID, "output_index": e.item, "content_index": 0, "text": t, "logprobs": []any{}})
		part := map[string]any{"type": "output_text", "text": t, "annotations": []any{}, "logprobs": []any{}}
		e.send("response.content_part.done", map[string]any{"item_id": e.itemID, "output_index": e.item, "content_index": 0, "part": part})
		m := map[string]any{"id": e.itemID, "type": "message", "role": "assistant", "status": "completed", "content": []map[string]any{part}}
		// its done waits for what comes next to say its phase
		e.release(phaseCommentary)
		item, e.heldMsg = m, m
	case Thinking:
		t := e.text.String()
		summary := []map[string]any{}
		if !e.bare {
			e.send("response.reasoning_summary_text.done", map[string]any{"item_id": e.itemID, "output_index": e.item, "summary_index": 0, "text": t})
			part := map[string]any{"type": "summary_text", "text": t}
			e.send("response.reasoning_summary_part.done", map[string]any{"item_id": e.itemID, "output_index": e.item, "summary_index": 0, "part": part})
			summary = append(summary, part)
		}
		m := map[string]any{"id": e.itemID, "type": "reasoning", "status": "completed", "summary": summary}
		if e.sig.Len() > 0 {
			// Claude's block, signed, for the next request to give back
			m["encrypted_content"] = claudeThinking(t, e.sig.String())
		}
		item = m
		if sealed != nil {
			item = sealed
		}
	case ToolCall:
		args := strings.TrimSpace(e.text.String())
		if args == "" {
			args = "{}"
		}
		p := e.call
		item = callTo(map[string]any{"id": e.itemID, "type": "function_call", "status": "completed", "call_id": p.ID, "arguments": args}, p.Name, e.named)
		switch q := e.named[p.Name]; {
		case q.Custom:
			// as OpenAI ends a custom tool's input deltas
			input, _ := item.(map[string]any)["input"].(string)
			e.send("response.custom_tool_call_input.done", map[string]any{"item_id": e.itemID, "output_index": e.item, "input": input})
		case !q.Search:
			e.send("response.function_call_arguments.done", callTo(map[string]any{"item_id": e.itemID, "output_index": e.item, "call_id": p.ID, "arguments": args}, p.Name, e.named))
		}
	}
	e.send("response.output_item.done", map[string]any{"output_index": e.item, "item": item})
	e.output = append(e.output, item)
	// the open item's upstream id went with it
	e.open, e.text, e.thinkUp = "", strings.Builder{}, ""
	e.sig, e.bare = strings.Builder{}, false
}

func (e *responsesEncoder) openItem(k Kind, prefix string, item map[string]any) {
	e.closeItem()
	if k != Thinking {
		e.release(phaseCommentary)
	}
	e.item++
	e.open, e.itemID = k, prefix+newID()
	if k == Thinking && e.thinkID != "" {
		e.itemID = e.thinkID
	}
	e.thinkNew, e.thinkID = false, ""
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
		if e.open != Thinking || e.thinkNew || e.bare {
			if e.open != Thinking || e.thinkNew {
				e.openItem(Thinking, "rs_", map[string]any{"type": "reasoning", "summary": []any{}})
			}
			e.bare = false
			e.send("response.reasoning_summary_part.added", map[string]any{"item_id": e.itemID, "output_index": e.item, "summary_index": 0,
				"part": map[string]any{"type": "summary_text", "text": ""}})
		}
		e.text.WriteString(ev.Text)
		e.send("response.reasoning_summary_text.delta", map[string]any{"item_id": e.itemID, "output_index": e.item, "summary_index": 0, "delta": ev.Text})
	case KThinkStart:
		e.thinkNew, e.thinkID, e.thinkUp = true, "", ev.ID
		if strings.HasPrefix(ev.ID, "rs_") {
			e.thinkID = ev.ID
		}
	case KSig:
		if ev.Name != sealAnthropic || ev.Text == "" {
			break // Gemini's, which goes back on its own path
		}
		if e.open != Thinking || e.thinkNew {
			// a block signed with no text in it: an item all the same,
			// with no summary, for its signature to come back
			e.openItem(Thinking, "rs_", map[string]any{"type": "reasoning", "summary": []any{}})
			e.bare = true
		}
		e.sig.WriteString(ev.Text)
	case KSealed:
		if ev.Name == sealAnthropic {
			// Claude's redacted_thinking: an item with no summary, the
			// block whole in its encrypted_content
			e.closeItem()
			e.item++
			e.thinkNew, e.thinkID = false, ""
			id := "rs_" + newID()
			e.send("response.output_item.added", map[string]any{"output_index": e.item,
				"item": map[string]any{"id": id, "type": "reasoning", "status": "in_progress", "summary": []any{}}})
			item := map[string]any{"id": id, "type": "reasoning", "status": "completed", "summary": []any{}, "encrypted_content": claudeseal.Seal([]byte(ev.Text))}
			e.send("response.output_item.done", map[string]any{"output_index": e.item, "item": item})
			e.output = append(e.output, item)
			break
		}
		if ev.Name != sealResponses {
			break // another API's, which a Responses client can't read
		}
		raw := json.RawMessage(ev.Text)
		if e.open == Thinking && !e.thinkNew && (ev.ID == "" || ev.ID == e.itemID || ev.ID == e.thinkUp) {
			// the item streamed so far, done as the upstream wrote it,
			// made over when it carries its text (another vendor's,
			// #2855); its events name it by the id they already carried
			e.closeAs(magpieMadeReasoningRaw(raw, e.itemID))
			break
		}
		// none of it streamed (no summary): added and done whole, made
		// over when the item carries its text (#2855)
		raw = magpieMadeReasoningRaw(raw, "")
		e.closeItem()
		e.item++
		e.thinkNew, e.thinkID = false, ""
		e.send("response.output_item.added", map[string]any{"output_index": e.item, "item": raw})
		e.send("response.output_item.done", map[string]any{"output_index": e.item, "item": raw})
		e.output = append(e.output, raw)
	case KSearch:
		// A search arrives whole, after it ran. Tell the client its query
		// and sources, as Anthropic's encoder does, without inventing
		// earlier in-progress events or citation offsets in the answer.
		e.closeItem()
		e.release(phaseCommentary)
		e.item++
		item := responsesSearchItem(ev.Text, ev.Hits)
		e.send("response.output_item.added", map[string]any{"output_index": e.item, "item": item})
		e.send("response.web_search_call.completed", map[string]any{"output_index": e.item, "item_id": item["id"]})
		e.send("response.output_item.done", map[string]any{"output_index": e.item, "item": item})
		e.output = append(e.output, item)
	case KToolStart:
		if ev.ID == "" {
			ev.ID = "call_" + newID()
		}
		// Open (and so close the previous item) before recording this call:
		// closeItem reads the previous call's id and name from e.call.
		item := callTo(map[string]any{"type": "function_call", "call_id": ev.ID, "arguments": ""}, ev.Name, e.named)
		e.openItem(ToolCall, itemPrefix(item), item)
		e.call = Part{Kind: ToolCall, ID: ev.ID, Name: ev.Name}
		e.input = customStream{}
		e.col.add(ev)
		return
	case KToolArgs:
		if e.open == ToolCall && ev.Text != "" {
			e.text.WriteString(ev.Text)
			switch q := e.named[e.call.Name]; {
			case q.Custom:
				// a custom tool's input, as it is read out of the
				// arguments: what the model writes of a patch is seen as
				// it writes it, as from OpenAI
				if in := e.input.feed(ev.Text); in != "" {
					e.send("response.custom_tool_call_input.delta", map[string]any{"item_id": e.itemID, "output_index": e.item, "delta": in})
				}
			default:
				e.send("response.function_call_arguments.delta", map[string]any{"item_id": e.itemID, "output_index": e.item, "delta": ev.Text})
			}
		}
	case KError:
		e.closeItem()
		e.release("")
		code := "server_error"
		if ev.Code != "" {
			code = ev.Code
		}
		failed := map[string]any{"code": code, "message": ev.Text}
		saidError(failed, ev.Said)
		e.send("response.failed", map[string]any{"response": e.response("failed", map[string]any{"error": failed})})
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
	// past what's waiting: a keepalive held back keeps nothing alive
	e.write("response.in_progress", map[string]any{"response": e.response("in_progress", nil)})
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
	if status == "completed" {
		e.release(phaseFinal)
	}
	e.release("")
	e.send(typ, map[string]any{"response": e.response(status, extra)})
}

// responsesSearchItem keeps a completed search in both Responses reply forms.
// Keep query for older clients and queries for clients using the current API.
func responsesSearchItem(query string, hits []Hit) map[string]any {
	sources := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		if h.URL != "" {
			sources = append(sources, map[string]any{"type": "url", "url": h.URL})
		}
	}
	return map[string]any{
		"id": "ws_" + newID(), "type": "web_search_call", "status": "completed",
		"action": map[string]any{"type": "search", "query": query, "queries": []string{query}, "sources": sources},
	}
}

// renderResponses is the non-streaming reply.
func renderResponses(res Result, model string, named map[string]nsTool) []byte {
	output := []any{}
	for _, p := range res.Parts {
		switch p.Kind {
		case Text:
			if p.Text == "" && p.Signature != "" {
				continue // only Gemini's signature, which is Gemini's
			}
			output = append(output, map[string]any{"id": "msg_" + newID(), "type": "message", "role": "assistant", "status": "completed",
				"content": []map[string]any{{"type": "output_text", "text": p.Text, "annotations": []any{}}}})
		case Thinking:
			if p.sealedBy(sealResponses) {
				// as the upstream wrote it: id, summary, encrypted_content;
				// made over when it carries its text (another vendor's,
				// #2855)
				output = append(output, magpieMadeReasoningRaw(p.Sealed, ""))
				continue
			}
			if p.sealedBy(sealAnthropic) {
				// Claude's redacted_thinking, whole, to come back (claudeseal)
				output = append(output, map[string]any{"id": "rs_" + newID(), "type": "reasoning", "status": "completed",
					"summary": []any{}, "encrypted_content": claudeseal.Seal(p.Sealed)})
				continue
			}
			claude := p.SignedBy == sealAnthropic && p.Signature != ""
			if p.Text == "" && !claude {
				continue // another API's sealed thinking, or only its signature
			}
			summary := []map[string]any{}
			if p.Text != "" {
				summary = append(summary, map[string]any{"type": "summary_text", "text": p.Text})
			}
			item := map[string]any{"id": "rs_" + newID(), "type": "reasoning", "status": "completed", "summary": summary}
			if claude {
				item["encrypted_content"] = claudeThinking(p.Text, p.Signature)
			}
			output = append(output, item)
		case ToolCall:
			id := p.ID
			if id == "" {
				id = "call_" + newID()
			}
			item := callTo(map[string]any{"type": "function_call", "status": "completed",
				"call_id": id, "arguments": argsString(p)}, p.Name, named)
			item["id"] = itemPrefix(item) + newID()
			output = append(output, item)
		case Search:
			output = append(output, responsesSearchItem(p.Text, p.Hits))
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
	phaseOutput(output, status == "completed")
	b, _ := json.Marshal(map[string]any{"id": id, "object": "response", "created_at": time.Now().Unix(), "status": status,
		"model": model, "output": output, "usage": res.Usage.responses(), "incomplete_details": incomplete,
		"parallel_tool_calls": true, "tool_choice": "auto", "tools": []any{}, "error": nil})
	return b
}
