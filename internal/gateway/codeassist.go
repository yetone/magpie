package gateway

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// ---- Google Code Assist (provider side) --------------------------------------
//
// Gemini CLI and Antigravity sign in with Google and talk to Code Assist
// (cloudcode-pa.googleapis.com), which takes a Gemini request wrapped in an
// envelope — {model, project, request} — and streams replies wrapped the
// same way, {response}. The gateway builds {model, request}; the account
// adds the project and the ids its app sends when it signs the request.

// skipSignature stands in for a thought signature on a function call the
// model didn't make here: Google checks the ones Gemini 3 hands out, and
// this one tells it not to.
const skipSignature = "skip_thought_signature_validator"

var unsafeToolID = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// antigravityBaseOf is the model an id of Antigravity's own is one level
// of; a var so tests can stand in for it.
var antigravityBaseOf = provider.AntigravityBase

// accountAgent is the agent behind p's account, and "" where p is one of the
// user's own and has none: the question a rule keyed by the account has to be
// able to answer for, and one a provider's id cannot answer — a relay added
// under a built-in's id is that relay and not that account.
func accountAgent(p provider.Provider) string {
	if p.Account == nil {
		return ""
	}
	return p.Account.Agent
}

// antigravityTurnsAway: a system prompt Antigravity answers with a 429
// "Resource has been exhausted (e.g. check quota)" whatever the quota left:
// one that carries a line of agTurnedAway anywhere in the system
// instruction, in any case, is turned away on every model, while the same
// words in a message go through (#666). magpie breaks those lines up before
// they go (antigravityCloak); this still tells the refusal for what it is
// when the user has the prompt sent as written, or Google turns away one
// that was broken up, so no one chases a quota that isn't spent.
func antigravityTurnsAway(system string) bool {
	low := strings.ToLower(system)
	for _, line := range agTurnedAway {
		if strings.Contains(low, line) {
			return true
		}
	}
	return false
}

// agTurnedAway are the lines, lower-cased, that Antigravity turns a system
// instruction away for, each tried on a real account:
//   - Claude Code's billing line and the Claude Agent SDK's identity line
//     (Claude Desktop's chats) (#666);
//   - the identity lines of Codex's own prompts for its GPT-5 models
//     (gpt-5.6-sol's "You are Codex, an agent based on GPT-5. You and the
//     user share one workspace…"). Codex sends its model's prompt as the
//     conversation's first developer message, and a thread begun on a GPT
//     model keeps it when it is switched to another (/model, resume -m) or
//     forked as an agent's thread, so such a thread on an Antigravity model
//     never got an answer (Jodiezzzzzz on X, 10-27). The prompt magpie
//     handed other models until 11-01 ("You are Codex, based on GPT-5."),
//     "an AI agent based on GPT-5" and GPT-6's prompt go through. Codex's
//     prompt for a model it doesn't know, which other models carry since
//     ("You are a coding agent running in the Codex CLI"), has not been
//     tried on a real account;
//   - Hermes Agent's earlier default persona (its SOUL.md until Hermes
//     replaced it: "You are Hermes Agent, an intelligent AI assistant
//     created by Nous Research. …"), which a SOUL.md the user edited keeps
//     (zgoodz on X, 10-27). The line without "Research", "AI" or "Hermes"
//     goes through, and so does today's "You are Hermes Agent, built by
//     Nous Research."
//
// Each is broken up before it goes by a zero-width space after its first
// letter (antigravityCloak), which Google lets through: each line so
// broken ("Y", U+200B, "ou are Codex, an agent based on GPT-5"; "b",
// U+200B, "uilt on Anthropic's Claude Agent SDK"; and so on) was answered
// on a real account where the line as written was turned away. A mention
// of the Claude Agent SDK elsewhere ("running within the Claude Agent
// SDK") goes through as written, and is sent so.
var agTurnedAway = []string{
	"x-anthropic-billing-header:",
	"built on anthropic's claude agent sdk",
	"you are codex, an agent based on gpt-5",
	"you are codex, a coding agent based on gpt-5",
	"hermes agent, an intelligent ai assistant created by nous research",
}

// antigravityTurnsCodexAway says whether system carries Codex's GPT-5
// identity line that Antigravity turns away.
func antigravityTurnsCodexAway(system string) bool {
	low := strings.ToLower(system)
	return strings.Contains(low, "you are codex, an agent based on gpt-5") ||
		strings.Contains(low, "you are codex, a coding agent based on gpt-5")
}

// antigravityTurnsHermesAway says whether system carries Hermes Agent's
// earlier default persona that Antigravity turns away.
func antigravityTurnsHermesAway(system string) bool {
	return strings.Contains(strings.ToLower(system), "hermes agent, an intelligent ai assistant created by nous research")
}

// zeroWidth is what antigravityCloak puts inside a line Antigravity turns
// away: U+200B, which the model reads past and Google's check doesn't.
const zeroWidth = "\u200b"

// antigravityCloak is system with each line of agTurnedAway in it broken up
// by a zero-width space after its first letter, wherever and in whatever
// case it is, and every other word as the agent wrote it: what an
// Antigravity account's system instruction is sent as, unless the user has
// it sent as written (Provider.PromptAsWritten). A line already broken up
// (the antigravity-cloak plugin's) isn't one any more and is left alone.
func antigravityCloak(system string) string {
	if !antigravityTurnsAway(system) {
		return system
	}
	// lower-casing keeps every byte offset here: each line is ASCII, and
	// a match is found in low at the offset it has in system only while
	// the two are the same length, which ToLower can change outside ASCII
	low := strings.ToLower(system)
	if len(low) != len(system) {
		low = asciiLower(system)
	}
	var at []int
	for _, line := range agTurnedAway {
		for i := 0; ; {
			j := strings.Index(low[i:], line)
			if j < 0 {
				break
			}
			at = append(at, i+j+1)
			i += j + len(line)
		}
	}
	slices.Sort(at)
	at = slices.Compact(at)
	var b strings.Builder
	b.Grow(len(system) + len(at)*len(zeroWidth))
	prev := 0
	for _, i := range at {
		b.WriteString(system[prev:i])
		b.WriteString(zeroWidth)
		prev = i
	}
	b.WriteString(system[prev:])
	return b.String()
}

// asciiLower lower-cases only ASCII letters, keeping every byte where it is.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// antigravityRefuses is what Antigravity says when it turns such a request
// away, on either shape the refusal arrives in: a 429's body, or the reply's
// own error event, where the Code Assist decoder keeps the message and leaves
// the status and its code out of the event.
func antigravityRefuses(said string) bool {
	return strings.Contains(strings.ToLower(said), "resource has been exhausted")
}

// antigravityTurnedAway says whether what p answered a request with system
// as its system instruction is that refusal (#666): an Antigravity account,
// the system prompt it turns away, and its "Resource has been exhausted"
// words. A 429 that says something else — the plan's own allowance used up
// ("You have exhausted your capacity on this model. Your quota will reset
// after …") — is the quota it says, whatever the system prompt (#1425).
func antigravityTurnedAway(p provider.Provider, system, said string) bool {
	return accountAgent(p) == "antigravity" && antigravityTurnsAway(system) && antigravityRefuses(said)
}

// agForeseeing is how long after Antigravity last turned that system prompt
// away a group still skips it for one, without asking: long enough that a
// conversation doesn't wait on the same refusal turn after turn, short
// enough that a block Google lifts is noticed on the next request after it.
const agForeseeing = 6 * time.Hour

// antigravityForeseen says whether p is an Antigravity account that would
// turn body away: it carries the system prompt Antigravity refuses, and
// Antigravity has refused it within agForeseeing. It holds for exactly the
// requests antigravityTurnedAway would tell as the refusal, and it changes
// nothing about the account.
// What it gives is the hint for that system prompt, or "" when p would be
// asked.
func (s *Server) antigravityForeseen(p provider.Provider, from provider.Protocol, body []byte) string {
	if accountAgent(p) != "antigravity" {
		return ""
	}
	at := s.agTurnedAway.Load()
	if at == 0 || time.Since(time.Unix(0, at)) > agForeseeing {
		return ""
	}
	req, err := parse(from, body)
	if err != nil || !antigravityTurnsAway(req.System) {
		return ""
	}
	return antigravityHint(req.System)
}

// antigravityTurnedAwayHint is what magpie adds to Antigravity's words, after
// " — ", for the agent and the Routing page, which says it apart from them in
// its own language (routing/client.js AG_TURNED_AWAY).
const antigravityTurnedAwayHint = "Antigravity answers this 429 to the system prompt of Claude Code and the Claude Agent SDK (Claude Desktop's chats) whatever quota is left, so it is not a quota and waiting won't help; use another provider for these chats, or put one after Antigravity in a routing group"

// antigravityCodexHint is the hint for Codex's GPT-5 system prompt
// (routing/client.js AG_CODEX): the thread keeps the prompt it began with,
// and a new one on the Antigravity model is sent magpie's own.
const antigravityCodexHint = "Antigravity answers this 429 to Codex's own system prompt for GPT-5 models whatever quota is left, so it is not a quota and waiting won't help; a Codex chat begun on a GPT model keeps that prompt after a model switch or a fork, so start a new chat on this model"

// antigravityHermesHint is the hint for Hermes Agent's earlier default
// persona (routing/client.js AG_HERMES).
const antigravityHermesHint = "Antigravity answers this 429 to Hermes Agent's earlier default persona (its SOUL.md) whatever quota is left, so it is not a quota and waiting won't help; Hermes' current default persona goes through, so put it in SOUL.md, or use another provider for these chats"

// antigravityHint is the hint for what Antigravity turned away with system
// as its system instruction.
func antigravityHint(system string) string {
	if antigravityTurnsCodexAway(system) {
		return antigravityCodexHint
	}
	if antigravityTurnsHermesAway(system) {
		return antigravityHermesHint
	}
	return antigravityTurnedAwayHint
}

// turnedAwayStatus is what the agent is told when nobody is left to answer
// what Antigravity turned away: a request it shouldn't send again as it is.
// Antigravity's own 429 had the Anthropic and OpenAI SDKs — Claude Desktop,
// Claude Code — retry it ten times over, each one turned away the same
// (#1425); they retry 408, 409, 429 and 5xx, and not a 400.
const turnedAwayStatus = http.StatusBadRequest

// turnedAwayErrType is the usage log's ErrType for that refusal, in place of
// the 429 Antigravity's body calls it.
const turnedAwayErrType = "prompt_turned_away"

// codeAssistID is the id a request on the account's app goes out under: on
// Antigravity the variant the effort picks for a model that is a family of
// levels, and elsewhere the model magpie knows. It is keyed by the account
// (accountAgent) rather than by the provider's id, since that is the account
// the variants belong to, and it is the id a ledger judges what the vendor
// answered against (provider.AntigravitySentID), so the gateway and the
// ledger read one rule rather than one each.
// The envelope is built under it and named with it, so it is worked out
// once where both of those are and handed down.
func codeAssistID(model, effort, agent string) string {
	if agent == "antigravity" {
		return provider.AntigravitySentID(model, effort)
	}
	return model
}

// codeAssistBody is the envelope for a request on p's account, with the
// vendor's own name for the model in its model field, over the names in
// force (wires). It is named with the id the envelope carries, which on
// Antigravity is the variant the effort picked (codeAssistID): a name given
// for the whole provider then asks for each level by its own id instead of
// one id for every level, so the effort keeps choosing and the relay is
// asked for the model it serves at that level.
//
// The id is worked out once here and handed to the builder, since the name
// in force is looked up against the very id the envelope is addressed to:
// asking twice reads the vendor's catalogue twice over for one answer.
func codeAssistBody(p provider.Provider, r *Request, model string, wires map[string]string) []byte {
	// a model the user lists by an id of theirs (SetModelID) is the
	// account's own model: its envelope, the variant the effort picks and
	// a name for the whole provider are of the vendor's id
	model = p.OwnModelID(model)
	sent := codeAssistID(model, r.Effort, p.Account.Agent)
	if p.Account.Agent == "antigravity" && !p.PromptAsWritten {
		// the lines Google turns away, broken up; the request the
		// caller holds keeps the system prompt as written, which the
		// refusal is told against (antigravityTurnedAway)
		if cloaked := antigravityCloak(r.System); cloaked != r.System {
			rc := *r
			rc.System = cloaked
			r = &rc
		}
	}
	body := buildCodeAssistSent(r, sent, p.Account.Agent)
	if wire := provider.UpstreamNameAsIn(wires, p.ID, model, sent); wire != sent {
		body = rewriteModel(body, wire)
	}
	return body
}

// buildCodeAssist builds the Code Assist envelope for a request, for the
// app the account belongs to: "gemini" or "antigravity". On Antigravity a
// model that is a family of levels is asked for as the variant the effort
// picks (codeAssistID).
func buildCodeAssist(r *Request, model, agent string) []byte {
	return buildCodeAssistSent(r, codeAssistID(model, r.Effort, agent), agent)
}

// buildCodeAssistSent is buildCodeAssist with the id the envelope is
// addressed to already worked out, for the caller that names the envelope
// with that same id and would otherwise work it out a second time. There is
// no model but that id in here: on Antigravity the two differ, and the one
// the request goes out under is the one everything below is shaped from.
// agent "vertex" is Vertex AI's request (buildVertex).
func buildCodeAssistSent(r *Request, sent, agent string) []byte {
	r = responseInstructions(r)
	ag := agent == "antigravity"
	vx := agent == "vertex"
	at := "" // the level the id says it thinks at
	if ag {
		if _, l, ok := antigravityBaseOf(sent); ok {
			at = l
		}
	}
	claude := strings.Contains(strings.ToLower(sent), "claude")
	toolID := func(id string) string {
		if !ag || id == "" {
			return id
		}
		return unsafeToolID.ReplaceAllString(id, "_")
	}
	names := map[string]string{}
	var contents []map[string]any
	for _, m := range dropSecondAnswers(r.Messages) {
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		}
		var parts, after []map[string]any // after: tools' images held back
		toolImage := map[int]bool{}       // which of parts are a tool's images
		// signed: a step Gemini signed, whose later calls go without, as
		// Gemini signs the first (#1445, as chat's #687)
		signed := false
		for _, p := range m.Parts {
			if p.Kind == ToolCall && p.Signature != "" {
				signed = true
			}
		}
		for _, p := range m.Parts {
			switch p.Kind {
			case Text:
				if p.Text != "" || (p.Signature != "" && !claude) {
					part := map[string]any{"text": p.Text}
					if p.Signature != "" && !claude {
						// Gemini's signature on its text, back as it gave it
						part["thoughtSignature"] = p.Signature
					}
					parts = append(parts, part)
				}
			case Image, File:
				if p.Data != "" {
					parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": p.MediaType, "data": p.Data}})
				} else if p.URL != "" {
					parts = append(parts, map[string]any{"fileData": map[string]any{"mimeType": p.MediaType, "fileUri": p.URL}})
				}
			case ToolCall:
				names[p.ID] = p.Name
				call := map[string]any{"name": p.Name, "args": json.RawMessage(argsOf(p))}
				if id := toolID(p.ID); id != "" {
					call["id"] = id
				}
				part := map[string]any{"functionCall": call}
				switch {
				case claude:
					part["thoughtSignature"] = skipSignature
				case p.Signature != "":
					// Gemini's own signature, back as it gave it (#1445)
					part["thoughtSignature"] = p.Signature
				case !signed:
					part["thoughtSignature"] = skipSignature
				}
				parts = append(parts, part)
			case ToolResult:
				name := names[p.CallID]
				if name == "" {
					name = "tool"
				}
				key := "output"
				switch {
				case ag:
					key = "result"
				case p.IsError:
					key = "error"
				}
				res := map[string]any{"name": name, "response": map[string]any{key: p.Text}}
				if id := toolID(p.CallID); id != "" {
					res["id"] = id
				}
				parts = append(parts, map[string]any{"functionResponse": res})
				// the images a tool returned follow its response, as Gemini
				// CLI sends a file it read. Antigravity hands a Claude model
				// the turn back as Anthropic blocks, where an image between
				// two responses splits them from their calls ("tool_use ids
				// were found without tool_result blocks immediately after"):
				// there the images wait until every response is in, each
				// after a line saying whose it is.
				var ims []map[string]any
				for _, im := range p.Images {
					if im.Data != "" {
						ims = append(ims, map[string]any{"inlineData": map[string]any{"mimeType": im.MediaType, "data": im.Data}})
					} else if im.URL != "" {
						ims = append(ims, map[string]any{"fileData": map[string]any{"mimeType": im.MediaType, "fileUri": im.URL}})
					}
				}
				if ag && claude && len(ims) > 0 {
					label := fmt.Sprintf("Image returned by tool %s (call %s):", name, toolID(p.CallID))
					if len(ims) > 1 {
						label = fmt.Sprintf("%d images returned by tool %s (call %s):", len(ims), name, toolID(p.CallID))
					}
					after = append(after, map[string]any{"text": label})
					after = append(after, ims...)
				} else {
					for _, im := range ims {
						toolImage[len(parts)] = true
						parts = append(parts, im)
					}
				}
			}
			// thinking isn't sent back: its signatures belong to whoever
			// made them, and Google turns away ones it didn't
		}
		if len(after) > 0 {
			// the responses lead the turn, as an Anthropic tool_result
			// turn has to; what the user said and the tools' images follow
			var lead, rest []map[string]any
			for _, part := range parts {
				if part["functionResponse"] != nil {
					lead = append(lead, part)
				} else {
					rest = append(rest, part)
				}
			}
			parts = append(append(lead, after...), rest...)
		}
		// what the user said goes in a turn of its own after the tools'
		// responses: Gemini answers a turn mixing a functionResponse with
		// text "Requests ending with a model turn are not supported" (#1533,
		// yycsyu: Codex's compaction prompt after a tool's output), and takes
		// the two as separate turns. A Claude model on Antigravity keeps them
		// in one, the tool_result turn Anthropic reads them as.
		var said []map[string]any
		if role == "user" && !(ag && claude) && hasResponse(parts) {
			var tools []map[string]any
			for i, part := range parts {
				if part["functionResponse"] != nil || toolImage[i] {
					tools = append(tools, part)
				} else {
					said = append(said, part)
				}
			}
			parts = tools
		}
		for _, ps := range [][]map[string]any{parts, said} {
			if len(ps) == 0 {
				continue
			}
			if n := len(contents); n > 0 && contents[n-1]["role"] == role && (role != "user" || hasResponse(contents[n-1]["parts"].([]map[string]any)) == hasResponse(ps)) {
				contents[n-1]["parts"] = append(contents[n-1]["parts"].([]map[string]any), ps...)
				continue
			}
			contents = append(contents, map[string]any{"role": role, "parts": ps})
		}
	}
	contents = pairFunctionTurns(contents, ag, ag && claude)
	if len(contents) > 0 && contents[0]["role"] == "model" {
		// Gemini turns away a history that opens with the model's turn
		// (what an agent's compaction can leave): "function call turn comes
		// immediately after a user turn". Gemini CLI's hardenHistory puts
		// this user turn before it, and so does this.
		contents = append([]map[string]any{{"role": "user", "parts": []map[string]any{{"text": "[Continuing from previous AI thoughts...]"}}}}, contents...)
	}
	if n := len(contents); n > 0 && contents[n-1]["role"] == "model" {
		// nor one that ends with it: "Requests ending with a model turn
		// are not supported" on Vertex AI, Code Assist and Antigravity
		// alike (yycsyu: Codex compacting on gemini-3.8-flash). A user
		// turn goes after it, as Gemini CLI keeps its history ending
		contents = append(contents, map[string]any{"role": "user", "parts": []map[string]any{{"text": modelLastAsk(r)}}})
	}
	req := map[string]any{"contents": contents}
	if r.System != "" {
		req["systemInstruction"] = map[string]any{"role": "user", "parts": []map[string]any{{"text": r.System}}}
	}

	if len(r.Tools) > 0 && r.ToolChoice != "none" {
		var decls []map[string]any
		for _, t := range r.Tools {
			d := map[string]any{"name": t.Name}
			if t.Description != "" {
				d["description"] = t.Description
			}
			schema := t.Schema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			if ag {
				d["parameters"] = plainSchema(schema)
			} else {
				d["parametersJsonSchema"] = schema
			}
			decls = append(decls, d)
		}
		req["tools"] = []map[string]any{{"functionDeclarations": decls}}
		mode := "AUTO"
		fc := map[string]any{}
		switch {
		case r.ToolChoice == "required":
			mode = "ANY"
		case strings.HasPrefix(r.ToolChoice, "name:"):
			mode = "ANY"
			fc["allowedFunctionNames"] = []string{strings.TrimPrefix(r.ToolChoice, "name:")}
		case ag && claude:
			// Antigravity's Claude wants its calls checked against the schema
			mode = "VALIDATED"
		}
		fc["mode"] = mode
		req["toolConfig"] = map[string]any{"functionCallingConfig": fc}
	} else if r.WebSearch {
		// Google's own search, which Code Assist runs on Gemini as long as
		// no function is declared beside it (codeAssistSearches)
		req["tools"] = []map[string]any{{"googleSearch": map[string]any{}}}
	}

	gen := map[string]any{}
	if r.MaxTokens > 0 && (!ag || claude) {
		gen["maxOutputTokens"] = r.MaxTokens
	}
	if r.Temp != nil {
		gen["temperature"] = *r.Temp
	}
	if r.TopP != nil {
		gen["topP"] = *r.TopP
	}
	if len(r.Stop) > 0 {
		gen["stopSequences"] = r.Stop
	}
	tc := thinkingConfig(r, sent, claude, at)
	if vx {
		tc = vertexThinking(r, sent)
	}
	if tc != nil {
		gen["thinkingConfig"] = tc
		// Claude's answer has to have room past its thinking
		if b, ok := tc["thinkingBudget"].(int); ok && claude && gen["maxOutputTokens"] == nil {
			gen["maxOutputTokens"] = b + 32000
		}
	}
	if r.Format != nil {
		// the answer as JSON, of the client's schema when it gave one
		gen["responseMimeType"] = "application/json"
		if sc := r.Format.schema(); len(sc) > 0 {
			gen["responseJsonSchema"] = sc
		}
	}
	if catalog.DrawsID(sent) || r.draws {
		// an image model draws only when asked for images too: one named
		// for them, or any asked to draw (a plugin's that says it does)
		gen["responseModalities"] = []string{"TEXT", "IMAGE"}
	}
	if len(r.ImageConfig) > 0 {
		// the size and shape a Gemini client asked its image drawn at, as
		// it asked them: left out, the model drew at its default size
		// (#3820)
		gen["imageConfig"] = r.ImageConfig
	}
	if len(gen) > 0 {
		req["generationConfig"] = gen
	}
	b, _ := json.Marshal(map[string]any{"model": sent, "request": req})
	return b
}

// dropSecondAnswers leaves out a tool result whose call an earlier result
// already answered, with the images it carried: the first answer stands, as
// pairToolMessages and pairToolBlocks keep it for a Chat or Messages
// upstream. Google turns a call turn away when its functionResponse count
// differs from its functionCall count, so a client that answers one call
// twice (#2262) had every later turn refused. A call made again under the
// same id takes an answer of its own, and a result with no id is left as
// it is. msgs is not changed: a fallback builds from the same request.
func dropSecondAnswers(msgs []Message) []Message {
	var out []Message
	answered := map[string]bool{}
	for i, m := range msgs {
		var parts []Part
		for j, p := range m.Parts {
			switch {
			case p.Kind == ToolCall && p.ID != "":
				delete(answered, p.ID)
			case p.Kind == ToolResult && p.CallID != "" && p.Standalone == nil:
				if answered[p.CallID] {
					if parts == nil {
						parts = append(make([]Part, 0, len(m.Parts)), m.Parts[:j]...)
					}
					if out == nil {
						out = append(make([]Message, 0, len(msgs)), msgs[:i]...)
					}
					continue
				}
				answered[p.CallID] = true
			}
			if parts != nil {
				parts = append(parts, p)
			}
		}
		if out == nil {
			continue
		}
		if parts != nil {
			if len(parts) == 0 {
				continue
			}
			m.Parts = parts
		}
		out = append(out, m)
	}
	if out == nil {
		return msgs
	}
	return out
}

// interruptedToolNote is the result a call left unanswered is answered
// with, the words the chat build gives such a call (pairToolMessages).
const interruptedToolNote = "[The result of this tool call is unavailable: the turn was interrupted.]"

// pairFunctionTurns answers a model turn's function calls in the turn
// right after it, as Google's API reads its histories ("Please ensure
// that the number of function response parts is equal to the number of
// function call parts of the function call turn"): a call the client
// answers nowhere — an interrupted turn leaves its call pending, and
// Google turns every later turn of the conversation away with 400 over
// the one — is answered with the synthetic result the chat build gives
// such a call (pairToolMessages, #1275). The answer goes in the turn that
// follows the calls when that turn already holds answers, and otherwise
// in a turn of its own before it: a user turn mixing a functionResponse
// with text is turned away too (#1533). A Claude model on Antigravity
// (agClaude) keeps the answer and what the user said in one turn, the
// tool_result turn Anthropic reads them as. A call with no id counts as
// answered by a response of its name with no id.
func pairFunctionTurns(contents []map[string]any, ag, agClaude bool) []map[string]any {
	if len(contents) == 0 {
		return contents
	}
	partsOf := func(c map[string]any) []map[string]any {
		ps, _ := c["parts"].([]map[string]any)
		return ps
	}
	answered := map[string]bool{}
	answeredName := map[string]bool{}
	for _, c := range contents {
		for _, p := range partsOf(c) {
			if fr, ok := p["functionResponse"].(map[string]any); ok {
				if id, _ := fr["id"].(string); id != "" {
					answered[id] = true
				} else {
					name, _ := fr["name"].(string)
					answeredName[name] = true
				}
			}
		}
	}
	key := "output"
	if ag {
		key = "result"
	}
	out := make([]map[string]any, 0, len(contents)+1)
	for i, c := range contents {
		out = append(out, c)
		if c["role"] != "model" {
			continue
		}
		var synth []map[string]any
		for _, p := range partsOf(c) {
			fc, ok := p["functionCall"].(map[string]any)
			if !ok {
				continue
			}
			id, _ := fc["id"].(string)
			name, _ := fc["name"].(string)
			if id != "" && answered[id] || id == "" && answeredName[name] {
				continue
			}
			res := map[string]any{"name": name, "response": map[string]any{key: interruptedToolNote}}
			if id != "" {
				res["id"] = id
			}
			synth = append(synth, map[string]any{"functionResponse": res})
		}
		if len(synth) == 0 {
			continue
		}
		if i+1 < len(contents) && contents[i+1]["role"] == "user" && (agClaude || hasResponse(partsOf(contents[i+1]))) {
			contents[i+1]["parts"] = append(synth, partsOf(contents[i+1])...)
			continue
		}
		out = append(out, map[string]any{"role": "user", "parts": synth})
	}
	return out
}

// thinkingConfig says how hard the model should think: a level for
// Gemini 3 and later (geminiGeneration), a budget for the rest. A model
// that doesn't think gets none, and one whose id already says its level,
// at (Antigravity's gemini-3.7-flash-low), is told that level and no
// other — the effort asked for picked that id. Told no level, Antigravity's variants all but
// stop thinking once the request has tools, and give none of it back
// (#636); Antigravity's own tiered ids, sent a level, don't.
//
// Gemini 3's levels: medium goes as medium to Flash, and up to high to
// Pro, which took low and high only (3 Pro). A later Gemini is sent the
// medium it was asked, already fitted to its own levels (fitFor):
// gemini-4-argon has low, medium and high (#2083). Minimal goes only to a
// Flash variant at minimal: 3.7 and 3.8 Flash have none (AI Studio
// answers 3.8's with a 400), and the id here doesn't say which Flash serves it (Gemini
// CLI's 3.5 Flash goes out as 3.8 Flash where that is rolled out).
// Reasoning off otherwise (the auto mode classifier's least level) goes at
// low, which every Gemini 3 text model takes; an image model may have no
// low (3.1 Flash Image: minimal and high), and goes at high.
func thinkingConfig(r *Request, model string, claude bool, at string) map[string]any {
	m := strings.ToLower(model)
	if strings.HasPrefix(m, "gpt-oss") || claude && !strings.Contains(m, "thinking") {
		return nil
	}
	if r.Effort == "" && !r.Thinking {
		// Claude asked to think still has to be told how much
		if !claude {
			return nil
		}
	}
	tc := map[string]any{"includeThoughts": true}
	if gen := geminiGeneration(m); gen >= 3 || strings.HasPrefix(m, "gemini-pro-agent") {
		effort := r.Effort
		if at != "" {
			effort = at
		}
		if effort != "" {
			level := "high"
			switch {
			case effort == "low", effort == "minimal" && at != "" && !strings.Contains(m, "flash"):
				level = "low"
			case effort == "minimal" && at != "":
				level = "minimal"
			case offEffort(effort) && !catalog.DrawsID(m):
				level = "low"
			case effort == "medium" && (strings.Contains(m, "flash") || gen > 3):
				level = "medium"
			}
			tc["thinkingLevel"] = level
		}
		return tc
	}
	if off, _, _, ok := vertexBudgets(strings.TrimPrefix(m, "models/")); ok && r.ThinkOff && !claude {
		// reasoning turned off (a Chat client's effort none, the routing
		// classifier's ask) goes as 2.5's off budget, as on Vertex AI:
		// 0 for Flash and Flash-Lite, Pro's least, 128. It went as low's
		// 4096 (tangtang969 on X: a classifier that doesn't think is
		// faster)
		return map[string]any{"thinkingBudget": off}
	}
	budget := budgetOf(effortOf(r.Effort))
	if claude && r.MaxTokens > 0 && budget >= r.MaxTokens {
		budget = r.MaxTokens - 1
		if budget < 1024 {
			return nil
		}
	}
	tc["thinkingBudget"] = budget
	return tc
}

// geminiVersion is the major version in a Gemini model's id, as the id
// names it: gemini-4-argon, models/gemini-3.8-flash, gemini-2.5-pro.
var geminiVersion = regexp.MustCompile(`(?:^|/)gemini-(\d+)`)

// geminiGeneration is the generation of Gemini model names, 0 for an id
// that names none (a non-Gemini model, or an alias such as
// gemini-flash-latest, which may still be a 2.5). From Gemini 3 a model is
// told a thinking level; Gemini 4 turns a budget away with "Thinking
// budget is not supported for this model. Use thinking level instead."
// (#2083), so a new generation isn't sent one either.
func geminiGeneration(model string) int {
	m := geminiVersion.FindStringSubmatch(model)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// plainSchema is a JSON schema cut down to what Antigravity's function
// declarations take: an OpenAPI-style subset with no references, unions
// or keywords outside it.
func plainSchema(raw json.RawMessage) json.RawMessage {
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil {
		return raw
	}
	defs := map[string]any{}
	for _, k := range []string{"$defs", "definitions"} {
		if d, ok := root[k].(map[string]any); ok {
			for n, v := range d {
				defs[n] = v
			}
		}
	}
	// localRef is the schema a local $ref names in root: a JSON pointer
	// ("#", "#/properties/func/properties/triggers/items", as dsh's tool
	// parameters carry one, #4561) or, when it is none, a $defs or
	// definitions name; ok false where it names nothing here
	localRef := func(ref string) (any, bool) {
		if rest, ok := strings.CutPrefix(ref, "#/"); ok {
			cur := any(root)
			for _, tok := range strings.Split(rest, "/") {
				tok = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
				switch m := cur.(type) {
				case map[string]any:
					cur = m[tok]
				case []any:
					n, err := strconv.Atoi(tok)
					if err != nil || n < 0 || n >= len(m) {
						return nil, false
					}
					cur = m[n]
				default:
					return nil, false
				}
				if cur == nil {
					// named nowhere the pointer says: its last
					// segment as a $defs or definitions name, as
					// before pointers were walked
					d, ok := defs[ref[strings.LastIndex(ref, "/")+1:]]
					return d, ok
				}
			}
			return cur, true
		}
		if ref == "#" {
			return root, true
		}
		d, ok := defs[ref[strings.LastIndex(ref, "/")+1:]]
		return d, ok
	}
	// resolve is a member as its local $ref names it, nil when it is no
	// schema
	resolve := func(v any) map[string]any {
		m, _ := v.(map[string]any)
		if ref, ok := m["$ref"].(string); ok {
			if d, ok := localRef(ref); ok {
				m, _ = d.(map[string]any)
			} else {
				m = nil
			}
		}
		return m
	}
	// selfRef is whether a local $ref names the site it sits at or one
	// of its containers: in a tree, a reference to an ancestor is a
	// self-reference
	selfRef := func(ref, path string) bool {
		if strings.HasPrefix(ref, "#") {
			// whole segments: "#/properties/a" is no container of
			// /properties/ab
			r := ref[1:]
			return r == "" || path == r || strings.HasPrefix(path, r+"/")
		}
		return false
	}
	// refPath is the path a $ref's target is walked at: its own pointer
	// where the ref is a JSON pointer, the site's where it is none (a
	// $defs name, whose place the pointer doesn't say)
	refPath := func(ref, path string) string {
		if strings.HasPrefix(ref, "#") {
			return ref[1:]
		}
		return path
	}
	// expanding is the $ref chain being walked, a self-reference caught
	// before it recurses
	expanding := map[string]bool{}
	// schemaTypeOf is the type a schema's own shape says, what a
	// self-reference stops at: the target's type alone
	schemaTypeOf := func(m map[string]any) string {
		if t, ok := m["type"].(string); ok {
			return t
		}
		if m["properties"] != nil {
			return "object"
		}
		if m["items"] != nil {
			return "array"
		}
		return "object"
	}
	// oneSays is the schema every member of a tuple that says anything
	// says alike ({} and whatever isn't an object say nothing), or nil when
	// they differ or none says anything: one member's schema would tell the
	// model every item is that kind, and a [field, op, value] is not three
	// fields
	oneSays := func(members []any) any {
		var one map[string]any
		for _, m := range members {
			s, ok := m.(map[string]any)
			if !ok || len(s) == 0 {
				continue
			}
			if one != nil && !reflect.DeepEqual(one, s) {
				return nil
			}
			one = s
		}
		if one == nil {
			return nil
		}
		return one
	}
	var walk func(v any, depth int, path string) any
	walk = func(v any, depth int, path string) any {
		switch x := v.(type) {
		case bool:
			// a boolean schema, which JSON Schema has wherever a schema
			// goes: true is any value, the schema {}; false is none, nil,
			// and whoever holds it leaves it out. Google's Schema is an
			// object only, and turns the whole request away over a
			// boolean ("…items.items (…aiplatform.master.Schema), true",
			// han_tiv on Discord: an MCP tool's array of arrays of
			// anything)
			if x {
				return map[string]any{}
			}
			return nil
		case []any:
			out := make([]any, len(x))
			for i, e := range x {
				out[i] = walk(e, depth, path)
			}
			return out
		case map[string]any:
			if depth > 32 {
				return map[string]any{"type": "object"}
			}
			if ref, ok := x["$ref"].(string); ok {
				if d, found := localRef(ref); found {
					if expanding[ref] || selfRef(ref, path) {
						// a self-reference stops at the target's
						// type alone
						if m, ok := d.(map[string]any); ok {
							return map[string]any{"type": schemaTypeOf(m)}
						}
						return map[string]any{"type": "object"}
					}
					expanding[ref] = true
					out := walk(d, depth+1, refPath(ref, path))
					delete(expanding, ref)
					return out
				}
				return map[string]any{"type": "object"}
			}
			// allOf: one schema with all the members' fields, and every
			// field one of them requires
			if all, ok := x["allOf"].([]any); ok {
				merged := map[string]any{}
				for k, v := range x {
					if k != "allOf" {
						merged[k] = v
					}
				}
				required := requiredNames(x["required"])
				for _, e := range all {
					// a member's required is read before it is walked: walked
					// alone, a member that only requires ({"required":["a"]})
					// has no properties to keep its names for
					required = append(required, requiredNames(resolve(e)["required"])...)
					if m, ok := walk(e, depth+1, path+"/allOf").(map[string]any); ok {
						for k, v := range m {
							if k == "required" {
								continue
							}
							if k == "properties" {
								props, _ := merged["properties"].(map[string]any)
								if props == nil {
									props = map[string]any{}
								}
								for pk, pv := range v.(map[string]any) {
									props[pk] = pv
								}
								merged["properties"] = props
							} else if _, has := merged[k]; !has {
								merged[k] = v
							}
						}
					}
				}
				if len(required) > 0 {
					merged["required"] = distinctNames(required)
				}
				return walk(merged, depth+1, path)
			}
			if (x["anyOf"] != nil || x["oneOf"] != nil) && ownShape(x) {
				// a schema with its own type or properties beside an anyOf
				// or oneOf keeps them, and the union is folded into it:
				// Philo's rg tool is an object whose oneOf only says which
				// of its fields go together, and its first branch, standing
				// for the whole, sent it as {"type":"string"}; Claude on
				// Antigravity answered 400 "input_schema.type: Input should
				// be 'object'" (#4046, imuxin)
				return walk(foldUnion(x, resolve), depth+1, path)
			}
			// anyOf / oneOf with nothing of its own beside it: the first
			// member that isn't null, nullable
			for _, k := range []string{"anyOf", "oneOf"} {
				if alts, ok := x[k].([]any); ok {
					var pick map[string]any
					nullable := false
					for _, a := range alts {
						m, _ := a.(map[string]any)
						if m["type"] == "null" {
							nullable = true
						} else if pick == nil && m != nil {
							pick = m
						}
					}
					out := map[string]any{}
					if pick != nil {
						if m, ok := walk(pick, depth+1, path+"/"+k).(map[string]any); ok {
							out = m
						}
					}
					if d, ok := x["description"].(string); ok && out["description"] == nil {
						out["description"] = d
					}
					if nullable {
						out["nullable"] = true
					}
					if out["type"] == nil {
						out["type"] = "string"
					}
					return out
				}
			}
			out := map[string]any{}
			for k, v := range x {
				switch k {
				case "description", "nullable", "required", "pattern", "minimum", "maximum", "minLength", "maxLength",
					"minItems", "maxItems", "minProperties", "maxProperties", "propertyOrdering":
					out[k] = v
				case "exclusiveMinimum", "exclusiveMaximum":
					// a bound it takes only as inclusive (draft 6 on: a number)
					if n, ok := v.(float64); ok {
						if b := "m" + strings.TrimPrefix(k, "exclusiveM"); x[b] == nil {
							out[b] = n
						}
					}
				case "type":
					if ts, ok := v.([]any); ok {
						for _, t := range ts {
							if t == "null" {
								out["nullable"] = true
							} else if out["type"] == nil {
								out["type"] = t
							}
						}
						// Google has one type a node: of a list, the one
						// its items or properties are for
						for _, t := range ts {
							if t == "array" && x["items"] != nil || t == "object" && x["properties"] != nil {
								out["type"] = t
								break
							}
						}
					} else {
						out["type"] = v
					}
				case "properties":
					props := map[string]any{}
					if m, ok := v.(map[string]any); ok {
						for pk, pv := range m {
							// a property whose schema is false is one the
							// call may not have: it is left out, and so is
							// its name in required
							at := strings.ReplaceAll(strings.ReplaceAll(pk, "~", "~0"), "/", "~1")
							if w := walk(pv, depth+1, path+"/properties/"+at); w != nil {
								props[pk] = w
							}
						}
					}
					out[k] = props
				case "items":
					if w := walk(v, depth+1, path+"/items"); w != nil {
						out[k] = w
					} else if x["maxItems"] == nil {
						// items false: an array with nothing in it
						out[k] = map[string]any{}
						out["maxItems"] = 0
					}
				case "enum":
					// Google takes string enums only
					if es, ok := v.([]any); ok {
						strs := make([]any, 0, len(es))
						for _, e := range es {
							if s, ok := e.(string); ok {
								strs = append(strs, s)
							}
						}
						if len(strs) == len(es) {
							out[k] = strs
						}
					}
				default:
					// a keyword outside its subset (format, default, title,
					// additionalProperties, const…): Antigravity refuses the
					// request over any it doesn't know (#187)
				}
			}
			if c, ok := x["const"]; ok {
				if s, ok := c.(string); ok {
					out["enum"] = []any{s}
				}
			}
			if out["type"] == nil {
				switch {
				case out["properties"] != nil:
					out["type"] = "object"
				case out["items"] != nil:
					out["type"] = "array"
				}
			}
			if out["type"] == "array" {
				if t, ok := out["items"].([]any); ok {
					// a tuple (draft 07's items as an array) stands as one
					// schema: Gemini takes an items object and nothing
					// else, so members alike are said once, and members
					// that differ as any item ({} below)
					out["items"] = oneSays(t)
				}
				if out["items"] == nil {
					// the members prefixItems said are gone from the subset;
					// when they are alike, their schema stands in for them
					if pfx, _ := x["prefixItems"].([]any); len(pfx) > 0 {
						if s := oneSays(pfx); s != nil {
							out["items"] = walk(s, depth+1, path+"/items")
						}
					}
					if out["items"] == nil {
						// an array says what its members are, or Google turns
						// the whole request away over the missing field (#2826;
						// plugins#375): any item is what JSON Schema means by
						// none, and {} answers 200
						out["items"] = map[string]any{}
					}
				}
			}
			if t, ok := out["type"].(string); ok {
				// JSON Schema reads an array's keywords only on an array
				// and an object's only on an object; Google turns the
				// whole request away over one on another type ("properties
				// [display].items: field predicate failed: $type ==
				// Type.ARRAY", ferock on Discord: items beside a string
				// enum)
				if t != "array" {
					delete(out, "items")
					delete(out, "minItems")
					delete(out, "maxItems")
				}
				if t != "object" {
					delete(out, "properties")
					delete(out, "required")
					delete(out, "minProperties")
					delete(out, "maxProperties")
					delete(out, "propertyOrdering")
				}
			}
			if req, ok := out["required"].([]any); ok {
				// required names only the properties there are
				props, _ := out["properties"].(map[string]any)
				keep := []any{}
				for _, n := range req {
					if s, ok := n.(string); ok && props[s] != nil {
						keep = append(keep, s)
					}
				}
				if len(keep) == 0 {
					delete(out, "required")
				} else {
					out["required"] = keep
				}
			}
			return out
		}
		return v
	}
	b, err := json.Marshal(walk(root, 0, ""))
	if err != nil {
		return raw
	}
	return b
}

// ownShape is whether a schema says what it is beside its anyOf or
// oneOf: a type that isn't only null, properties or items.
func ownShape(x map[string]any) bool {
	if x["properties"] != nil || x["items"] != nil {
		return true
	}
	switch t := x["type"].(type) {
	case string:
		return t != "null"
	case []any:
		for _, e := range t {
			if e != "null" {
				return true
			}
		}
	}
	return false
}

// foldUnion is x, which has a shape of its own (ownShape), with its anyOf
// and oneOf folded in, for a schema subset that has no unions. What x says
// itself stands. A null branch makes it nullable. On an object, a field
// only branches declare joins its properties, as the first branch to
// declare it gives it, and a field every branch that isn't null requires
// is required: the model sees every field it may send, and is held only
// to what any choice holds it to. The rest of a branch — which fields go together, a "not" —
// is a rule the subset can't say, and the client still checks the call
// against its own schema.
func foldUnion(x map[string]any, resolve func(any) map[string]any) map[string]any {
	out := make(map[string]any, len(x))
	for k, v := range x {
		if k != "anyOf" && k != "oneOf" {
			out[k] = v
		}
	}
	object := x["type"] == "object" || x["properties"] != nil
	if ts, ok := x["type"].([]any); ok {
		for _, t := range ts {
			object = object || t == "object"
		}
	}
	props := map[string]any{}
	own, _ := x["properties"].(map[string]any)
	for k, v := range own {
		props[k] = v
	}
	var common []any
	branches := 0
	for _, k := range []string{"anyOf", "oneOf"} {
		alts, _ := x[k].([]any)
		for _, a := range alts {
			m := resolve(a)
			if m == nil {
				continue
			}
			if m["type"] == "null" {
				out["nullable"] = true
				continue
			}
			if !object {
				continue
			}
			bp, _ := m["properties"].(map[string]any)
			for _, pk := range slices.Sorted(maps.Keys(bp)) {
				if _, has := props[pk]; !has {
					props[pk] = bp[pk]
				}
			}
			br := requiredNames(m["required"])
			if branches == 0 {
				common = append([]any(nil), br...)
			} else {
				common = slices.DeleteFunc(common, func(n any) bool { return !slices.Contains(br, n) })
			}
			branches++
		}
	}
	if object {
		if len(props) > 0 {
			out["properties"] = props
		}
		if req := requiredNames(x["required"]); len(req)+len(common) > 0 {
			out["required"] = distinctNames(append(req, common...))
		}
	}
	return out
}

// requiredNames is a required list's names: its strings, a fresh slice.
func requiredNames(v any) []any {
	l, _ := v.([]any)
	var out []any
	for _, e := range l {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// distinctNames is vs with each value kept once, in order.
func distinctNames(vs []any) []any {
	var out []any
	for _, v := range vs {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// codeAssistDecoder reads Code Assist's stream: Gemini chunks, each wrapped
// in {response}.
type codeAssistDecoder struct {
	started bool
	tools   bool
	stopped bool
	// searched is a googleSearch's grounding told already: it is given
	// again with each chunk after it
	searched bool
	usage    *Usage
	// held is text that may be the start of a call the model wrote out as
	// text (see textCall), kept back until it is one or isn't
	held string
}

func (d *codeAssistDecoder) decode(data string, emit func(Event)) error {
	var ch struct {
		Response *geminiChunk `json:"response"`
		Error    *struct {
			Message string `json:"message"`
		} `json:"error"`
		geminiChunk
	}
	if err := json.Unmarshal([]byte(data), &ch); err != nil {
		// an error in a shape of its own is still the reply's end (#1933)
		if e, ok := carriedError(data); ok {
			emit(e)
		}
		return nil
	}
	if ch.Error != nil {
		emit(Event{Kind: KError, Text: cmp.Or(ch.Error.Message, streamErrorText(data))})
		return nil
	}
	c := ch.geminiChunk
	if ch.Response != nil {
		c = *ch.Response
	}
	if c.UsageMetadata != nil {
		u := c.UsageMetadata
		d.usage = &Usage{Input: max(u.Prompt-u.Cached, 0), CacheRead: u.Cached,
			Output: u.Candidates + u.Thoughts, Reasoning: u.Thoughts}
	}
	if !d.started {
		d.started = true
		// the prompt's count rides on the first chunk: told at the start,
		// as Anthropic's message_start tells it, which Claude Code's
		// context meter reads before the turn ends (#1887)
		var prompt Usage
		if d.usage != nil {
			prompt = Usage{Input: d.usage.Input, CacheRead: d.usage.CacheRead}
		}
		emit(Event{Kind: KStart, MsgID: c.ResponseID, Model: c.ModelVersion, Usage: prompt})
	}
	for _, cand := range c.Candidates {
		for _, p := range cand.Content.Parts {
			switch {
			case p.FunctionCall != nil:
				d.flush(emit)
				id := p.FunctionCall.ID
				if id == "" {
					id = "call_" + newID()
				}
				args := p.FunctionCall.Args
				if len(args) == 0 || string(args) == "null" {
					args = json.RawMessage("{}")
				}
				d.tools = true
				// its signature rides in the id, and comes back off it
				// (signedID, unsignCalls), as on chat (#687, #1445)
				emit(Event{Kind: KToolStart, ID: signedID(id, p.Signature), Name: toolOfCall(p.FunctionCall.Name)})
				emit(Event{Kind: KToolArgs, Text: string(args)})
			case p.Thought:
				d.flush(emit)
				if p.Text != "" {
					emit(Event{Kind: KThink, Text: p.Text})
				}
				if p.Signature != "" {
					emit(Event{Kind: KSig, Text: p.Signature})
				}
			case p.InlineData != nil && p.InlineData.Data != "":
				// a picture an image model drew (gemini-*-image): its
				// thought images, drafts on the way, stay with the
				// reasoning above (#620)
				d.flush(emit)
				emit(Event{Kind: KImage, Name: p.InlineData.MimeType, Text: p.InlineData.Data})
			case p.Text != "" || p.Signature != "":
				d.text(p.Text, emit)
				if p.Signature != "" {
					// Gemini's signature on its text, after it (#1445)
					d.flush(emit)
					emit(Event{Kind: KTextSig, Text: p.Signature})
				}
			}
		}
		if g := cand.GroundingMetadata; g != nil && !d.searched {
			// what googleSearch found: the pages, by their site's name and
			// an address of Google's that leads to them
			var hits []Hit
			for _, c := range g.GroundingChunks {
				if c.Web != nil && c.Web.URI != "" {
					hits = append(hits, Hit{Title: cmp.Or(c.Web.Title, c.Web.URI), URL: c.Web.URI})
				}
			}
			if len(hits) > 0 || len(g.WebSearchQueries) > 0 {
				d.flush(emit)
				d.searched = true
				emit(Event{Kind: KSearch, Text: strings.Join(g.WebSearchQueries, "; "), Hits: hits})
			}
		}
		if cand.FinishReason != "" && !d.stopped {
			d.flush(emit)
			d.stopped = true
			stop := stopFromGemini(cand.FinishReason)
			if d.tools && stop == "stop" {
				stop = "tool"
			}
			emit(Event{Kind: KStop, Stop: stop})
		}
	}
	if d.stopped && d.usage != nil {
		emit(Event{Kind: KUsage, Usage: *d.usage})
		d.usage = nil
	}
	return nil
}

// Gemini's own tools live in a namespace, default_api, and a model now and
// then names one of the client's with it: as a call's name
// (default_api:bash), or, on Antigravity, as a whole call written out in
// its reply's text — "Let's look.call:default_api:bash{command:ls}" — with
// no call made (#636). Either way the client is left with nothing it can
// run, so the decoder takes the namespace off a call's name and makes a
// call written out in text the call it meant. Text that only looks like
// the start of one is held back until it is one or isn't.
const callMarker = "call:" + geminiNamespace + ":"

const geminiNamespace = "default_api"

// toolOfCall is a call's tool name without Gemini's namespace.
func toolOfCall(name string) string {
	for _, sep := range []string{":", "."} {
		if t, ok := strings.CutPrefix(name, geminiNamespace+sep); ok && t != "" {
			return t
		}
	}
	return name
}

// maxHeld bounds a call written out as text: one that goes on longer is
// taken for text after all.
const maxHeld = 256 << 10

// text passes a text part on, making any call written out in it a call.
func (d *codeAssistDecoder) text(s string, emit func(Event)) {
	s = d.held + s
	d.held = ""
	say := func(t string) {
		if t != "" {
			emit(Event{Kind: KText, Text: t})
		}
	}
	for s != "" {
		i := strings.Index(s, callMarker)
		if i < 0 {
			// a tail that may be the start of the marker waits for the
			// next part
			k := len(callMarker) - 1
			for ; k > 0 && !strings.HasSuffix(s, callMarker[:k]); k-- {
			}
			say(s[:len(s)-k])
			d.held = s[len(s)-k:]
			return
		}
		say(s[:i])
		s = s[i:]
		name, args, n := textCall(s)
		switch {
		case n == 0 && len(s) < maxHeld: // not all here yet
			d.held = s
			return
		case n <= 0: // not a call
			say(s[:len(callMarker)])
			s = s[len(callMarker):]
		default:
			d.tools = true
			emit(Event{Kind: KToolStart, ID: "call_" + newID(), Name: name})
			emit(Event{Kind: KToolArgs, Text: string(args)})
			s = s[n:]
		}
	}
}

// flush passes held text on as text: what comes next isn't more of it.
func (d *codeAssistDecoder) flush(emit func(Event)) {
	if d.held != "" {
		emit(Event{Kind: KText, Text: d.held})
		d.held = ""
	}
}

// ctrl46 quotes a string in the calls Gemini writes out as text.
const ctrl46 = "<ctrl46>"

// textCall reads a call written out as text at the start of s,
// call:default_api:<tool>{<args>}: its tool, its arguments as JSON and its
// length; a length of 0 where s may yet be one and isn't all here, -1
// where it isn't one.
func textCall(s string) (string, json.RawMessage, int) {
	j := len(callMarker)
	for j < len(s) && isIdent(s[j]) {
		j++
	}
	if j == len(s) {
		return "", nil, 0
	}
	name := s[len(callMarker):j]
	if name == "" || s[j] != '{' {
		return "", nil, -1
	}
	end := closeBrace(s, j)
	if end < 0 {
		return "", nil, 0
	}
	args, ok := textArgs(s[j+1 : end])
	if !ok {
		return "", nil, -1
	}
	return name, args, end + 1
}

func isIdent(c byte) bool {
	return c == '_' || c == '-' || c == '.' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// closeBrace is where the brace open at s[i] closes, with braces inside
// <ctrl46> quotes left out; -1 where it doesn't in s.
func closeBrace(s string, i int) int {
	depth := 0
	for i < len(s) {
		if strings.HasPrefix(s[i:], ctrl46) {
			k := strings.Index(s[i+len(ctrl46):], ctrl46)
			if k < 0 {
				return -1
			}
			i += 2*len(ctrl46) + k
			continue
		}
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
		i++
	}
	return -1
}

// textArgs makes a written-out call's arguments a JSON object. They come
// as JSON's own members or as key:value pairs, the value quoted with
// <ctrl46>, JSON, or bare text running to the next ",key:" or the end.
func textArgs(body string) (json.RawMessage, bool) {
	body = strings.TrimSpace(body)
	if body == "" {
		return json.RawMessage("{}"), true
	}
	if obj := "{" + body + "}"; json.Valid([]byte(obj)) {
		return json.RawMessage(obj), true
	}
	var b strings.Builder
	b.WriteByte('{')
	for first := true; body != ""; first = false {
		k := 0
		for k < len(body) && isIdent(body[k]) {
			k++
		}
		if k == 0 || k == len(body) || body[k] != ':' {
			return nil, false
		}
		key, _ := json.Marshal(body[:k])
		body = body[k+1:]
		var val []byte
		if rest, ok := strings.CutPrefix(body, ctrl46); ok {
			e := strings.Index(rest, ctrl46)
			if e < 0 {
				return nil, false
			}
			val, _ = json.Marshal(rest[:e])
			body = strings.TrimLeft(rest[e+len(ctrl46):], " ")
			if body != "" {
				if body[0] != ',' {
					return nil, false
				}
				body = strings.TrimLeft(body[1:], " ")
			}
		} else {
			e := nextKey(body)
			v := strings.TrimSpace(body[:e])
			if json.Valid([]byte(v)) {
				val = []byte(v)
			} else {
				val, _ = json.Marshal(v)
			}
			body = body[e:]
			if body != "" {
				body = strings.TrimLeft(body[1:], " ")
			}
		}
		if !first {
			b.WriteByte(',')
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(val)
	}
	b.WriteByte('}')
	return json.RawMessage(b.String()), true
}

// nextKey is where a bare value ends: at the "," of the next ",key:" outside
// brackets, or the end.
func nextKey(s string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		case ',':
			if depth != 0 {
				continue
			}
			k := i + 1
			for k < len(s) && s[k] == ' ' {
				k++
			}
			start := k
			for k < len(s) && isIdent(s[k]) {
				k++
			}
			if k > start && k < len(s) && s[k] == ':' {
				return i
			}
		}
	}
	return len(s)
}

type geminiChunk struct {
	Candidates []struct {
		Content struct {
			Parts []gPart `json:"parts"`
		} `json:"content"`
		FinishReason      string `json:"finishReason"`
		GroundingMetadata *struct {
			WebSearchQueries []string `json:"webSearchQueries"`
			GroundingChunks  []struct {
				Web *struct {
					Title string `json:"title"`
					URI   string `json:"uri"`
				} `json:"web"`
			} `json:"groundingChunks"`
		} `json:"groundingMetadata"`
	} `json:"candidates"`
	UsageMetadata *struct {
		Prompt     int `json:"promptTokenCount"`
		Candidates int `json:"candidatesTokenCount"`
		Thoughts   int `json:"thoughtsTokenCount"`
		Cached     int `json:"cachedContentTokenCount"`
	} `json:"usageMetadata"`
	ModelVersion string `json:"modelVersion"`
	ResponseID   string `json:"responseId"`
}

func stopFromGemini(s string) string {
	switch s {
	case "MAX_TOKENS":
		return "length"
	case "STOP", "FINISH_REASON_UNSPECIFIED", "OTHER":
		return "stop"
	case "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL":
		return "stop"
	}
	// SAFETY, RECITATION, BLOCKLIST, PROHIBITED_CONTENT, SPII, ...
	return "filter"
}

// hasResponse says whether a Gemini turn's parts hold a tool's response.
func hasResponse(parts []map[string]any) bool {
	for _, p := range parts {
		if p["functionResponse"] != nil {
			return true
		}
	}
	return false
}

// antigravityRound is ask, a round of magpie's own search for p, told as
// the refusal it is when Antigravity turns the system prompt away: its 429
// or the reply's own error event gets the hint, and the attempt is marked
// so the account doesn't rest. Codex asks every turn with its web_search
// tool, which an Antigravity model is given magpie's search for, so its
// turns went this way and came back as the plain 429 that Codex retried to
// its limit (Jodiezzzzzz on X, 10-27).
func antigravityRound(w http.ResponseWriter, p provider.Provider, ask round) round {
	if accountAgent(p) != "antigravity" {
		return ask
	}
	return func(ctx context.Context, req *Request) (<-chan Event, int, string) {
		in, status, msg := ask(ctx, req)
		if in == nil {
			if status == http.StatusTooManyRequests && antigravityTurnedAway(p, req.System, msg) {
				markAntigravityTurnsAway(w, req.System)
				msg += " — " + antigravityHint(req.System)
			}
			return in, status, msg
		}
		out := make(chan Event, cap(in))
		go func() {
			defer close(out)
			for ev := range in {
				if ev.Kind == KError && markAntigravityRefused(w, p, req.System, ev.Text) {
					ev.Text += " — " + antigravityHint(req.System)
				}
				select {
				case out <- ev:
				case <-ctx.Done():
					// in's own reader stops sending once ctx is done
					return
				}
			}
		}()
		return out, status, msg
	}
}
