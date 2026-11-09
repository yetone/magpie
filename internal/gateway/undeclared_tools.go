package gateway

import (
	"bytes"
	"encoding/json"
	"regexp"

	"github.com/yetone/magpie/internal/capre"
	"github.com/yetone/magpie/internal/provider"
)

// An agent that loads its tools when it needs them (pi's and T3 Code's
// tool_search, Gearclickist on Discord) declares a few tools and
// tool_search in each request, while its history holds calls to tools a
// search found earlier and the request no longer declares; one that has
// no tools to offer sends "tools": [] beside a history of calls. OpenAI's
// Chat and Responses APIs and Gemini take such a history. Anthropic's
// Messages API takes a call to a tool it isn't given, but not a call with
// no tools at all ("Requests which include `tool_use` or `tool_result`
// blocks must define tools"), so anthropicBody declares a stub for each
// tool the history called when the request has none, with tool_choice
// none: the client offered nothing to call. A stricter upstream (a relay
// in front of Kiro or Bedrock, a vendor that checks each call's name
// against tools) turns the request away naming the tool; it is asked
// once more with a stub declared for each tool the history called and
// tools lacks, and so from then on. A request whose calls are all
// declared goes as it was.

// historyToolsRefused is how unfit remembers a provider refusing a
// history call to a tool its request didn't declare: the provider's
// validator, not a model, turns it away.
const historyToolsRefused = "tools[].history"

// stubToolDescription is a stub's description: the call it stands for
// was made earlier and the tool isn't offered now.
const stubToolDescription = "Called earlier in this conversation; not available now."

// anthropicToolName is a name Anthropic takes for a tool.
var anthropicToolName = capre.MustCompile(`^[a-zA-Z0-9_-]+$`, 128)

// toolsMissing is an upstream refusing tool calls in a request with no
// tools: Anthropic's "must define tools", Bedrock's "The toolConfig field
// must be defined when using toolUse and toolResult content blocks", or
// tools said required beside the calls.
var toolsMissing = regexp.MustCompile("(?i)must define tools|toolconfig field must be defined|tools?\\W{1,4}(?:param(?:eter)?|field)?\\W{0,4}(?:is |are )?(?:required|must be (?:defined|provided|specified))[^\\n]{0,80}(?:tool_calls?|tool_use|tool_result|toolUse|toolResult|function_call)")

// toolNotDeclared is what follows or leads a tool's name in a refusal of
// a call to it: not found, not defined, unknown.
const toolNotDeclared = `(?:not (?:found|defined|declared|provided|offered|available|present|exist|in (?:the )?(?:tools?|request|list))|unknown|undefined|undeclared|does ?n[o']t exist|no such|missing|invalid)`

// refusesHistoryTools reports whether b, a 4xx body, refuses a history
// call to one of names, the tools the history called that the request
// doesn't declare, or calls in a request with no tools (toolless).
func refusesHistoryTools(b []byte, names []string, toolless bool) bool {
	if toolless && toolsMissing.Match(b) {
		return true
	}
	for _, n := range names {
		q := regexp.QuoteMeta(n)
		name := `(?:^|[^A-Za-z0-9_.-])` + q + `(?:$|[^A-Za-z0-9_-])`
		re, err := regexp.Compile(`(?i)` + toolNotDeclared + `[^\n{}]{0,80}?` + name + `|` + name + `[^\n{}]{0,80}?` + toolNotDeclared)
		if err == nil && re.Match(b) {
			return true
		}
	}
	return false
}

// withHistoryTools is a Chat, Responses or Messages body with a stub
// declared for each tool its history calls and its tools don't, the
// names it declared, and whether tools was empty. tool_choice goes none
// where the client offered no tools, so the model calls none of them.
func withHistoryTools(proto provider.Protocol, body []byte) ([]byte, []string, bool) {
	if proto != provider.Chat && proto != provider.Responses && proto != provider.Anthropic {
		return body, nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return body, nil, false
	}
	tools, _ := m["tools"].([]any)
	declared := map[string]bool{}
	declaredNames(proto, tools, declared)
	var add []string
	for _, n := range calledNames(proto, m) {
		if !declared[n] && (proto != provider.Anthropic || anthropicToolName.MatchString(n)) {
			declared[n] = true
			add = append(add, n)
		}
	}
	if len(add) == 0 {
		return body, nil, false
	}
	toolless := len(tools) == 0
	for _, n := range add {
		tools = append(tools, stubTool(proto, n))
	}
	m["tools"] = tools
	if toolless {
		if _, set := m["tool_choice"]; !set {
			if proto == provider.Anthropic {
				m["tool_choice"] = map[string]any{"type": "none"}
			} else {
				m["tool_choice"] = "none"
			}
		}
	}
	out, err := marshalPlain(m)
	if err != nil {
		return body, nil, false
	}
	return out, add, toolless
}

// declaredNames adds the names tools declares to into: a function's, a
// custom tool's, and on Responses a namespace's own tools.
func declaredNames(proto provider.Protocol, tools []any, into map[string]bool) {
	for _, t := range tools {
		tm, _ := t.(map[string]any)
		if tm == nil {
			continue
		}
		if proto == provider.Responses && tm["type"] == "namespace" {
			nested, _ := tm["tools"].([]any)
			declaredNames(proto, nested, into)
			continue
		}
		if n, _ := tm["name"].(string); n != "" {
			into[n] = true
		}
		for _, k := range []string{"function", "custom"} {
			if fn, _ := tm[k].(map[string]any); fn != nil {
				if n, _ := fn["name"].(string); n != "" {
					into[n] = true
				}
			}
		}
	}
}

// calledNames is the names of the functions m's history calls, in order,
// each once. A Responses call under a namespace is the namespace's, which
// declares its own tools, and is left to it.
func calledNames(proto provider.Protocol, m map[string]any) []string {
	var names []string
	seen := map[string]bool{}
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	switch proto {
	case provider.Chat:
		msgs, _ := m["messages"].([]any)
		for _, x := range msgs {
			msg, _ := x.(map[string]any)
			calls, _ := msg["tool_calls"].([]any)
			for _, c := range calls {
				cm, _ := c.(map[string]any)
				if fn, _ := cm["function"].(map[string]any); fn != nil {
					n, _ := fn["name"].(string)
					add(n)
				}
			}
		}
	case provider.Responses:
		items, _ := m["input"].([]any)
		for _, x := range items {
			it, _ := x.(map[string]any)
			if it == nil || it["type"] != "function_call" {
				continue
			}
			if ns, _ := it["namespace"].(string); ns != "" {
				continue
			}
			n, _ := it["name"].(string)
			add(n)
		}
	case provider.Anthropic:
		msgs, _ := m["messages"].([]any)
		for _, x := range msgs {
			msg, _ := x.(map[string]any)
			blocks, _ := msg["content"].([]any)
			for _, c := range blocks {
				cb, _ := c.(map[string]any)
				if cb != nil && cb["type"] == "tool_use" {
					n, _ := cb["name"].(string)
					add(n)
				}
			}
		}
	}
	return names
}

// stubTool is a function tool named n that takes any object, as proto
// declares one.
func stubTool(proto provider.Protocol, n string) map[string]any {
	params := map[string]any{"type": "object", "properties": map[string]any{}}
	switch proto {
	case provider.Chat:
		return map[string]any{"type": "function", "function": map[string]any{"name": n, "description": stubToolDescription, "parameters": params}}
	case provider.Responses:
		return map[string]any{"type": "function", "name": n, "description": stubToolDescription, "parameters": params}
	}
	return map[string]any{"name": n, "description": stubToolDescription, "input_schema": params}
}

// historyCallNames is the names of the tools r's history calls that
// Anthropic takes as a tool's name, in order, each once.
func historyCallNames(r *Request) []string {
	var names []string
	seen := map[string]bool{}
	for _, m := range r.Messages {
		for _, p := range m.Parts {
			if p.Kind == ToolCall && !seen[p.Name] && anthropicToolName.MatchString(p.Name) {
				seen[p.Name] = true
				names = append(names, p.Name)
			}
		}
	}
	return names
}
