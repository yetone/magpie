package gateway

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
)

// Claude Code (2.1.288) asks Anthropic's API for what its newest models
// take and a relay may not know yet (Keenc on Discord: CHCP and a Vertex
// relay answered 400 for each):
//
//   - thinking.display "updates" (and "highlights"), where a relay that
//     checks the field takes only "summarized" and "omitted": "thinking.
//     adaptive.display: Input should be 'summarized', 'omitted'";
//   - the effort of each turn, as output_config on a {role: "system"}
//     message inside messages: "messages.1.output_config: Extra inputs are
//     not permitted". OpenRouter's Anthropic endpoint takes it as a
//     configuration_update and refuses that on DeepSeek and GLM:
//     "Mid-conversation reasoning effort (configuration_update) is not
//     supported on z-ai/glm-5.3-flash-20260826" (#3696).
//
// Claude Code mends either after the 400, once per conversation, sending
// the request again without it; every new conversation asks again, and a
// relay's refusal is an error in magpie's log each time. magpie mends it
// itself instead: the provider that refused is asked again at once without
// it, and is not sent it again while magpie runs. A provider that takes
// them (Anthropic's own API, a relay passing them on) is sent them as the
// agent wrote them.
//
// Command Code's Provider API turns away a {role: "system"} message inside
// messages at all, the empty one that only carries a turn's effort too:
// "Invalid input at messages.1.role" (#1407). A provider that refuses one is
// sent its system messages' text at the end of the top-level system, and an
// empty one left out.
//
// Copilot's Messages API turns away auto mode's `safeguards` (automode.go)
// on its Claude models: 400 "safeguards: Extra inputs are not permitted"
// (#1756, claude-opus-5.5). A provider that refuses it is sent none, and
// Claude Code, finding no safeguard_results, reviews its actions locally
// or, where Anthropic has turned its local classifier off, asks for
// approval (automode.go).
//
// Claude Code's deferred tools (ToolSearch, MCP tools that come and go)
// put blocks of their own in a message: {"type":"tool_addition","tool":
// {"type":"tool_reference","name":…}} (or a tool_definition) when a tool
// is loaded, a tool_removal when one goes, and a tool_reference. DeepSeek's
// Anthropic endpoint took a tool_addition naming a tool once (10-14), but
// takes one at no position now: "`tool_addition` blocks can only be in
// `system` messages", and a system message's with "unknown variant
// `tool_addition`, expected `text`" (#5404, checked against
// api.deepseek.com on 2026-11-05), so its blocks come out before the
// first ask (isDeepSeekAnthropic). A provider that refuses one is sent
// none of them from then on (withoutDeferredTools); Kimi, whose 400
// names nothing, is never sent them (forwardOnce, #2042). OpenCode's
// DeepSeek route also names nothing: 422 "Endpoint is unavailable.".
//
// ToolSearch's answer is a tool_result whose content is tool_reference
// blocks alone — or, in newer builds, the loaded tool's definition itself,
// which an endpoint that refuses the blocks refuses there too: DeepSeek's
// 422 "unknown variant `tool_definition`, expected `tool_reference`"
// (#5404). It goes as the reference, its definition on the request's tools
// list (withoutDeferredTools). OpenDesign's Anthropic endpoint can't read
// a references-only one: 422 "Failed
// to deserialize the JSON body into the target type: messages[4]: missing
// field `content`" (#2124), while it takes the same result with a text block
// beside the references. A provider that refuses one is sent each such
// result with a text block first, saying which tools are loaded, in the
// words a translated route tells a model (stringOrText); the references and
// the tool_use/tool_result pairing stay.
//
// An endpoint that reads only text and images in a tool_result refuses the
// references themselves: "DeepSeek V4.1 supports text and image content
// only; got 'tool_reference'" (emilochen on X, Claude Code to a DeepSeek
// V4.1 behind an Anthropic-compatible URL). A provider whose 400 names
// tool_reference, other than as what it expected, while the request holds
// one in a tool_result is sent each reference as the text a translated
// route tells a model (toolLoadedText), and the tools they named without
// defer_loading, so the model still sees what ToolSearch loaded
// (withRefsAsText). A translated route (Chat, Responses, Gemini) already
// carries them as that text (stringOrText).

// The refusals, remembered in unfit under the provider's id as betas are.
const (
	shapeDisplayNew = "shape:thinking.display=new" // a display other than summarized or omitted
	shapeDisplay    = "shape:thinking.display"     // display at all
	shapeTurnEffort = "shape:messages[].output_config"
	shapeTurnSystem = "shape:messages[].role=system"
	shapeSafeguards = "shape:safeguards"
	shapeDeferred   = "shape:messages[].content[].tool_addition"
	shapeRefsOnly   = "shape:tool_result.content=tool_reference"
	shapeRefs       = "shape:tool_result.content[].tool_reference"
)

var (
	displayValueRefused = lazyre.New(`thinking\.(?:adaptive|enabled)\.display:? Input should be`)
	displayRefused      = lazyre.New(`thinking\.(?:adaptive|enabled)\.display:? Extra inputs are not permitted`)
	turnEffortRefused   = lazyre.New(`messages\.\d+\.output_config:? Extra inputs are not permitted|Mid-conversation reasoning effort \(configuration_update\) is not supported`)
	turnRoleRefused     = lazyre.New(`messages\.(\d+)\.role\b`)
	safeguardsRefused   = lazyre.New(`\bsafeguards:? Extra inputs are not permitted`)
	deferredRefused     = lazyre.New("messages\\[\\d+\\]\\.content: unknown variant `(?:tool_addition|tool_removal|tool_definition|tool_reference)`")
	refsOnlyRefused     = lazyre.New("missing field `content`")
	// a refusal of tool_reference in words, not in a request echoed back
	// (no brace between); refsRefused leaves out one that names it as what
	// was expected (DeepSeek's "expected `tool_reference`" is about a
	// tool_definition)
	refsNamed    = lazyre.New(`(?i)\b(?:got|unknown|unsupported|not supported|invalid|unexpected|not allowed|not permitted|does ?n[o']t support|cannot|can't)\b[^\n{}]{0,40}\btool_reference\b|\btool_reference\b[^\n{}]{0,40}\b(?:not supported|unsupported|not allowed|not permitted|invalid)`)
	refsExpected = lazyre.New("(?i)expected [`'\"\\\\]*tool_reference\\b")
)

// knownDisplay is a thinking.display every Anthropic endpoint has taken.
func knownDisplay(d string) bool { return d == "summarized" || d == "omitted" }

// refuseShapes notes what of body the provider turned away in errBody, a
// 400's; true when body has something to leave out on the next try.
func (s *Server) refuseShapes(p provider.Provider, errBody, body []byte) bool {
	fresh := false
	mark := func(key string) {
		if s.fits(p.ID, key, provider.Anthropic) {
			s.markUnfit(p.ID, key, provider.Anthropic)
			fresh = true
		}
	}
	// the display as forwardOnce sends it: a budget turned adaptive asks one
	if d := gjson.GetBytes(adaptiveThinking(body), "thinking.display"); d.Exists() {
		if displayRefused.Match(errBody) {
			mark(shapeDisplay)
		} else if displayValueRefused.Match(errBody) && !knownDisplay(d.String()) {
			mark(shapeDisplayNew)
		}
	}
	if turnEffortRefused.Match(errBody) && hasTurnEffort(body) {
		mark(shapeTurnEffort)
	}
	if safeguardsRefused.Match(errBody) && gjson.GetBytes(body, "safeguards").Exists() {
		mark(shapeSafeguards)
	}
	if deferredRefused.Match(errBody) && hasDeferredTools(body) {
		mark(shapeDeferred)
	}
	if refsOnlyRefused.Match(errBody) && hasRefsOnlyResult(body) {
		mark(shapeRefsOnly)
	}
	if refsRefused(errBody) {
		// the references in a tool_result themselves; one a deferred
		// block's refusal names, while such blocks are still sent, is
		// that refusal's
		sent := s.withoutRefusedShapes(p, body)
		if deferredOutUpFront(p, provider.Anthropic, sent) {
			sent = withoutDeferredTools(sent)
		}
		if hasResultRefs(sent) && !(deferredRefused.Match(errBody) && hasDeferredTools(sent)) {
			mark(shapeRefs)
		}
	}
	// a role refused at a message that is a system one, counted in the
	// messages as they were sent; a refusal naming a user or assistant
	// message is over something else
	if m := turnRoleRefused.FindSubmatch(errBody); m != nil {
		sent := s.withoutRefusedShapes(p, body)
		if gjson.GetBytes(sent, "messages."+string(m[1])+".role").String() == "system" {
			mark(shapeTurnSystem)
		}
	}
	return fresh
}

// withoutRefusedShapes is body as the provider takes it, leaving out what
// it has refused before.
func (s *Server) withoutRefusedShapes(p provider.Provider, body []byte) []byte {
	if !s.fits(p.ID, shapeDeferred, provider.Anthropic) {
		body = withoutDeferredTools(body)
	}
	if !s.fits(p.ID, shapeRefs, provider.Anthropic) {
		body = withRefsAsText(body)
	}
	if !s.fits(p.ID, shapeRefsOnly, provider.Anthropic) {
		body = withRefsSaid(body)
	}
	if !s.fits(p.ID, shapeTurnSystem, provider.Anthropic) {
		body = withSystemOnTop(body)
	}
	if !s.fits(p.ID, shapeSafeguards, provider.Anthropic) && gjson.GetBytes(body, "safeguards").Exists() {
		body = withoutFields(body, "safeguards")
	}
	if !bodyHas(body, `"display"`) && !bodyHas(body, `"output_config"`) {
		return body
	}
	if d := gjson.GetBytes(body, "thinking.display"); d.Exists() {
		all := !s.fits(p.ID, shapeDisplay, provider.Anthropic)
		if all || !knownDisplay(d.String()) && !s.fits(p.ID, shapeDisplayNew, provider.Anthropic) {
			body = withDisplay(body, all)
		}
	}
	if !s.fits(p.ID, shapeTurnEffort, provider.Anthropic) {
		body = withoutTurnEffort(body)
	}
	return body
}

// withDisplay is body with thinking.display left out, or (not all) only a
// value beyond summarized and omitted: "highlights" as "summarized", the
// thinking still shown; "updates", or any other, left out, as Claude Code
// itself sends it again.
func withDisplay(body []byte, all bool) []byte {
	var v struct {
		Thinking map[string]any `json:"thinking"`
	}
	if json.Unmarshal(body, &v) != nil || v.Thinking == nil {
		return body
	}
	d, _ := v.Thinking["display"].(string)
	switch {
	case all:
		delete(v.Thinking, "display")
	case knownDisplay(d):
		return body
	case d == "highlights":
		v.Thinking["display"] = "summarized"
	default:
		delete(v.Thinking, "display")
	}
	return withFields(body, map[string]any{"thinking": v.Thinking})
}

// hasTurnEffort is a request with an output_config on one of its messages.
func hasTurnEffort(body []byte) bool {
	if !bodyHas(body, `"output_config"`) {
		return false
	}
	found := false
	getIn(body, "messages").ForEach(func(_, m gjson.Result) bool {
		found = m.Get("output_config").Exists()
		return !found
	})
	return found
}

// withoutTurnEffort is body without the per-turn effort on its messages, as
// Claude Code sends it once refused: a system message that carried nothing
// else goes with it. The request's own output_config, the effort of the
// turn now asked, stays.
func withoutTurnEffort(body []byte) []byte {
	if !hasTurnEffort(body) {
		return body
	}
	var v struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if json.Unmarshal(body, &v) != nil {
		return body
	}
	out := make([]json.RawMessage, 0, len(v.Messages))
	for _, raw := range v.Messages {
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			out = append(out, raw)
			continue
		}
		if _, ok := m["output_config"]; !ok {
			out = append(out, raw)
			continue
		}
		delete(m, "output_config")
		if r := gjson.GetBytes(raw, "role").String(); r == "system" && emptyContent(gjson.GetBytes(raw, "content")) {
			continue
		}
		b, err := marshalPlain(m)
		if err != nil {
			return body
		}
		out = append(out, b)
	}
	return withFields(body, map[string]any{"messages": out})
}

// withSystemOnTop is body with no system message inside messages: the text
// of each goes, in order, at the end of the top-level system, and one with
// none (a turn's effort alone) is left out. The other messages stay as they
// were sent.
func withSystemOnTop(body []byte) []byte {
	if !bodyHas(body, `"system"`) {
		return body
	}
	var v struct {
		System   json.RawMessage   `json:"system"`
		Messages []json.RawMessage `json:"messages"`
	}
	if json.Unmarshal(body, &v) != nil {
		return body
	}
	var moved []any // text blocks for the top-level system
	out := make([]json.RawMessage, 0, len(v.Messages))
	for _, raw := range v.Messages {
		if gjson.GetBytes(raw, "role").String() != "system" {
			out = append(out, raw)
			continue
		}
		c := gjson.GetBytes(raw, "content")
		switch {
		case c.Type == gjson.String && c.String() != "":
			moved = append(moved, map[string]any{"type": "text", "text": c.String()})
		case c.IsArray():
			for _, b := range c.Array() {
				if b.Get("type").String() == "text" && b.Get("text").String() != "" {
					moved = append(moved, json.RawMessage(b.Raw))
				}
			}
		}
	}
	if len(out) == len(v.Messages) {
		return body
	}
	fields := map[string]any{"messages": out}
	if len(moved) > 0 {
		var top []any
		var s string
		var blocks []json.RawMessage
		switch {
		case json.Unmarshal(v.System, &s) == nil:
			if s != "" {
				top = append(top, map[string]any{"type": "text", "text": s})
			}
		case json.Unmarshal(v.System, &blocks) == nil:
			for _, b := range blocks {
				top = append(top, b)
			}
		}
		fields["system"] = append(top, moved...)
	}
	return withFields(body, fields)
}

// emptyContent is a message's content with nothing in it: missing, "", or
// [] (or text blocks with no text).
func emptyContent(c gjson.Result) bool {
	switch {
	case !c.Exists() || c.Type == gjson.Null:
		return true
	case c.Type == gjson.String:
		return c.String() == ""
	case c.IsArray():
		empty := true
		c.ForEach(func(_, b gjson.Result) bool {
			empty = b.Get("type").String() == "text" && b.Get("text").String() == ""
			return empty
		})
		return empty
	}
	return false
}

// deferredBlock is a message block of Claude Code's deferred tools.
func deferredBlock(typ string) bool {
	return typ == "tool_addition" || typ == "tool_removal" || typ == "tool_reference"
}

// hasDeferredTools is a request with a deferred-tool block in a message's
// content, or with a tool_definition in a tool_result's — the ToolSearch
// answer of newer Claude Code builds, which an endpoint that refuses the
// blocks refuses there too (#5404). A tool_reference inside a tool_result
// (ToolSearch's older answer) is not one.
func hasDeferredTools(body []byte) bool {
	if !bodyHas(body, `"tool_addition"`) && !bodyHas(body, `"tool_removal"`) && !bodyHas(body, `"tool_reference"`) && !bodyHas(body, `"tool_definition"`) {
		return false
	}
	found := false
	getIn(body, "messages").ForEach(func(_, m gjson.Result) bool {
		m.Get("content").ForEach(func(_, b gjson.Result) bool {
			found = deferredBlock(b.Get("type").String()) ||
				b.Get("type").String() == "tool_result" && strings.Contains(b.Get("content").Raw, `"tool_definition"`)
			return !found
		})
		return !found
	})
	return found
}

// withoutDeferredTools is body without the deferred-tool blocks in its
// messages, and without a message they leave empty. Definitions carried
// only inline join the request's own tools list; a definition already on
// that list wins. A tool_result's tool_reference stays: it is
// the tool's answer, and Kimi and DeepSeek take it. A tool_definition in
// a tool_result's content (a ToolSearch answer carrying the definition
// itself, #5404) joins the tools as its block goes as the reference a
// tool_result takes. A body with none is sent as it was.
func withoutDeferredTools(body []byte) []byte {
	if !hasDeferredTools(body) {
		return body
	}
	var v struct {
		Messages []json.RawMessage `json:"messages"`
		Tools    []json.RawMessage `json:"tools"`
	}
	if json.Unmarshal(body, &v) != nil {
		return body
	}
	have := make(map[string]bool, len(v.Tools))
	for _, tool := range v.Tools {
		have[gjson.GetBytes(tool, "name").String()] = true
	}
	tools := v.Tools
	out := make([]json.RawMessage, 0, len(v.Messages))
	for _, raw := range v.Messages {
		c := gjson.GetBytes(raw, "content")
		if !c.IsArray() {
			out = append(out, raw)
			continue
		}
		var kept []json.RawMessage
		changed := false
		for _, b := range c.Array() {
			if b.Get("type").String() == "tool_addition" && b.Get("tool.type").String() == "tool_definition" {
				def := b.Get("tool.definition")
				if name := def.Get("name"); def.IsObject() && name.Type == gjson.String && name.String() != "" && !have[name.String()] {
					tools = append(tools, json.RawMessage(def.Raw))
					have[name.String()] = true
				}
			}
			if b.Get("type").String() == "tool_result" && strings.Contains(b.Get("content").Raw, `"tool_definition"`) {
				// a ToolSearch answer carrying the definition itself: the
				// definition joins the tools and the block goes as the
				// reference the result takes (#5404)
				if fixed, added := resultDefinitionRef(b, have); fixed != nil {
					tools = append(tools, added...)
					kept = append(kept, fixed)
					changed = true
					continue
				}
			}
			if !deferredBlock(b.Get("type").String()) {
				kept = append(kept, json.RawMessage(b.Raw))
			}
		}
		if !changed && len(kept) == len(c.Array()) {
			out = append(out, raw)
			continue
		}
		if len(kept) == 0 {
			continue // it said only that tools came or went
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			return body
		}
		content, err := marshalPlain(kept)
		if err != nil {
			return body
		}
		m["content"] = content
		b, err := marshalPlain(m)
		if err != nil {
			return body
		}
		out = append(out, b)
	}
	fields := map[string]any{"messages": out}
	if len(tools) != len(v.Tools) {
		fields["tools"] = tools
	}
	return withFields(body, fields)
}

// resultDefinitionRef is a tool_result whose content carries a
// tool_definition, the ToolSearch answer of newer Claude Code builds: each
// definition joins the request's tools (have dedupes by name) and the
// block goes back as the tool_reference a tool_result's content takes.
// DeepSeek's Anthropic endpoint refuses the definition there with
// "unknown variant `tool_definition`, expected `tool_reference`", and a
// reference only once the tool is on the tools list (#5404, a 2026-11-05
// check against api.deepseek.com). nil when the result can't be read, or
// a definition in it has no name: it goes as it came.
func resultDefinitionRef(b gjson.Result, have map[string]bool) (json.RawMessage, []json.RawMessage) {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(b.Raw), &m) != nil {
		return nil, nil
	}
	content := b.Get("content")
	if !content.IsArray() {
		return nil, nil
	}
	var added []json.RawMessage
	items := make([]json.RawMessage, 0, len(content.Array()))
	for _, item := range content.Array() {
		if item.Get("type").String() != "tool_definition" {
			items = append(items, json.RawMessage(item.Raw))
			continue
		}
		def := item.Get("definition")
		name := def.Get("name").String()
		if name == "" {
			return nil, nil // a definition without a name: the result goes as it came
		}
		if def.IsObject() && !have[name] {
			added = append(added, json.RawMessage(def.Raw))
			have[name] = true
		}
		ref, err := marshalPlain(map[string]string{"type": "tool_reference", "tool_name": name})
		if err != nil {
			return nil, nil
		}
		items = append(items, ref)
	}
	cb, err := marshalPlain(items)
	if err != nil {
		return nil, nil
	}
	m["content"] = cb
	out, err := marshalPlain(m)
	if err != nil {
		return nil, nil
	}
	return out, added
}

// toolLoadedText is what a model is told of a tool ToolSearch loaded, where
// the tool_reference that names it can't go as it is.
func toolLoadedText(name string) string {
	return "Tool " + name + " is loaded and can be called now."
}

// toolRefs are the tools a tool_result's content names in tool_reference
// blocks, as Claude Code's ToolSearch answers, in their order.
func toolRefs(content json.RawMessage) []string {
	if !bodyHas(content, `"tool_reference"`) {
		return nil
	}
	var names []string
	gjson.ParseBytes(content).ForEach(func(_, b gjson.Result) bool {
		if n := b.Get("tool_name").String(); b.Get("type").String() == "tool_reference" && n != "" {
			names = append(names, n)
		}
		return true
	})
	return names
}

// refsOnly is a tool_result's content that is tool_reference blocks and
// nothing else, as ToolSearch answers.
func refsOnly(c gjson.Result) bool {
	if !c.IsArray() || len(c.Array()) == 0 {
		return false
	}
	only := true
	c.ForEach(func(_, b gjson.Result) bool {
		only = b.Get("type").String() == "tool_reference"
		return only
	})
	return only
}

// hasRefsOnlyResult is a request with a tool_result holding only
// tool_reference blocks.
func hasRefsOnlyResult(body []byte) bool {
	if !bodyHas(body, `"tool_reference"`) {
		return false
	}
	found := false
	getIn(body, "messages").ForEach(func(_, m gjson.Result) bool {
		m.Get("content").ForEach(func(_, b gjson.Result) bool {
			found = b.Get("type").String() == "tool_result" && refsOnly(b.Get("content"))
			return !found
		})
		return !found
	})
	return found
}

// withRefsSaid is body with a text block first in each tool_result that
// holds only tool_reference blocks, naming the tools they load; the
// references stay after it. A body with none is sent as it was.
func withRefsSaid(body []byte) []byte {
	if !hasRefsOnlyResult(body) {
		return body
	}
	var v struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if json.Unmarshal(body, &v) != nil {
		return body
	}
	out := make([]json.RawMessage, 0, len(v.Messages))
	for _, raw := range v.Messages {
		c := gjson.GetBytes(raw, "content")
		if !c.IsArray() {
			out = append(out, raw)
			continue
		}
		changed := false
		blocks := make([]json.RawMessage, 0, len(c.Array()))
		for _, b := range c.Array() {
			rc := b.Get("content")
			if b.Get("type").String() != "tool_result" || !refsOnly(rc) {
				blocks = append(blocks, json.RawMessage(b.Raw))
				continue
			}
			var said []string
			for _, r := range rc.Array() {
				said = append(said, toolLoadedText(r.Get("tool_name").String()))
			}
			content := []any{map[string]any{"type": "text", "text": strings.Join(said, "\n")}}
			for _, r := range rc.Array() {
				content = append(content, json.RawMessage(r.Raw))
			}
			var m map[string]json.RawMessage
			if json.Unmarshal([]byte(b.Raw), &m) != nil {
				return body
			}
			cb, err := marshalPlain(content)
			if err != nil {
				return body
			}
			m["content"] = cb
			nb, err := marshalPlain(m)
			if err != nil {
				return body
			}
			blocks = append(blocks, nb)
			changed = true
		}
		if !changed {
			out = append(out, raw)
			continue
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			return body
		}
		cb, err := marshalPlain(blocks)
		if err != nil {
			return body
		}
		m["content"] = cb
		nb, err := marshalPlain(m)
		if err != nil {
			return body
		}
		out = append(out, nb)
	}
	return withFields(body, map[string]any{"messages": out})
}

// refsRefused is a refusal that names tool_reference other than as what
// the endpoint expected.
func refsRefused(errBody []byte) bool {
	if !bodyHas(errBody, "tool_reference") {
		return false
	}
	return refsNamed.Match(refsExpected.ReplaceAll(errBody, nil))
}

// hasResultRefs is a request with a tool_reference block in a
// tool_result's content.
func hasResultRefs(body []byte) bool {
	if !bodyHas(body, `"tool_reference"`) {
		return false
	}
	found := false
	getIn(body, "messages").ForEach(func(_, m gjson.Result) bool {
		m.Get("content").ForEach(func(_, b gjson.Result) bool {
			found = b.Get("type").String() == "tool_result" && holdsRef(b.Get("content"))
			return !found
		})
		return !found
	})
	return found
}

// holdsRef is a tool_result's content with a tool_reference block in it.
func holdsRef(c gjson.Result) bool {
	if !c.IsArray() || !strings.Contains(c.Raw, `"tool_reference"`) {
		return false
	}
	found := false
	c.ForEach(func(_, b gjson.Result) bool {
		found = b.Get("type").String() == "tool_reference"
		return !found
	})
	return found
}

// withRefsAsText is body with each tool_reference block in a tool_result's
// content as a text block saying the tool is loaded (toolLoadedText), and
// the tools they name without defer_loading, offered from the start as the
// endpoint can't load them by reference. The tool_use/tool_result pairing
// stays. A body with none is sent as it was.
func withRefsAsText(body []byte) []byte {
	if !hasResultRefs(body) {
		return body
	}
	var v struct {
		Messages []json.RawMessage `json:"messages"`
		Tools    []json.RawMessage `json:"tools"`
	}
	if json.Unmarshal(body, &v) != nil {
		return body
	}
	named := map[string]bool{}
	out := make([]json.RawMessage, 0, len(v.Messages))
	for _, raw := range v.Messages {
		c := gjson.GetBytes(raw, "content")
		if !c.IsArray() {
			out = append(out, raw)
			continue
		}
		changed := false
		blocks := make([]json.RawMessage, 0, len(c.Array()))
		for _, b := range c.Array() {
			rc := b.Get("content")
			if b.Get("type").String() != "tool_result" || !holdsRef(rc) {
				blocks = append(blocks, json.RawMessage(b.Raw))
				continue
			}
			content := make([]json.RawMessage, 0, len(rc.Array()))
			for _, r := range rc.Array() {
				if r.Get("type").String() != "tool_reference" {
					content = append(content, json.RawMessage(r.Raw))
					continue
				}
				name := r.Get("tool_name").String()
				if name == "" {
					continue // names nothing to say
				}
				named[name] = true
				t, err := marshalPlain(map[string]string{"type": "text", "text": toolLoadedText(name)})
				if err != nil {
					return body
				}
				content = append(content, t)
			}
			var m map[string]json.RawMessage
			if json.Unmarshal([]byte(b.Raw), &m) != nil {
				return body
			}
			cb, err := marshalPlain(content)
			if err != nil {
				return body
			}
			m["content"] = cb
			nb, err := marshalPlain(m)
			if err != nil {
				return body
			}
			blocks = append(blocks, nb)
			changed = true
		}
		if !changed {
			out = append(out, raw)
			continue
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			return body
		}
		cb, err := marshalPlain(blocks)
		if err != nil {
			return body
		}
		m["content"] = cb
		nb, err := marshalPlain(m)
		if err != nil {
			return body
		}
		out = append(out, nb)
	}
	fields := map[string]any{"messages": out}
	toolsChanged := false
	tools := make([]json.RawMessage, 0, len(v.Tools))
	for _, t := range v.Tools {
		if !named[gjson.GetBytes(t, "name").String()] || !gjson.GetBytes(t, "defer_loading").Exists() {
			tools = append(tools, t)
			continue
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(t, &m) != nil {
			return body
		}
		delete(m, "defer_loading")
		nt, err := marshalPlain(m)
		if err != nil {
			return body
		}
		tools = append(tools, nt)
		toolsChanged = true
	}
	if toolsChanged {
		fields["tools"] = tools
	}
	return withFields(body, fields)
}

// deferredOutUpFront is a provider sent no deferred-tool block from the
// first ask (withoutDeferredTools), as forwardOnce sends it: Kimi's
// Anthropic endpoint turns the whole request away with a 400 that names
// nothing to learn from (#2042), DeepSeek's own takes a tool_addition at
// no position (#5404), and OpenCode's DeepSeek route hides the refusal
// behind a 422 "Endpoint is unavailable.".
func deferredOutUpFront(p provider.Provider, to provider.Protocol, body []byte) bool {
	return isKimi(p, to) || isDeepSeekAnthropic(p, to) || p.IsOpenCode() && strings.Contains(strings.ToLower(modelOf(body)), "deepseek")
}
