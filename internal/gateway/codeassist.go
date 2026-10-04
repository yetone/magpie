package gateway

import (
	"cmp"
	"encoding/json"
	"regexp"
	"strings"

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
// "Resource has been exhausted (e.g. check quota)" whatever the quota left,
// the one Claude Code and its Agent SDK (Claude Desktop's chats) send: its
// billing line or the SDK's identity line anywhere in the system
// instruction turns the request away, on every model, while the same words
// in a message go through (#666). magpie doesn't hide them from Google; it
// says why, so no one chases a quota that isn't spent.
func antigravityTurnsAway(system string) bool {
	return strings.Contains(system, "x-anthropic-billing-header:") ||
		strings.Contains(system, "built on Anthropic's Claude Agent SDK")
}

const antigravityTurnedAwayHint = "not a quota: Antigravity turns away Claude Code's and the Claude Agent SDK's system prompt (Claude Code, Claude Desktop's chats) with this 429; use another provider for them"

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
	sent := codeAssistID(model, r.Effort, p.Account.Agent)
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
func buildCodeAssistSent(r *Request, sent, agent string) []byte {
	ag := agent == "antigravity"
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
	for _, m := range r.Messages {
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		}
		var parts []map[string]any
		for _, p := range m.Parts {
			switch p.Kind {
			case Text:
				if p.Text != "" {
					parts = append(parts, map[string]any{"text": p.Text})
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
				parts = append(parts, map[string]any{"functionCall": call, "thoughtSignature": skipSignature})
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
				// CLI sends a file it read
				for _, im := range p.Images {
					if im.Data != "" {
						parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": im.MediaType, "data": im.Data}})
					} else if im.URL != "" {
						parts = append(parts, map[string]any{"fileData": map[string]any{"mimeType": im.MediaType, "fileUri": im.URL}})
					}
				}
			}
			// thinking isn't sent back: its signatures belong to whoever
			// made them, and Google turns away ones it didn't
		}
		if len(parts) == 0 {
			continue
		}
		if n := len(contents); n > 0 && contents[n-1]["role"] == role {
			contents[n-1]["parts"] = append(contents[n-1]["parts"].([]map[string]any), parts...)
			continue
		}
		contents = append(contents, map[string]any{"role": role, "parts": parts})
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
	if tc := thinkingConfig(r, sent, claude, at); tc != nil {
		gen["thinkingConfig"] = tc
		// Claude's answer has to have room past its thinking
		if b, ok := tc["thinkingBudget"].(int); ok && claude && gen["maxOutputTokens"] == nil {
			gen["maxOutputTokens"] = b + 32000
		}
	}
	if catalog.DrawsID(sent) {
		// an image model draws only when asked for images too
		gen["responseModalities"] = []string{"TEXT", "IMAGE"}
	}
	if len(gen) > 0 {
		req["generationConfig"] = gen
	}
	b, _ := json.Marshal(map[string]any{"model": sent, "request": req})
	return b
}

// thinkingConfig says how hard the model should think: a level for
// Gemini 3, a budget for the rest. A model that doesn't think gets none,
// and one whose id already says its level, at (Antigravity's
// gemini-3.7-flash-low), is told that level and no other — the effort
// asked for picked that id. Told no level, Antigravity's variants all but
// stop thinking once the request has tools, and give none of it back
// (#636); Antigravity's own tiered ids, sent a level, don't.
//
// Gemini 3's levels: Flash takes minimal, low, medium and high, so medium
// goes as medium; Pro takes low and high only, so medium goes up to high.
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
	if strings.HasPrefix(m, "gemini-3") || strings.HasPrefix(m, "gemini-pro-agent") {
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
			case effort == "medium" && strings.Contains(m, "flash"):
				level = "medium"
			}
			tc["thinkingLevel"] = level
		}
		return tc
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
	var walk func(v any, depth int) any
	walk = func(v any, depth int) any {
		switch x := v.(type) {
		case []any:
			out := make([]any, len(x))
			for i, e := range x {
				out[i] = walk(e, depth)
			}
			return out
		case map[string]any:
			if depth > 32 {
				return map[string]any{"type": "object"}
			}
			if ref, ok := x["$ref"].(string); ok {
				name := ref[strings.LastIndex(ref, "/")+1:]
				if d, ok := defs[name]; ok {
					return walk(d, depth+1)
				}
				return map[string]any{"type": "object"}
			}
			// allOf: one schema with all the members' fields
			if all, ok := x["allOf"].([]any); ok {
				merged := map[string]any{}
				for k, v := range x {
					if k != "allOf" {
						merged[k] = v
					}
				}
				for _, e := range all {
					if m, ok := walk(e, depth+1).(map[string]any); ok {
						for k, v := range m {
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
				return walk(merged, depth+1)
			}
			// anyOf / oneOf: the first member that isn't null, nullable
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
						if m, ok := walk(pick, depth+1).(map[string]any); ok {
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
					} else {
						out["type"] = v
					}
				case "properties":
					props := map[string]any{}
					if m, ok := v.(map[string]any); ok {
						for pk, pv := range m {
							props[pk] = walk(pv, depth+1)
						}
					}
					out[k] = props
				case "items":
					out[k] = walk(v, depth+1)
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
	b, err := json.Marshal(walk(root, 0))
	if err != nil {
		return raw
	}
	return b
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
		return nil
	}
	if ch.Error != nil {
		emit(Event{Kind: KError, Text: ch.Error.Message})
		return nil
	}
	c := ch.geminiChunk
	if ch.Response != nil {
		c = *ch.Response
	}
	if !d.started {
		d.started = true
		emit(Event{Kind: KStart, MsgID: c.ResponseID, Model: c.ModelVersion})
	}
	if c.UsageMetadata != nil {
		u := c.UsageMetadata
		d.usage = &Usage{Input: max(u.Prompt-u.Cached, 0), CacheRead: u.Cached,
			Output: u.Candidates + u.Thoughts, Reasoning: u.Thoughts}
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
				emit(Event{Kind: KToolStart, ID: id, Name: toolOfCall(p.FunctionCall.Name)})
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
			case p.Text != "":
				d.text(p.Text, emit)
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
