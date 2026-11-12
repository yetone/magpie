package gateway

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/lazyre"
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
	// sealed: a redacted_thinking block as Anthropic wrote it, which goes
	// back as it came (Part.Sealed, #1445)
	sealed json.RawMessage
}

// MarshalJSON writes a thinking block's text even when it is empty.
// Messages requires the field: a signed block with no text (Claude Code's
// thinking when its display is omitted) sent without it is refused,
// "messages.N.content.0.thinking.thinking: Field required" (#1447). Every
// other block keeps its omitempty fields.
func (b aBlock) MarshalJSON() ([]byte, error) {
	type plain aBlock
	if len(b.sealed) > 0 {
		return b.sealed, nil
	}
	if b.Type != "thinking" {
		return json.Marshal(plain(b))
	}
	// a thinking block always says its signature, empty for one magpie
	// sends unsigned (noReasoning, or a turn's thinking replayed): the
	// Messages API schema has it as a string, and OpenRouter's endpoint
	// turns a block without one away (#2883)
	return json.Marshal(struct {
		plain
		Thinking  string `json:"thinking"`
		Signature string `json:"signature"`
	}{plain(b), b.Thinking, b.Signature})
}

// ephemeral marks a prompt-cache breakpoint.
var ephemeral = map[string]string{"type": "ephemeral"}

// addedMark is the mark anthropicBody puts on what it marks for the cache:
// Anthropic's default five minutes, or an hour for a request whose turns
// are often further apart (Request.hourMarks) to a Claude model. A 1h write
// costs 2x the input price where a 5m one costs 1.25x, and a read 0.1x or
// less: one turn after a pause of 5 to 60 minutes, read where it would
// have been written again, pays for the dearer writes of dozens of
// requests in quick succession. Every mark it adds has the same TTL, so
// none comes after a shorter one, which Anthropic refuses.
func addedMark(r *Request, model string) map[string]string {
	if r.hourMarks && anthropicModel.MatchString(model) {
		return cacheMark1h()
	}
	return ephemeral
}

type aRequest struct {
	Model        string          `json:"model"`
	System       json.RawMessage `json:"system,omitempty"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
	Messages     []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Type         string          `json:"type,omitempty"`
		Name         string          `json:"name"`
		Description  string          `json:"description,omitempty"`
		InputSchema  json.RawMessage `json:"input_schema,omitempty"`
		CacheControl json.RawMessage `json:"cache_control,omitempty"`
		DeferLoading bool            `json:"defer_loading,omitempty"`
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
	return parseAnthropicRequest(body, false)
}

func parseAnthropicRequest(body []byte, clientMarks bool) (*Request, error) {
	return readAnthropic(body, clientMarks, false)
}

// readAnthropic is parseAnthropicRequest; for facts (readFacts) it leaves
// out what they never read, as readChat does: the assistant's blocks, the
// tools' outputs, and the copies of raw values a parse kept would need.
func readAnthropic(body []byte, clientMarks, facts bool) (*Request, error) {
	decode := decodeJSON
	if facts {
		decode = decodeJSONView
	}
	var a aRequest
	if err := decode(body, &a); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Model: a.Model, System: stringOrText(a.System), MaxTokens: a.MaxTokens,
		Temp: a.Temperature, TopP: a.TopP, Stop: a.StopSequences, Stream: a.Stream, Fast: a.Speed == "fast"}
	if len(a.System) > 0 {
		r.systemRaw = []json.RawMessage{a.System}
	}
	if !facts {
		r.askedTTL = askedCacheTTL(&a)
	}
	if clientMarks {
		var err error
		r.clientCache, err = readClientCache(a)
		if err != nil {
			return nil, err
		}
	}
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
	for mi, m := range a.Messages {
		msg := Message{Role: m.Role}
		if facts && m.Role == "assistant" {
			r.Messages = append(r.Messages, msg)
			continue
		}
		var s string
		if decodeJSON(m.Content, &s) == nil {
			if s != "" {
				msg.Parts = append(msg.Parts, Part{Kind: Text, Text: s})
			}
		} else {
			var blocks []aBlock
			if err := decode(m.Content, &blocks); err != nil {
				return nil, fmt.Errorf("invalid message content: %v", err)
			}
			var raws []json.RawMessage // each block as the client sent it
			for i, b := range blocks {
				before := len(msg.Parts)
				switch b.Type {
				case "text":
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: b.Text, block: true})
				case "image":
					if b.Source != nil {
						msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: b.Source.MediaType, Data: b.Source.Data, URL: b.Source.URL, block: true})
					}
				case "video":
					// MiniMax's Anthropic endpoint takes a video in the image
					// block's shape (#6135): a File of the video, which every
					// builder sends on or turns away, never leaves out
					if b.Source != nil {
						if v, ok := videoPart(cmp.Or(b.Source.Data, b.Source.URL), cmp.Or(b.Source.MediaType, "video/mp4"), ""); ok {
							v.block = true
							msg.Parts = append(msg.Parts, v)
						}
					}
				case "tool_use":
					msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: anthropicInID(b.ID), Name: b.Name, Args: b.Input, block: true})
				case "tool_result":
					if facts {
						msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: anthropicInID(b.ToolUseID)})
						break
					}
					out, images := toolOutput(b.Content)
					msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: anthropicInID(b.ToolUseID), Text: out, Images: images, IsError: b.IsError, Loads: toolRefs(b.Content), block: true})
				case "thinking":
					msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: b.Thinking, Signature: b.Signature, block: true})
				case "redacted_thinking":
					// Claude's reasoning its safety systems sealed: it
					// goes back to Messages whole, and is nothing to
					// another API (#1445)
					if raws == nil {
						_ = decodeJSON(m.Content, &raws)
					}
					if i < len(raws) {
						msg.Parts = append(msg.Parts, Part{Kind: Thinking, Sealed: raws[i], SealedBy: sealAnthropic, block: true})
					}
				}
				if clientMarks && !(mi == len(a.Messages)-1 && i == len(blocks)-1) {
					if ttl := cacheTTL(b.CacheControl); ttl != "" {
						if len(msg.Parts) > before && renderedCacheBlock(b) {
							msg.Parts[len(msg.Parts)-1].cache = ttl
						} else {
							r.clientCache.droppedMessages++
						}
					}
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
			if strings.HasPrefix(t.Type, "advisor_") {
				// Claude Code's advisor: its instructions, which would
				// have the model call a tool it hasn't got (advisor.go)
				for i := range r.Messages {
					for j, pt := range r.Messages[i].Parts {
						if pt.Kind == Text {
							r.Messages[i].Parts[j].Text, _ = advisorGoneIn(pt.Text)
						}
					}
				}
			}
			continue
		}
		if t.Name == "DeferredToolPlaceholder" {
			// only keeps Anthropic's deferred loading on; never to be called
			continue
		}
		r.Tools = append(r.Tools, Tool{Name: t.Name, Description: t.Description, Schema: t.InputSchema, Deferred: t.DeferLoading})
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
	// between_tools is Sonnet 5.5's thinking off (#1454)
	if th := a.Thinking; th != nil && (th.Type == "disabled" || th.Type == "between_tools") {
		r.ThinkOff = true
	} else if th != nil && (th.Type == "enabled" || th.Type == "adaptive") {
		r.Thinking = true
		r.Effort = effortOfBudget(th.BudgetTokens)
		if oc := a.OutputConfig; oc != nil && effortOf(oc.Effort) != "" {
			r.Effort = effortOf(oc.Effort)
		} else if e := turnEffort(body); e != "" {
			// the level of the turn, kept in a message of its own (Claude
			// Desktop's and Claude Code's {role: "system"} message with an
			// output_config), is the one picked; the budget beside it is
			// only nearest to it (a High asked as 31999 read as Max)
			r.Effort = e
		}
	}
	return r, nil
}

// turnEffort is the last effort a message of an Anthropic request carries
// in its own output_config; "" when none does.
func turnEffort(body []byte) string {
	if !bytes.Contains(body, []byte(`"output_config"`)) {
		return ""
	}
	last := ""
	for _, e := range gjson.GetBytes(body, "messages.#.output_config.effort").Array() {
		if v := effortOf(e.String()); v != "" {
			last = v
		}
	}
	return last
}

// thinkingOffUnlessAsked says thinking is off when the request doesn't
// mention it. That is what Anthropic's API assumes, but DeepSeek and other
// vendors' Anthropic endpoints think by default, so Claude Code's requests
// for a session title, sent without thinking, spent their tokens thinking.
func thinkingOffUnlessAsked(body []byte) []byte {
	var v struct {
		Thinking json.RawMessage `json:"thinking"`
	}
	if decodeJSON(body, &v) != nil || v.Thinking != nil {
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

// anthropicReplaysThinking is whether model, served by host's Messages
// API, wants a thinking block on every assistant turn that calls a tool:
// DeepSeek's Anthropic endpoint, thinking (its default, and when asked),
// refuses a tool_use turn with none, "The `content[].thinking` in the
// thinking mode must be passed back to the API", and turns
// redacted_thinking away as a block it doesn't know (422), checked against
// api.deepseek.com/anthropic on 2026-10-11 (#1611). It takes Claude's
// signed thinking, its own, one with no signature and a space.
func anthropicReplaysThinking(host, model string) bool {
	if host == "api.anthropic.com" || anthropicModel.MatchString(model) {
		return false
	}
	return strings.Contains(host, "deepseek") || strings.Contains(strings.ToLower(model), "deepseek")
}

// withThinkingOnToolTurns gives each assistant message of a Messages
// request that has a tool_use, or that answers tool results alone (DeepSeek
// refuses that one too, checked 2026-10-12, #1822), and no thinking block
// one holding noReasoning, and turns a redacted_thinking block (Claude's, sealed, which
// DeepSeek can't read) into that too, or leaves it out when the message
// has thinking already, for anthropicReplaysThinking. A Claude Code routing
// group that falls from Claude to DeepSeek sends both (#1611). Every other
// message, and a body with nothing to change, goes byte for byte.
func withThinkingOnToolTurns(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"tool_use"`)) && !bytes.Contains(body, []byte(`"redacted_thinking"`)) {
		return body
	}
	var q map[string]json.RawMessage
	var msgs []json.RawMessage
	if decodeJSON(body, &q) != nil || decodeJSON(q["messages"], &msgs) != nil {
		return body
	}
	space, _ := json.Marshal(aBlock{Type: "thinking", Thinking: noReasoning})
	changed := false
	// afterTool: the message before is the user's tool results alone
	afterTool := false
	for i, raw := range msgs {
		var m map[string]json.RawMessage
		var role string
		var blocks []json.RawMessage
		if decodeJSON(raw, &m) != nil || decodeJSON(m["role"], &role) != nil {
			afterTool = false
			continue
		}
		if role != "assistant" {
			afterTool = decodeJSON(m["content"], &blocks) == nil && len(blocks) > 0 &&
				!slices.ContainsFunc(blocks, func(b json.RawMessage) bool {
					var t struct {
						Type string `json:"type"`
					}
					return decodeJSON(b, &t) != nil || t.Type != "tool_result"
				})
			continue
		}
		after := afterTool
		afterTool = false
		if decodeJSON(m["content"], &blocks) != nil {
			continue
		}
		var out []json.RawMessage
		thought, called, edited := false, false, false
		for _, b := range blocks {
			var t struct {
				Type string `json:"type"`
			}
			decodeJSON(b, &t)
			switch t.Type {
			case "thinking":
				thought = true
			case "tool_use":
				called = true
			case "redacted_thinking":
				edited = true
				if thought {
					continue
				}
				b, thought = space, true
			}
			out = append(out, b)
		}
		if (called || after) && !thought {
			out = append([]json.RawMessage{space}, out...)
			edited = true
		}
		if !edited {
			continue
		}
		m["content"], _ = json.Marshal(out)
		if nb, err := json.Marshal(m); err == nil {
			msgs[i], changed = nb, true
		}
	}
	if !changed {
		return body
	}
	q["messages"], _ = json.Marshal(msgs)
	nb, err := marshalJSON(q)
	if err != nil {
		return body
	}
	return nb
}

// anthropicModel is one of Anthropic's own models, which think only when asked,
// so a request for one is left as the agent sent it.
var anthropicModel = lazyre.New(`(?i)(?:^|[/.:-])claude-`)

// alwaysThinks is a vendor refusing to turn a model's thinking off: Z.ai's
// GLM-5.3 answers 1210, "…always engages in thinking…", or, in Chinese
// (ZCode's GLM-5.3-Flash), "该模型始终支持思考，不可关闭" (#699); DashScope's
// own glm-5.3 answers "The value of the enable_thinking parameter is
// restricted to True." MiniMax's MiniMax-M3.1-Flash-Preview answers
// "…requires adaptive thinking; thinking.type="disabled" (including
// reasoning.effort=none) is not allowed (2013)", and Volcengine Ark's
// glm-5.3 "thinking.type `disabled` is not supported by this model"
// (#1141). Kimi Code (China)'s K3.1 answers "…does not support disabled
// thinking mode" (#3739).
var alwaysThinks = lazyre.New(`(?i)always engages in thinking|thinking (?:can ?not|can't) be (?:disabled|turned off)|enable_thinking[^"]{0,60}restricted to true|始终(?:支持|开启|启用)?思考|思考[^"]{0,20}(?:不可|无法|不能)关闭|requires adaptive thinking\W+thinking\.type\W+disabled\W+\(including reasoning\.effort=none\) is not allowed|thinking\.type \x60disabled\x60 is not supported by this model|does not support disabled thinking mode`)

// refusesThinkingOff is an Anthropic endpoint turning away a request
// with thinking turned off (thinking.type disabled): a model that always
// thinks (alwaysThinks), OpenRouter's mandatory reasoning, a Claude that
// takes no disabled (thinkingOffRefused), or a relay reading disabled as
// reasoning effort none and turning that level away by naming the ones
// it takes: Sub2API's gpt-6.1-sol, "gpt-6.1-sol does not support reasoning
// effort \"none\"; use low, medium, high, xhigh or max" (#1465).
func refusesThinkingOff(b []byte) bool {
	// alwaysThinks' words are matched decoded, too (#1678): 思考 escaped as \uXXXX
	return alwaysThinks.Match(wordsOf(b)) || mandatoryReasoning.Match(b) || thinkingOffRefused.Match(b) ||
		effortLevelsNamed.Match(b) && noneQuoted.Match(b)
}

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
	if decodeJSON(body, &q) != nil || decodeJSON(q["thinking"], &th) != nil || th.Type != "disabled" {
		return nil, false
	}
	delete(q, "thinking")
	out, err := marshalJSON(q)
	return out, err == nil
}

// claudeVersion finds the family's version in a Claude model id however a
// relay spells it: claude-opus-4-6, claude-opus-5, anthropic.claude-sonnet-4.6-v1,
// a relay's opus-5.5, claude-fable-5-1, or the old order, claude-3-7-sonnet.
var claudeVersion = lazyre.New(`(?:^|[^a-z0-9])(?:claude-)?(?:opus|sonnet|haiku|fable|mythos)-(\d{1,2})(?:[-.](\d{1,2}))?(?:[^0-9]|$)|claude-(\d+)(?:[-.](\d))?-(?:opus|sonnet|haiku)`)

// adaptiveOnly is a Claude model from 4.6 on, which thinks adaptively:
// claude-opus-5-5 refuses thinking.type=enabled with a budget ("requires
// adaptive thinking"), as do Fable and Mythos 5 and 5.1, whose adaptive
// thinking is always on, so how hard it thinks goes in output_config.effort.
func adaptiveOnly(model string) bool {
	major, minor, ok := claudeVersionOf(model)
	return ok && (major > 4 || major == 4 && minor >= 6)
}

// noXhigh is a Claude that thinks only adaptively but has no xhigh effort,
// Opus and Sonnet 4.6, so it is asked max in its place. Every later one
// takes xhigh (platform.claude.com/docs/en/build-with-claude/effort), and
// max there spends without limit.
func noXhigh(model string) bool {
	major, minor, ok := claudeVersionOf(model)
	return ok && major == 4 && minor == 6
}

// claudeVersionOf is a Claude model's version as claudeVersion reads it;
// ok is false for a model it doesn't read as a Claude.
func claudeVersionOf(model string) (major, minor int, ok bool) {
	m := claudeVersion.FindStringSubmatch(strings.ToLower(model))
	if m == nil {
		return 0, 0, false
	}
	v := m[1:3]
	if m[3] != "" {
		v = m[3:5]
	}
	major, _ = strconv.Atoi(v[0])
	minor, _ = strconv.Atoi(v[1])
	return major, minor, true
}

// adaptiveThinking is an Anthropic request as a model that thinks only
// adaptively takes it: thinking.type=enabled with a budget — sent by an
// agent that doesn't know the model (its name in magpie, an alias, or a
// group's member, mapped to the vendor's later), or by magpie fitting an
// effort to it — goes as thinking.type=adaptive, with the budget as the
// effort it is nearest in output_config.effort unless one is there (Keenc
// on Discord: claude-opus-5-5 answered 400). Read off the model the body
// is sent with, the vendor's own name; any other request, older Claudes'
// included, goes as it came, and "disabled" stays. The thinking enabled
// shows (display "summarized" by default) is still shown: Opus 4.7 and
// later leave it out of adaptive thinking unless asked (#1485), so it is
// asked for unless the request names its own display. A provider that
// refuses display has it taken out after (withoutRefusedShapes).
func adaptiveThinking(body []byte) []byte {
	th := gjson.GetBytes(body, "thinking")
	if th.Get("type").String() != "enabled" || !adaptiveOnly(gjson.GetBytes(body, "model").String()) {
		return body
	}
	thinking := map[string]any{"type": "adaptive", "display": "summarized"}
	if d := th.Get("display"); d.Exists() {
		thinking["display"] = d.Value()
	}
	fields := map[string]any{"thinking": thinking}
	// a level a turn's message carries is the one asked: a top-level effort
	// made of the budget would be read first by the next magpie, and by
	// parseAnthropicRequest, over it (a High asked as 31999 went on as max;
	// DIYgod on X)
	if gjson.GetBytes(body, "output_config.effort").String() == "" && turnEffort(body) == "" {
		if e := effortOfBudget(int(th.Get("budget_tokens").Int())); e != "" {
			if e == "xhigh" {
				e = "max" // a budget can't tell xhigh from max (budgetOf), and 4.6 has no xhigh
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

// claudeLine finds a Claude model's family and version however a relay
// spells it: claude-sonnet-5-5, claude-sonnet-5.5, a relay's sonnet-5.5,
// Bedrock's anthropic.claude-sonnet-5-5-v1:0, Vertex's
// claude-sonnet-5-5@20261001, claude-fable-5-1.
var claudeLine = lazyre.New(`(?:^|[^a-z0-9])(?:claude-)?(opus|sonnet|haiku|fable|mythos)-(\d{1,2})(?:[-.](\d{1,2}))?(?:[^0-9]|$)`)

// thinkingOff is how a Claude model takes thinking turned off.
type thinkingOff int

const (
	offUnknown      thinkingOff = iota // not a model these rules know: as sent
	offTaken                           // thinking.type=disabled at any effort
	offUpToHigh                        // disabled at effort high or below: Opus 5, Haiku 5.5
	offBetweenTools                    // disabled refused, between_tools asked for: Sonnet 5.5
	offNever                           // thinking can't be turned off: Opus 5.5, Fable 5, Mythos 5
)

// thinkingOffOf is how model takes thinking turned off, from Anthropic's
// own rules: Sonnet 5.5 answers disabled with 400 "To turn thinking off on
// this model, send "thinking": {"type": "between_tools"}…" and takes
// between_tools at effort high or below; Opus 5.5 and the Fable and
// Mythos models refuse disabled at every effort, so thinking is left out
// (they think adaptively); Opus 5 and Haiku 5.5 refuse it at xhigh and
// max. Every other Claude takes it as sent, and between_tools on none of
// them. A model of a later line than these is offUnknown, left as sent.
func thinkingOffOf(model string) thinkingOff {
	m := claudeLine.FindStringSubmatch(strings.ToLower(model))
	if m == nil {
		return offUnknown
	}
	major, _ := strconv.Atoi(m[2])
	minor, _ := strconv.Atoi(m[3])
	family := m[1]
	switch {
	case family == "fable" || family == "mythos":
		if major == 5 {
			return offNever
		}
	case major < 5:
		return offTaken
	case major == 5 && minor == 0:
		if family == "opus" {
			return offUpToHigh
		}
		return offTaken // Sonnet 5, Haiku 5
	case major == 5 && minor == 5:
		switch family {
		case "sonnet":
			return offBetweenTools
		case "opus":
			return offNever
		case "haiku":
			return offUpToHigh
		}
	}
	return offUnknown
}

// thinkingOffAsTaken is an Anthropic request that turns thinking off, as
// the model it is sent to takes that (thinkingOffOf). Claude Code turns
// thinking off for auto mode's classifier and its side queries whenever
// it can't tell the model refuses it — a model reached through a gateway
// is one — and magpie turns it off itself when fitting an effort of none
// or a budget that doesn't fit; claude-sonnet-5-5 answered every one of
// them 400 (#1454). Sonnet 5.5 is sent between_tools, alone in thinking,
// with an effort above high asked as high; Opus 5.5, Fable and Mythos are
// sent no thinking, at effort low unless one is asked, with the room a
// thinking model needs on top of a short reply, as Claude Code gives it
// (classifierRoom); Opus 5 and Haiku 5.5 are asked effort high in place
// of xhigh or max. between_tools sent to a model that doesn't take it goes
// as disabled, or as no thinking where disabled is refused too. model is
// the vendor's name of it: the body's, or, for Bedrock and Vertex, whose
// body names none, the request path's. Any other request, other vendors'
// models' and older Claudes' included, goes byte for byte as it came.
func thinkingOffAsTaken(body []byte, path string) []byte {
	t := gjson.GetBytes(body, "thinking.type").String()
	if t != "disabled" && t != "between_tools" {
		return body
	}
	model := gjson.GetBytes(body, "model").String()
	if model == "" {
		model = path
	}
	rule := thinkingOffOf(model)
	effort := outputConfigEffort(body) // a turn's own level counts too
	aboveHigh := effort == "xhigh" || effort == "max"
	var thinking map[string]any // nil: left out
	effortTo := ""
	switch rule {
	case offTaken:
		if t == "disabled" {
			return body
		}
		thinking = map[string]any{"type": "disabled"}
	case offUpToHigh:
		if t == "disabled" && !aboveHigh {
			return body
		}
		thinking = map[string]any{"type": "disabled"}
		if aboveHigh {
			effortTo = "high"
		}
	case offBetweenTools:
		if th := gjson.GetBytes(body, "thinking"); t == "between_tools" && len(th.Map()) == 1 && !aboveHigh {
			return body
		}
		// between_tools takes no other field (display, budget_tokens)
		thinking = map[string]any{"type": "between_tools"}
		if aboveHigh {
			effortTo = "high"
		}
	case offNever:
		if effort == "" {
			effortTo = "low"
		}
	default:
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var q map[string]any
	if dec.Decode(&q) != nil {
		return body
	}
	if thinking != nil {
		q["thinking"] = thinking
	} else {
		delete(q, "thinking")
		if n := gjson.GetBytes(body, "max_tokens").Int(); n > 0 && n < classifierRoom {
			q["max_tokens"] = n + classifierRoom
		}
	}
	if effortTo != "" {
		oc, _ := q["output_config"].(map[string]any)
		if oc == nil {
			oc = map[string]any{}
		}
		oc["effort"] = effortTo
		q["output_config"] = oc
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false) // <, > and & as the agent wrote them
	if enc.Encode(q) != nil {
		return body
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n"))
}

// asksBetweenTools is Sonnet 5.5's answer to thinking turned off, asking
// for between_tools: "To turn thinking off on this model, send "thinking":
// {"type": "between_tools"} instead of {"type": "disabled"}" as relays
// pass it on (#1454), or the API's "Use "thinking.type.between_tools" for
// the lowest thinking setting".
var asksBetweenTools = lazyre.New(`(?i)between_tools\W+instead of|use\W+thinking\.type\.between_tools`)

// thinkingOffRefused is a Claude refusing thinking turned off at any
// effort, as Opus 5.5 does: ""thinking.type.disabled" is not supported for
// this model. Use "thinking.type.adaptive"…".
var thinkingOffRefused = lazyre.New(`(?i)thinking\.type\.disabled\W+is not supported`)

// withBetweenTools is body with its thinking turned off as Sonnet 5.5
// asks it, at effort high at most; false when body doesn't turn it off.
func withBetweenTools(body []byte) ([]byte, bool) {
	if gjson.GetBytes(body, "thinking.type").String() != "disabled" {
		return nil, false
	}
	fields := map[string]any{"thinking": map[string]any{"type": "between_tools"}}
	if e := outputConfigEffort(body); e == "xhigh" || e == "max" {
		oc, _ := gjson.GetBytes(body, "output_config").Value().(map[string]any)
		if oc == nil {
			oc = map[string]any{}
		}
		oc["effort"] = "high"
		fields["output_config"] = oc
	}
	return withFields(body, fields), true
}

// samplingRefused is a Claude that answers temperature, top_p or top_k
// with a 400, by Anthropic's rules: Opus 4.7, 4.8, 5 and 5.5, Fable 5 and
// Mythos 5 refuse them at any value; Sonnet 5 and 5.5 refuse a value but
// their default; Haiku 5.5 takes only temperature 1 and top_p 0.99, never
// both, and no top_k ("`temperature` is deprecated for this model", #1454).
// Left out, each model samples at its default. Older Claudes, Haiku 4.5
// and Opus 4.6 among them, take all three; a later line than these is
// left as sent.
func samplingRefused(model string) bool {
	m := claudeLine.FindStringSubmatch(strings.ToLower(model))
	if m == nil {
		return false
	}
	major, _ := strconv.Atoi(m[2])
	minor, _ := strconv.Atoi(m[3])
	switch m[1] {
	case "fable", "mythos":
		return major == 5
	case "opus":
		return major == 4 && minor >= 7 || major == 5 && (minor == 0 || minor == 5)
	case "sonnet":
		return major == 5 && (minor == 0 || minor == 5)
	case "haiku":
		return major == 5 && minor == 5
	}
	return false
}

// samplingAsTaken is an Anthropic request without temperature, top_p and
// top_k when the model it is sent to refuses them (samplingRefused):
// Claude Code's auto mode classifier, and magpie's own classifiers, ask at
// temperature 0. model is the vendor's name, from the body or, for
// Bedrock and Vertex, the path. Any other request goes byte for byte as it
// came.
func samplingAsTaken(body []byte, path string) []byte {
	if !hasSampling(body) {
		return body
	}
	model := gjson.GetBytes(body, "model").String()
	if model == "" {
		model = path
	}
	if !samplingRefused(model) {
		return body
	}
	return withoutFields(body, "temperature", "top_p", "top_k")
}

func hasSampling(body []byte) bool {
	r := gjson.GetManyBytes(body, "temperature", "top_p", "top_k")
	return r[0].Exists() || r[1].Exists() || r[2].Exists()
}

// samplingDeprecated is a Claude refusing a sampling parameter, as Haiku
// 5.5 does: "`temperature` is deprecated for this model." (and `top_p`,
// `top_k`).
var samplingDeprecated = lazyre.New("(?i)\\b(?:temperature|top_p|top_k)`?\\W+is (?:deprecated|not supported) for this model")

// withoutSampling is body without temperature, top_p and top_k; false
// when it has none of them.
func withoutSampling(body []byte) ([]byte, bool) {
	if !hasSampling(body) {
		return nil, false
	}
	return withoutFields(body, "temperature", "top_p", "top_k"), true
}

// AdaptiveThinking is adaptiveOnly for agents told how to ask a model: a
// Claude that takes thinking.type=adaptive and an effort, never a budget.
func AdaptiveThinking(model string) bool { return adaptiveOnly(model) }

// effortInOutputConfig is a model that takes how hard it thinks from
// output_config.effort, beside thinking turned on, as ZCode asks it: Z.ai's
// GLM-5.2 and GLM-5.3 on their Anthropic endpoints. A budget alone leaves
// them at their own default. MiniMax's MiniMax-M3.1-Flash and its
// -Preview, which think at low…max (MiniMax Code's catalog: forced_on),
// are asked the same way as MiniMax Code asks them: a budget alone gave
// medium to max one budget under an 8192 cap, so the level was lost
// (shin4 on plugins#316).
//
// GPT models are asked so too, on whatever Messages endpoint serves them (a
// relay the user set up): a relay that maps Messages onto OpenAI reads the
// level from output_config.effort, and a budget alone left every request at
// its default, medium (zxz8009195489 on Discord, 2026-11-04).
var effortInOutputConfig = lazyre.New(`(?i)glm-5\.[23](?:$|[-.:/\[])|minimax-m3\.1-flash(?:$|[-.:/\[])|(?:^|[^a-z0-9])gpt-\d`)

// deepseekHost is DeepSeek's own API, whose Anthropic endpoint
// (api.deepseek.com/anthropic) takes the effort in output_config.
const deepseekHost = provider.DeepSeekHost

// isDeepSeekAnthropic is whether the request goes to DeepSeek's own
// Anthropic endpoint, whose shape is checked against the host, not the
// preset: a "deepseek" provider pointed at another host is that host's
// shape, and it learns from that endpoint's refusals as any other does.
func isDeepSeekAnthropic(p provider.Provider, to provider.Protocol) bool {
	return provider.HostOf(p.Base(to)) == deepseekHost
}

// deepseekEfforts are the levels DeepSeek's models think at: its thinking
// guide maps minimal to low, medium and xhigh to high, and ultra to max.
// A budget a client sent is fitted to them when the model's own list is
// unknown, so 32000 (max, as budgetOf gives it) asks max, not xhigh,
// which DeepSeek would read as high.
var deepseekEfforts = []string{"low", "high", "max"}

// outputEffortAt is effortInOutputConfig for a model at host: those
// models anywhere, and every model on DeepSeek's own endpoint. DeepSeek's
// Anthropic API docs (api-docs.deepseek.com/guides/anthropic_api, read
// 2026-10-23) list thinking as "Supported (budget_tokens is ignored)" and
// output_config as "Only effort is supported", and its thinking guide
// (guides/thinking_mode) names output_config.effort as the Anthropic
// format's effort control: a budget alone left DSH's low and max at
// DeepSeek's default, high (#3298). Another host serving a DeepSeek model
// is not covered by those docs and goes as it did.
func outputEffortAt(host, model string) bool {
	return host == deepseekHost || effortInOutputConfig.MatchString(model)
}

// AdaptiveWithEffort is, for agents told how to ask a model, one of
// effortInOutputConfig's that takes thinking adaptive with its level in
// output_config.effort: Z.ai's GLM-5.2 and GLM-5.3 (tried live on GLM-5.3,
// 10-24: max thought about three times high's). An agent whose budget
// can't say max (pi-ai clamps it to high's) asks them so. MiniMax's
// M3.1-Flash isn't, until it is tried (#3496).
func AdaptiveWithEffort(model string) bool { return glmOutputEffort.MatchString(model) }

// glmOutputEffort is effortInOutputConfig's Z.ai half.
var glmOutputEffort = lazyre.New(`(?i)glm-5\.[23](?:$|[-.:/\[])`)

// withOutputEffort is body asking, in output_config.effort, for the level
// of levels nearest the reasoning it asks for; body when it asks for none.
// Thinking asked for adaptively (DSH asks these models so, its Max not
// clamped to a high budget) stays adaptive: Z.ai takes it with the level.
func withOutputEffort(body []byte, levels []string) []byte {
	e := requestEffort(provider.Anthropic, body)
	var v struct {
		OutputConfig map[string]any `json:"output_config"`
	}
	if e == "" || decodeJSON(body, &v) != nil {
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

// aMessage is one message of a request built for a Messages upstream.
type aMessage struct {
	Role    string   `json:"role"`
	Content []aBlock `json:"content"`
}

// interruptedTool is what an unanswered call gets, as pairToolMessages and
// pairToolItems make for the Chat and Responses paths.
const interruptedTool = "[The result of this tool call is unavailable: the turn was interrupted.]"

// buildAnthropic renders a request for an Anthropic-style upstream.
func buildAnthropic(r *Request, model string) []byte {
	return anthropicBody(r, model, "", false)
}

// anthropicBody is buildAnthropic; with replay, for an upstream that wants
// a turn's thinking back (anthropicReplaysThinking), thinking with no
// signature goes as its text instead of being left out. host is the
// upstream's (outputEffortAt).
func anthropicBody(r *Request, model, host string, replay bool) []byte {
	r = responseInstructions(r)
	if r.Format != nil && r.Format.schema() == nil {
		// output_config.format takes a schema only: any JSON object is
		// asked for in words
		r = r.inSystem()
	}
	var msgs []aMessage
	push := func(role string, blocks []aBlock) {
		if len(blocks) == 0 {
			return
		}
		if n := len(msgs); n > 0 && msgs[n-1].Role == role {
			msgs[n-1].Content = append(msgs[n-1].Content, blocks...)
			return
		}
		msgs = append(msgs, aMessage{Role: role, Content: blocks})
	}
	for _, m := range r.Messages {
		var results, rest []aBlock
		for _, p := range m.Parts {
			switch p.Kind {
			case Text:
				if strings.TrimSpace(p.Text) != "" {
					rest = append(rest, aBlock{Type: "text", Text: p.Text, CacheControl: partMark(p.cache)})
				}
			case File:
				if isVideo(p) && !r.videoOff {
					b := videoBlock(p)
					b.CacheControl = partMark(p.cache)
					rest = append(rest, b)
					break
				}
				rest = append(rest, aBlock{Type: "text", Text: attachmentText(p)})
			case Image:
				b := imageBlock(p)
				b.CacheControl = partMark(p.cache)
				rest = append(rest, b)
			case ToolCall:
				// Anthropic takes a tool_use id only of [A-Za-z0-9_-]:
				// another vendor's call (Devin's "Bash:0#…") goes as
				// anthropicOutID makes it, its result named the same
				rest = append(rest, aBlock{Type: "tool_use", ID: anthropicOutID(p.ID), Name: p.Name, Input: argsOf(p), CacheControl: partMark(p.cache)})
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
				results = append(results, aBlock{Type: "tool_result", ToolUseID: anthropicOutID(p.CallID), Content: c, IsError: p.IsError, CacheControl: partMark(p.cache)})
			case Thinking:
				if p.sealedBy(sealAnthropic) {
					rest = append(rest, aBlock{Type: "redacted_thinking", sealed: p.Sealed})
				} else if p.Signature != "" {
					rest = append(rest, aBlock{Type: "thinking", Thinking: p.Text, Signature: p.Signature})
				} else if replay && p.Text != "" {
					rest = append(rest, aBlock{Type: "thinking", Thinking: p.Text})
				}
			}
		}
		role := m.Role
		if role != "assistant" {
			role = "user"
		}
		push(role, append(results, rest...))
	}
	// a conversation a client left with its exchange broken — a call whose
	// turn was interrupted, a result whose call it dropped — is mended, as
	// the Chat and Responses paths mend theirs, where Anthropic refuses it
	msgs = pairToolBlocks(msgs)
	// Anthropic caches a prompt only up to a block marked for it, which a
	// request from another API (Codex's, a Chat client's) never has: the
	// conversation so far is marked at its last block, so the next turn,
	// which only adds to it, reads it from the cache; thinking can't be
	// marked. Two marks at most, with the one Claude's sign-in adds kept
	// within Anthropic's four. A request that brought cache_control of its
	// own (parseTranslated, #4571) is rebuilt with those instead: the
	// blocks the client marked stand marked again (the parts loop), the
	// tail and the system take the client's ttl, and none of magpie's own
	// are added — a mark the client put on thinking, which Anthropic does
	// not mark, or on a block the rebuild drops is left out. A request
	// that marks nothing (one that only mentions the field in its text)
	// is magpie's to mark, as before.
	mark := addedMark(r, model)
	var tail, systemMark map[string]string
	if c := r.clientCache; c == nil || c.count == 0 {
		tail, systemMark = mark, mark
	} else {
		if c.tail != "" {
			tail = cacheMark(c.tail)
		}
		if n := len(c.system); n > 0 {
			// the system's blocks are one text block in the rebuild: a
			// fixed system needs only its end marked, the last mark's ttl
			systemMark = cacheMark(c.system[n-1].TTL)
		}
	}
	if n := len(msgs); n > 0 && tail != nil {
		c := msgs[n-1].Content
		for i := len(c) - 1; i >= 0; i-- {
			if c[i].Type != "thinking" && c[i].Type != "redacted_thinking" {
				c[i].CacheControl = tail
				break
			}
		}
	}
	// an assistant turn made only of unsigned thinking is nothing to Anthropic
	out := map[string]any{"model": model, "messages": msgs, "stream": r.Stream}
	if r.System != "" {
		// the tools and system prompt, the same every turn, are cached
		// apart from the conversation
		out["system"] = []aBlock{{Type: "text", Text: r.System, CacheControl: systemMark}}
	}
	maxTokens := r.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens // the model's own is filled in before (askedOutput)
	}
	// a client that turned reasoning off — Codex's or a Chat client's
	// effort none, which Effort reads as low — is sent thinking off, not a
	// low budget: it was thinking at 4096 tokens while the log said none
	// (#1636). A model that won't stop thinking is asked again as before
	// (forwardTranslated); thinkingOffAsTaken fits off to each Claude
	think := (r.Thinking || r.Effort != "") && !r.ThinkOff
	if r.ThinkOff {
		out["thinking"] = map[string]any{"type": "disabled"}
	}
	if think && adaptiveOnly(model) {
		// Opus 4.7 and later leave the thinking text out unless asked
		// (display "omitted" by default), and a Chat or Responses client
		// that asked to reason would see none of it; OpenAI is asked for
		// its summary and the Claude subscription for summarized alike
		out["thinking"] = map[string]any{"type": "adaptive", "display": "summarized"}
		if e := r.Effort; e != "" {
			if e == "xhigh" && noXhigh(model) {
				e = "max"
			}
			out["output_config"] = map[string]any{"effort": e}
		}
	} else if budget := budgetOf(r.Effort); think && (r.MaxTokens <= 0 || r.MaxTokens > 2048) {
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
		if r.Effort != "" && outputEffortAt(host, model) {
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
	if len(r.Safeguards) > 0 && anthropicModel.MatchString(model) {
		// auto mode's review, which Claude alone does (automode.go)
		out["safeguards"] = r.Safeguards
	}
	// a history of calls with no tools to go with it, which Anthropic
	// turns away ("must define tools", undeclared_tools.go): each tool
	// it called is declared, and none of them may be called
	rTools := r.Tools
	stubbed := false
	if len(rTools) == 0 && !r.WebSearch {
		for _, n := range historyCallNames(r) {
			rTools = append(rTools, Tool{Name: n, Description: stubToolDescription})
			stubbed = true
		}
	}
	if len(rTools) > 0 || r.WebSearch {
		var tools []map[string]any
		for _, t := range rTools {
			schema := t.Schema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			} else {
				schema = objectSchema(schema)
			}
			tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "input_schema": schema})
		}
		if r.System == "" && len(tools) > 0 && systemMark != nil {
			tools[len(tools)-1]["cache_control"] = systemMark
		}
		if r.WebSearch {
			tools = append(tools, map[string]any{"type": "web_search_20250305", "name": "web_search", "max_uses": 5})
		}
		out["tools"] = tools
		var tc map[string]any
		switch {
		case stubbed || r.ToolChoice == "none":
			tc = map[string]any{"type": "none"}
		case r.ToolChoice == "auto" || r.ToolChoice == "":
			if r.Parallel != nil && !*r.Parallel {
				tc = map[string]any{"type": "auto"}
			}
		case r.ToolChoice == "required":
			tc = map[string]any{"type": "any"}
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
	b, _ := marshalBody(out, "messages")
	return b
}

// pairToolBlocks mends the tool exchange of a request built for a Messages
// upstream, as pairToolMessages does for a Chat one (#554) and
// pairToolItems for a relayed Responses request (#1341): Anthropic refuses
// a tool_use the next message's tool_results don't answer ("`tool_use` ids
// were found without `tool_result` blocks immediately after") and a
// tool_result naming a call it never saw ("unexpected `tool_use_id` found
// in `tool_result` blocks") — an interrupted turn or a client that dropped
// its calls leaves both behind, and a relay serving Claude over Messages
// passes the refusal on. A call left unanswered gets a synthetic result in
// the user message that follows, or one of its own where none does; an
// answer to no call becomes a text block, keeping what it said. A
// well-formed exchange is built exactly as it was.
func pairToolBlocks(msgs []aMessage) []aMessage {
	out := make([]aMessage, 0, len(msgs))
	var pending []string          // the calls of the assistant message in hand
	open := map[string]bool{}     // which of them stand
	answered := map[string]bool{} // pending calls a result answered

	// synthetic is a call's missing result
	synthetic := func(id string) aBlock {
		c, _ := json.Marshal(interruptedTool)
		return aBlock{Type: "tool_result", ToolUseID: id, Content: c, IsError: true}
	}
	// closeExchange answers the calls still open and ends the exchange
	closeExchange := func() {
		var blocks []aBlock
		for _, id := range pending {
			if !answered[id] {
				blocks = append(blocks, synthetic(id))
			}
		}
		if len(blocks) > 0 {
			out = append(out, aMessage{Role: "user", Content: blocks})
		}
		pending, open, answered = nil, map[string]bool{}, map[string]bool{}
	}
	for _, m := range msgs {
		if m.Role == "assistant" {
			closeExchange()
			for _, b := range m.Content {
				if b.Type == "tool_use" {
					pending = append(pending, b.ID)
					open[b.ID] = true
				}
			}
			out = append(out, m)
			continue
		}
		var kept, rest []aBlock
		for _, b := range m.Content {
			if b.Type != "tool_result" {
				rest = append(rest, b)
				continue
			}
			if !open[b.ToolUseID] {
				// answers no call of the exchange in hand — one another
				// request carried, that closed already, or never there
				rest = append(rest, resultSaid(b)...)
				continue
			}
			if answered[b.ToolUseID] {
				continue // a second answer: the first stands
			}
			answered[b.ToolUseID] = true
			kept = append(kept, b)
		}
		for _, id := range pending {
			if !answered[id] {
				kept = append(kept, synthetic(id))
			}
		}
		out = append(out, aMessage{Role: "user", Content: append(kept, rest...)})
		pending, open, answered = nil, map[string]bool{}, map[string]bool{}
	}
	closeExchange()
	return out
}

// resultSaid is what a tool_result that answers no call keeps: its text as
// a text block, its images beside it as they came.
func resultSaid(b aBlock) []aBlock {
	var s string
	if len(b.Content) > 0 && json.Unmarshal(b.Content, &s) == nil {
		if strings.TrimSpace(s) == "" {
			s = "Tool result received."
		}
		return []aBlock{{Type: "text", Text: s}}
	}
	var blocks []aBlock
	if len(b.Content) > 0 && json.Unmarshal(b.Content, &blocks) == nil && len(blocks) > 0 {
		return blocks
	}
	return []aBlock{{Type: "text", Text: "Tool result received."}}
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
	if decodeJSON([]byte(data), &ev) == nil {
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
	return decodeJSON(in, &m) == nil && len(m) > 0
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
			// thinking
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
		} `json:"content_block"`
		Delta struct {
			Type            string `json:"type"`
			Text            string `json:"text"`
			PartialJSON     string `json:"partial_json"`
			Thinking        string `json:"thinking"`
			EstimatedTokens *int   `json:"estimated_tokens"`
			Signature       string `json:"signature"`
			StopReason      string `json:"stop_reason"`
			// auto mode's review of the reply's calls (automode.go)
			SafeguardResults json.RawMessage `json:"safeguard_results"`
			StopDetails      json.RawMessage `json:"stop_details"`
		} `json:"delta"`
		Usage aUsage `json:"usage"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := decodeJSON([]byte(data), &ev); err != nil {
		// keep going: pings and unknown shapes are harmless, but an
		// error in a shape of its own is still the reply's end (#1933)
		if e, ok := carriedError(data); ok {
			emit(e)
		}
		return nil
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
		case "thinking":
			// a block of its own, even signed with no text in it, or
			// right after another (#1445)
			emit(Event{Kind: KThinkStart})
			if ev.ContentBlock.Thinking != "" {
				emit(Event{Kind: KThink, Text: ev.ContentBlock.Thinking})
			}
			if ev.ContentBlock.Signature != "" {
				emit(Event{Kind: KSig, Name: sealAnthropic, Text: ev.ContentBlock.Signature})
			}
		case "redacted_thinking":
			// sealed whole: it goes on as it came, to Messages only
			var whole struct {
				Block json.RawMessage `json:"content_block"`
			}
			if decodeJSON([]byte(data), &whole) == nil && len(whole.Block) > 0 {
				emit(Event{Kind: KSealed, Name: sealAnthropic, Text: string(whole.Block)})
			}
		}
	case "content_block_delta":
		switch ev.Delta.Type {
		case "text_delta":
			emit(Event{Kind: KText, Text: ev.Delta.Text})
		case "input_json_delta":
			emit(Event{Kind: KToolArgs, Text: ev.Delta.PartialJSON})
		case "thinking_delta":
			emit(Event{Kind: KThink, Text: ev.Delta.Thinking, Estimated: ev.Delta.EstimatedTokens})
		case "signature_delta":
			emit(Event{Kind: KSig, Name: sealAnthropic, Text: ev.Delta.Signature})
		}
	case "message_delta":
		if ev.Delta.StopReason != "" {
			emit(Event{Kind: KStop, Stop: stopFromAnthropic(ev.Delta.StopReason)})
		}
		var review json.RawMessage
		if r := ev.Delta.SafeguardResults; len(r) > 0 && string(r) != "null" {
			review = r
		}
		emit(Event{Kind: KUsage, Usage: ev.Usage.usage(), SafeguardResults: review, StopDetails: stopDetails(ev.Delta.StopDetails)})
	case "error":
		code, status, said := filterRefused(data)
		emit(Event{Kind: KError, Text: cmp.Or(ev.Error.Message, ev.Error.Type, streamErrorText(data)), Code: code, Status: status, Said: said})
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
	// CacheUnknown: a plugin's usage saying its vendor gave no cache
	// counts (magpie_cache_unknown), so the zero above is not a miss:
	// Kiro caches but tells only credits, never tokens (zeewell on X).
	// It is read, never told: anthropic() leaves it false.
	CacheUnknown bool `json:"magpie_cache_unknown,omitempty"`
	// OutputTokensDetails says how many of the output tokens were
	// thinking, as Copilot counts them on a message_delta: the reasoning
	// count the other protocols' usage carries (completion_tokens_details,
	// Gemini's thoughtsTokenCount) and the Claude subscription's cliUsage
	// reads from Claude Code. A vendor that leaves it out is told by its
	// stream (hidesReasoning, #4545)
	OutputTokensDetails *aOutputDetails `json:"output_tokens_details,omitempty"`
}

type aCacheCreation struct {
	Ephemeral5m int `json:"ephemeral_5m_input_tokens"`
	Ephemeral1h int `json:"ephemeral_1h_input_tokens"`
}

type aOutputDetails struct {
	ThinkingTokens int `json:"thinking_tokens"`
}

func (u aUsage) usage() Usage {
	out := Usage{Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheReadInputTokens, CacheWrite: u.CacheCreationInputTokens, cacheUnknown: u.CacheUnknown}
	if c := u.CacheCreation; c != nil {
		out.cacheCreationReported = true
		out.cacheCreationHasCounts = c.Ephemeral5m+c.Ephemeral1h > 0
		out.CacheWrite = max(out.CacheWrite, c.Ephemeral5m+c.Ephemeral1h)
		out.CacheWrite1h = c.Ephemeral1h
	}
	if d := u.OutputTokensDetails; d != nil {
		out.Reasoning = d.ThinkingTokens
	}
	return out
}

// anthropic is the usage as Anthropic says it, the cache writes split by
// how long they are kept when upstream reported TTLs or some were for an
// hour, and the thinking among the output tokens when the upstream counted
// its reasoning.
func (u Usage) anthropic() aUsage {
	out := aUsage{InputTokens: u.Input, OutputTokens: u.Output, CacheReadInputTokens: u.CacheRead, CacheCreationInputTokens: u.CacheWrite}
	if h := min(u.CacheWrite1h, u.CacheWrite); u.cacheCreationReported || h > 0 {
		out.CacheCreation = &aCacheCreation{Ephemeral5m: u.CacheWrite - h, Ephemeral1h: h}
	}
	if u.Reasoning > 0 {
		out.OutputTokensDetails = &aOutputDetails{ThinkingTokens: u.Reasoning}
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
	id    string
	w     *sseWriter
	model string
	index int
	open  Kind // kind of the open content block, "" when none
	args  bool // the open tool block got arguments
	// fresh: a thinking block began upstream (KThinkStart), to be opened
	// apart from the one open with what it says first
	fresh   bool
	started bool
	pinged  time.Time // the last ping keepalive sent
	col     collector
	// cite cuts the text said after a search into lines, each naming a
	// page found its own block with citations (search_cite.go); cited:
	// the open text block is such a line's
	cite  citer
	cited bool
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

func anthropicInID(id string) string {
	// an id a relayed reply's was salted to (saltCallID, #4910) goes to a
	// translated upstream as the client holds it: unique, and safe as it
	// is, where decoding it would hand the upstream the salt's NUL
	if _, salted := unsaltCallID(id); salted {
		return id
	}
	return rawCallID(anthropicIDMark, id)
}

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

var safeIDChars = lazyre.New(`^[A-Za-z0-9_-]+$`)

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
	e.args, e.fresh = false, false
	block["type"] = map[Kind]string{Text: "text", Thinking: "thinking", ToolCall: "tool_use"}[k]
	e.w.event("content_block_start", map[string]any{"type": "content_block_start", "index": e.index, "content_block": block})
	e.open = k
}

// thinking makes the open block a thinking block: the one open, unless a
// new one has begun since (KThinkStart).
func (e *anthropicEncoder) thinking() {
	if e.fresh {
		e.close()
		e.fresh = false
	}
	// signed empty where it starts, as Anthropic's own stream starts one:
	// its signature_delta, if any, comes before the block's stop, and a
	// client that reads the field as required (Grok Build) turns a block
	// without it away (thinking_signature.go)
	e.openBlock(Thinking, map[string]any{"thinking": "", "signature": ""})
}

func (e *anthropicEncoder) delta(d map[string]any) {
	e.w.event("content_block_delta", map[string]any{"type": "content_block_delta", "index": e.index, "delta": d})
}

// sayCited says a line of text after a search (citer): a line that cites
// a page is a text block of its own, its citations first, as Anthropic's
// stream gives them (citations_delta); one that cites none goes on in the
// open text block unless that block cites; the whitespace left at the end
// (tail) goes on whatever text block is open.
func (e *anthropicEncoder) sayCited(text string, cites []map[string]any, tail bool) {
	switch {
	case len(cites) > 0:
		e.close()
		e.openBlock(Text, map[string]any{"text": ""})
		e.cited = true
		for _, c := range cites {
			e.delta(map[string]any{"type": "citations_delta", "citation": c})
		}
	case tail && e.open == Text:
	default:
		if e.cited {
			e.close()
		}
		e.openBlock(Text, map[string]any{"text": ""})
		e.cited = false
	}
	e.delta(map[string]any{"type": "text_delta", "text": text})
}

func (e *anthropicEncoder) event(ev Event) {
	if ev.Kind != KStart && !e.started {
		e.start(Event{})
	}
	switch ev.Kind {
	case KText, KUsage, KStart, KTextSig, KStop, KAlive:
	default:
		// anything else ends the text: its last line is said
		e.cite.flush(e.sayCited)
	}
	switch ev.Kind {
	case KStart:
		e.start(ev)
	case KText:
		if ev.Text == "" {
			return
		}
		if e.cite.on() {
			e.cite.feed(ev.Text, e.sayCited)
			break
		}
		e.openBlock(Text, map[string]any{"text": ""})
		e.delta(map[string]any{"type": "text_delta", "text": ev.Text})
	case KThinkStart:
		// opened with what it says first: one that says nothing isn't
		e.fresh = true
		e.col.add(ev)
		return
	case KThink:
		if ev.Text == "" {
			if ev.Estimated == nil {
				return
			}
			// an empty thinking delta that carries the vendor's progress
			// count goes to the client as it came: the live beat that says
			// the think is moving, not silence (#5390)
			e.thinking()
			e.delta(map[string]any{"type": "thinking_delta", "thinking": "", "estimated_tokens": *ev.Estimated})
			return
		}
		e.thinking()
		d := map[string]any{"type": "thinking_delta", "thinking": ev.Text}
		if ev.Estimated != nil {
			d["estimated_tokens"] = *ev.Estimated
		}
		e.delta(d)
	case KSig:
		// a block signed with no text goes as it came, signed (#1445)
		if e.open == Thinking || e.fresh {
			e.thinking()
			e.delta(map[string]any{"type": "signature_delta", "signature": ev.Text})
		}
	case KSealed:
		if ev.Name == sealAnthropic {
			// a redacted_thinking block, whole as Anthropic wrote it
			e.close()
			e.fresh = false
			e.w.event("content_block_start", map[string]any{"type": "content_block_start", "index": e.index, "content_block": json.RawMessage(ev.Text)})
			e.w.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": e.index})
			e.index++
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
		e.cite.hits = append(e.cite.hits, ev.Hits...)
		e.w.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": e.index})
		e.index++
	case KError:
		e.close()
		typ := "api_error"
		if ev.Status == http.StatusRequestEntityTooLarge && ev.Code != "context_length_exceeded" {
			// the same type a 413 JSON body carries (writeErrorSaid)
			typ = "request_too_large"
		}
		failed := map[string]any{"type": typ, "message": ev.Text}
		if ev.Code != "" {
			failed["code"] = ev.Code // preserve the upstream error type, including refusals
		}
		saidError(failed, ev.Said)
		e.w.event("error", map[string]any{"type": "error", "error": failed})
	}
	e.col.add(ev)
}

// keepalive is Anthropic's own: a ping event, which its SDKs skip and
// Claude Code's stream watchdog counts, at most one each pingEvery
// (Claude Code counts 30 in a row); a comment in between. Before
// message_start it is a comment too: the SDKs' MessageStream fails on
// any event before it ("Unexpected event order, got ping before
// message_start"), and Anthropic sends none.
func (e *anthropicEncoder) keepalive() {
	if !e.started || !e.pinged.IsZero() && time.Since(e.pinged) < pingEvery {
		e.w.comment("keepalive")
		return
	}
	e.pinged = time.Now()
	e.w.event("ping", map[string]any{"type": "ping"})
}

func (e *anthropicEncoder) finish() {
	if !e.started {
		e.start(Event{})
	}
	e.cite.flush(e.sayCited)
	e.close()
	res := e.col.finish()
	delta := map[string]any{"stop_reason": stopToAnthropic(res.Stop), "stop_sequence": nil}
	if len(res.SafeguardResults) > 0 {
		delta["safeguard_results"] = res.SafeguardResults
	}
	if len(res.StopDetails) > 0 {
		delta["stop_details"] = res.StopDetails
	}
	e.w.event("message_delta", map[string]any{"type": "message_delta",
		"delta": delta,
		"usage": res.Usage.anthropic()})
	e.w.event("message_stop", map[string]any{"type": "message_stop"})
}

// renderAnthropic is the non-streaming reply.
func renderAnthropic(res Result, model string) []byte {
	content := []any{}
	var cite citer
	for _, p := range res.Parts {
		switch p.Kind {
		case Text:
			if p.Text == "" && p.Signature != "" {
				continue // only Gemini's signature, which is Gemini's
			}
			if cite.on() {
				content = append(content, citedBlocks(&cite, p.Text)...)
				continue
			}
			content = append(content, map[string]any{"type": "text", "text": p.Text})
		case Thinking:
			switch {
			case p.sealedBy(sealAnthropic):
				content = append(content, p.Sealed)
			case p.Text == "" && p.Signature == "":
				// another API's sealed reasoning, nothing to Messages
			default:
				content = append(content, map[string]any{"type": "thinking", "thinking": p.Text, "signature": p.Signature})
			}
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
			cite.hits = append(cite.hits, p.Hits...)
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
	if len(res.StopDetails) > 0 {
		reply["stop_details"] = res.StopDetails
	}
	b, _ := json.Marshal(reply)
	return b
}

// citedBlocks are the text blocks of text said after a search, as the
// stream's sayCited makes them.
func citedBlocks(c *citer, text string) []any {
	var blocks []any
	var open map[string]any // the last text block, while it may go on
	cited := false
	say := func(t string, cites []map[string]any, tail bool) {
		switch {
		case len(cites) > 0:
			open = map[string]any{"type": "text", "text": t, "citations": cites}
			blocks = append(blocks, open)
			cited = true
			return
		case tail && open != nil, open != nil && !cited:
			open["text"] = open["text"].(string) + t
			return
		}
		open = map[string]any{"type": "text", "text": t}
		blocks = append(blocks, open)
		cited = false
	}
	c.feed(text, say)
	c.flush(say)
	return blocks
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
	if decodeJSON(schema, &m) != nil || !provider.ObjectRoot(m) {
		return schema
	}
	b, err := json.Marshal(m)
	if err != nil {
		return schema
	}
	return b
}

// stopDetails is a reply's stop_details, or nil when it had none.
func stopDetails(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return raw
}
