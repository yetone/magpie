package redact

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// kept are the keys whose strings are left alone: ids, names and kinds the
// vendor needs as they are, and what is sealed or encoded (a thinking
// block's signature, reasoning's encrypted content, an image's data). A
// tool is named by its name and, for an MCP tool, its server's: a
// Responses function_call's namespace, an mcp_call's server_label,
// Anthropic's mcp_tool_use server_name. Codex checks a namespace against
// ^[a-zA-Z0-9_-]+$, so one with a word masked in it was a 400 on every turn
// that replayed the call (#3143).
var kept = map[string]bool{
	"signature": true, "encrypted_content": true, "data": true, "thoughtSignature": true, "thought_signature": true,
	"model": true, "id": true, "tool_use_id": true, "call_id": true, "item_id": true, "type": true, "role": true,
	"name": true, "namespace": true, "server_label": true, "server_name": true, "previous_response_id": true, "prompt_cache_key": true, "media_type": true, "mime_type": true,
	"mimeType": true, "url": true, "image_url": true, "file_id": true, "reasoning_effort": true, "effort": true,
	"stop_reason": true, "finish_reason": true, "status": true, "object": true, "event": true,
}

// keepIn is keep for a request's string at path, with its ids kept too.
// In a tool call's arguments (args) the keys are the tool's, not the
// vendor's: a "signature", "name" or "user_id" there holds what the agent
// wrote, so only an encoded data: value is kept. In what the agent defines
// for the vendor to check (defs: its tools, their schemas and grammars, the
// output's schema, the request's settings) only a description is masked:
// the rest is read by the vendor as structure, and a word masked inside a
// Lark grammar's rule name ("pd" in update_hunk) left a grammar the vendor
// turned away with 400 "Invalid lark grammar" (#1911).
func keepIn(path, key, s string, args, defs []string) bool {
	if strings.HasPrefix(s, "data:") {
		return true
	}
	if under(path, args) {
		return false
	}
	if under(path, defs) {
		return key != "description"
	}
	return kept[key] || isID(key)
}

// isID says a key holds an identifier the vendor matches against another:
// a tool result's tool_call_id against its call's id, an approval's
// approval_request_id against its request. A vendor's ids are its own
// letters and digits, and GLM's call_ and 19 digits can read as a bank
// card number; masked on one side only, the two no longer match.
func isID(key string) bool {
	return key == "id" || key == "ids" || strings.HasSuffix(key, "_id") || strings.HasSuffix(key, "_ids") ||
		strings.HasSuffix(key, "Id") || strings.HasSuffix(key, "Ids")
}

// signatureKeys are the keys of what a vendor seals the content beside it
// with: Anthropic's thinking signature, Gemini's thought signature,
// Responses' encrypted reasoning.
var signatureKeys = []string{"signature", "thoughtSignature", "thought_signature", "encrypted_content"}

// scan finds three kinds of objects in body. signed are those a vendor
// sealed with a signature: what is in them the vendor wrote and checks, so
// it goes back exactly as the vendor wrote it. args are a tool call's
// arguments (Anthropic's tool_use input, Gemini's functionCall args), whose
// keys are the tool's own, as are those of Gemini's functionResponse
// response, the structured result the tool gave; nothing in them counts
// as sealed. defs are what the agent defines for the vendor rather than
// says to the model (definitions).
func scan(body []byte) (signed, args, defs []string) {
	found := false
	for _, k := range append(signatureKeys, "functionCall", "function_call", "functionResponse", "function_response", "arguments") {
		found = found || bytes.Contains(body, []byte(`"`+k+`"`))
	}
	// a type that ends so (mcp_tool_use, custom_tool_call), without the
	// quote before it
	found = found || bytes.Contains(body, []byte(`tool_use"`)) || bytes.Contains(body, []byte(`_call"`))
	for _, k := range defKeys {
		found = found || bytes.Contains(body, []byte(`"`+k+`"`))
	}
	if !found {
		return nil, nil, nil
	}
	var v any
	if json.Unmarshal(body, &v) != nil {
		return nil, nil, nil
	}
	defs = definitions(v)
	var visit func(path, key string, v any)
	visit = func(path, key string, v any) {
		switch x := v.(type) {
		case map[string]any:
			if sealed(x) {
				signed = append(signed, path)
				return
			}
			t, _ := x["type"].(string)
			for k, c := range x {
				if toolArgs(t, key, k) || k == "response" && (key == "functionResponse" || key == "function_response") {
					args = append(args, join(path, k))
					continue
				}
				visit(join(path, k), k, c)
			}
		case []any:
			for i, c := range x {
				visit(join(path, strconv.Itoa(i)), key, c)
			}
		}
	}
	visit("", "", v)
	return signed, args, defs
}

// toolArgs says the value under k, in an object of type t under key, is
// what the model wrote for a tool to run, in any protocol: Anthropic's
// input of every *_tool_use (tool_use, server_tool_use, mcp_tool_use),
// a Responses *_call's arguments, input, action or operation
// (function_call, mcp_call, custom_tool_call, tool_search_call,
// local_shell_call, computer_call, apply_patch_call), a Chat Completions
// or Ollama tool call's function.arguments, and Gemini's functionCall
// args (and a legacy Chat function_call's arguments). Counted only for tool_use and server_tool_use, an mcp_tool_use's
// input had the protocol's key names (url, data, *_id) kept as written,
// a phone, a card and a key among them.
func toolArgs(t, key, k string) bool {
	switch {
	case strings.HasSuffix(t, "tool_use"):
		return k == "input"
	case strings.HasSuffix(t, "_call"):
		return k == "arguments" || k == "input" || k == "action" || k == "operation"
	case key == "function" || key == "functionCall" || key == "function_call":
		return k == "arguments" || k == "args"
	}
	return false
}

// defKeys are the request's own keys whose values the agent defines for the
// vendor to read as structure, in every protocol an agent sends: the tools
// (Anthropic, Chat Completions, Responses, Gemini) and the choice among
// them, the output's schema (Chat's response_format, Responses' text,
// Anthropic's output_format and output_config, Gemini's generationConfig),
// Responses' reasoning and include, how the reply streams
// (stream_options, whose reasoning_summary_delivery the vendor checks is
// "sequential" or "sequential_cutoff", #3144), and the client's ids that
// no model reads (client_metadata: Codex's installation, session, thread,
// turn and window, as a JSON string in x-codex-turn-metadata, which the
// vendor and magpie's own sessions match a turn by).
var defKeys = []string{
	"tools", "tool_choice", "functions", "function_call", "toolConfig", "tool_config",
	"response_format", "text", "reasoning", "include", "output_format", "output_config",
	"generationConfig", "generation_config", "stream_options", "client_metadata",
}

// defItems are the Responses input items that carry tools: Responses
// Lite's additional_tools, which Codex sends its tools in in place of the
// request's tools, and a tool search's output, the tools it found.
var defItems = map[string]bool{"additional_tools": true, "tool_search_output": true}

// definitions are the paths in a request v at which the agent defines
// rather than says: the request's own defKeys, and the tools of its
// defItems. Only where they sit in the protocol counts: a "tools" or
// "text" key deeper in, in a message or a tool call's arguments, is what
// the agent or the model wrote.
func definitions(v any) []string {
	m, _ := v.(map[string]any)
	if m == nil {
		return nil
	}
	var defs []string
	for _, k := range defKeys {
		if _, ok := m[k]; !ok {
			continue
		}
		if k == "text" {
			// Responses' text is the output's settings; a prompt's text
			// (a completion's, a Gemini contents part's) is a string
			if _, ok := m[k].(map[string]any); !ok {
				continue
			}
		}
		defs = append(defs, k)
	}
	items, _ := m["input"].([]any)
	for i, it := range items {
		if im, _ := it.(map[string]any); im != nil {
			if t, _ := im["type"].(string); defItems[t] {
				defs = append(defs, join(join("input", strconv.Itoa(i)), "tools"))
			}
		}
	}
	return defs
}

// sealed says x is a block the vendor signed: a part with Gemini's thought
// signature, or a thinking or reasoning block with Anthropic's signature or
// Responses' encrypted content. Another object with a key of that name
// (a text block, the request itself) holds the agent's words.
func sealed(x map[string]any) bool {
	t, _ := x["type"].(string)
	for _, k := range signatureKeys {
		if s, _ := x[k].(string); s == "" {
			continue
		}
		if k == "thoughtSignature" || k == "thought_signature" ||
			t == "thinking" || t == "reasoning" || strings.HasPrefix(t, "reasoning.") {
			return true
		}
	}
	return false
}

// under says path is in one of the objects at roots.
func under(path string, roots []string) bool {
	for _, r := range roots {
		if r == "" || path == r || strings.HasPrefix(path, r+".") {
			return true
		}
	}
	return false
}

// walk calls fn with every string value in the JSON document b — its path
// (keys and indexes joined by dots) and the key it is under — and writes
// what fn returns in its place. Everything else is kept byte for byte, and
// b comes back as it is if nothing changed; if it isn't JSON, ok is false.
func walk(b []byte, fn func(path, key, s string) string) (out []byte, ok bool) {
	w := walker{in: b, fn: fn}
	i, good := w.value(w.ws(0), "", "")
	if !good || w.ws(i) != len(b) {
		return b, false
	}
	if w.last == 0 {
		return b, true
	}
	w.out.Write(b[w.last:])
	out = w.out.Bytes()
	if cap(out)-len(out) > len(out)/8 {
		// placeholders longer than what they stand for grew it past the
		// room made for it: held as it is, a doubled buffer (#3383)
		out = bytes.Clone(out)
	}
	return out, true
}

type walker struct {
	in   []byte
	out  bytes.Buffer
	last int
	fn   func(path, key, s string) string
}

func (w *walker) ws(i int) int {
	for i < len(w.in) && (w.in[i] == ' ' || w.in[i] == '\t' || w.in[i] == '\n' || w.in[i] == '\r') {
		i++
	}
	return i
}

func join(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}

// str is the end of the string literal that starts at i.
func (w *walker) str(i int) (end int, escaped, ok bool) {
	for j := i + 1; j < len(w.in); j++ {
		switch w.in[j] {
		case '\\':
			escaped = true
			j++
		case '"':
			return j + 1, escaped, true
		}
	}
	return 0, false, false
}

func (w *walker) value(i int, path, key string) (int, bool) {
	if i >= len(w.in) {
		return i, false
	}
	switch c := w.in[i]; c {
	case '{':
		i = w.ws(i + 1)
		if i < len(w.in) && w.in[i] == '}' {
			return i + 1, true
		}
		for {
			if i >= len(w.in) || w.in[i] != '"' {
				return i, false
			}
			end, esc, ok := w.str(i)
			if !ok {
				return i, false
			}
			k := string(w.in[i+1 : end-1])
			if esc {
				if json.Unmarshal(w.in[i:end], &k) != nil {
					return i, false
				}
			}
			i = w.ws(end)
			if i >= len(w.in) || w.in[i] != ':' {
				return i, false
			}
			if i, ok = w.value(w.ws(i+1), join(path, k), k); !ok {
				return i, false
			}
			i = w.ws(i)
			if i < len(w.in) && w.in[i] == ',' {
				i = w.ws(i + 1)
				continue
			}
			if i < len(w.in) && w.in[i] == '}' {
				return i + 1, true
			}
			return i, false
		}
	case '[':
		i = w.ws(i + 1)
		if i < len(w.in) && w.in[i] == ']' {
			return i + 1, true
		}
		for n := 0; ; n++ {
			var ok bool
			// an array's strings are under the key the array is
			if i, ok = w.value(i, join(path, strconv.Itoa(n)), key); !ok {
				return i, false
			}
			i = w.ws(i)
			if i < len(w.in) && w.in[i] == ',' {
				i = w.ws(i + 1)
				continue
			}
			if i < len(w.in) && w.in[i] == ']' {
				return i + 1, true
			}
			return i, false
		}
	case '"':
		end, esc, ok := w.str(i)
		if !ok {
			return i, false
		}
		s := string(w.in[i+1 : end-1])
		if esc && json.Unmarshal(w.in[i:end], &s) != nil {
			return i, false
		}
		if t := w.fn(path, key, s); t != s {
			if w.out.Cap() == 0 {
				// the masked body is about the body's size: grown by
				// doubling, its buffer was up to twice that, held as the
				// request's body while it streams (#3383)
				w.out.Grow(len(w.in) + len(w.in)/16 + 64)
			}
			w.out.Write(w.in[w.last:i])
			w.out.WriteByte('"')
			w.out.WriteString(jsonEscape(t))
			w.out.WriteByte('"')
			w.last = end
		}
		return end, true
	default:
		j := i
		for j < len(w.in) && !strings.ContainsRune(",]} \t\r\n", rune(w.in[j])) {
			j++
		}
		if j == i {
			return i, false
		}
		return j, true
	}
}

// jsonEscape is s as it goes between the quotes of a JSON string.
func jsonEscape(s string) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(s)
	out := b.Bytes()
	return string(out[1 : len(out)-2]) // the quotes and the newline
}

// namedSecret says a string under a field named for a secret holds one,
// whatever the value looks like: the rules judge a value by its shape, the
// name by what a field so called carries (#2946) — {"api_key": "…"} with a
// value no rule knows, an AKIA under access_key_id, a bearer token under
// "Authorization". A short value, one that stands for a variable (${PASS},
// <token>, ****), and a moment (a token's expiry,
// access_token_expires_at, dchaofei #5002) are left alone. It is asked
// only of a tool call's arguments and a tool's structured result (args),
// whose keys are the tool's: a key the vendor's protocol names
// (Anthropic's mcp_servers[].authorization_token, a tool's own settings)
// is the vendor's to read, and masked by its name, it would no longer
// work.
func namedSecret(key, s string) bool {
	return key != "" && len(s) >= 8 && notAVariable(s) && !timestampValue(s) && SecretName(key)
}

// MaskJSON masks the strings of a request body, and says how many values
// it masked. A body that isn't JSON is masked as text.
//
// In a tool call's arguments and a tool's structured result (args), a
// field named for a secret has its value masked by its name, with secret
// masking on (#2946). Anywhere else a key is the protocol's, and a value
// is judged by its shape alone. What
// a vendor sealed with a signature (a thinking block, Gemini's
// thoughts, Responses' encrypted reasoning) is checked by the vendor
// against the text it wrote, so it goes back as written: the values
// masked elsewhere in the request become their placeholders again, which
// is what the vendor wrote where the agent reads the value (Restore), as
// do the secrets masked before (knownSpans), and nothing else in it is
// touched. Masked by the rules instead, a value the
// vendor wrote as a placeholder in other words around it ("the password
// is {{SECRET_…}}") went back as the value itself, and what the vendor
// wrote of its own that looks like an email or a phone number went as a
// placeholder: either way the text no longer matched its signature, and
// Anthropic answers 400 "Invalid `signature` in `thinking` block".
func MaskJSON(body []byte, o Options) ([]byte, int) {
	o.sent = true
	signed, args, defs := scan(body)
	masked := func(path, key, s string) bool {
		return !keepIn(path, key, s, args, defs) && (signed == nil || !under(path, signed))
	}
	// What the agent defines (its tools' and schemas' descriptions) is
	// masked by the rules alone, as with the model off: it is the agent's
	// or a tool server's text, the same in every request, and no one's
	// conversation. Read by the model it was most of a request's text,
	// 52,577 of Claude Code 2.1.296's 67,532 bytes, and on a 4-core
	// server the first request waited out the gateway's 2 minutes while
	// the model read it (niuniu_8964 on X); on three real Claude Code
	// requests the model masked nothing there the rules didn't.
	defsO := o
	defsO.Model = nil
	if o.Model != nil {
		// the model is asked once, for all of the request's texts
		var texts []string
		if _, ok := walk(body, func(path, key, s string) string {
			if masked(path, key, s) && !under(path, defs) {
				texts = append(texts, s)
			}
			return s
		}); ok {
			o = withModel(o, texts)
		}
	}
	total := 0
	var seen []string // value, placeholder, value, placeholder…
	put := func(kind, v string) string {
		p := o.put()(kind, v)
		seen = append(seen, v, p)
		return p
	}
	// the definitions after the rest, so a value the model found in what
	// is said is masked in them too, as known
	out, ok := walk(body, func(path, key, s string) string {
		if strings.HasPrefix(s, "data:") || signed != nil && under(path, signed) {
			return s
		}
		if o.Secrets && under(path, args) && namedSecret(key, s) {
			p := put("SECRET", s)
			total++
			return p
		}
		if !masked(path, key, s) || under(path, defs) {
			return s
		}
		so := o
		so.args = under(path, args) || key == "arguments"
		t, n := mask(s, so, put, asJSON(key, ""))
		total += n
		return t
	})
	if ok && len(defs) > 0 {
		out, ok = walk(out, func(path, key, s string) string {
			if !masked(path, key, s) || !under(path, defs) {
				return s
			}
			t, n := mask(s, defsO, put, asJSON(key, ""))
			total += n
			return t
		})
	}
	if !ok {
		t, n := Mask(string(body), o)
		return []byte(t), n
	}
	if len(signed) == 0 {
		return out, total
	}
	var remask *strings.Replacer
	if len(seen) > 0 {
		remask = replacerOf(seen)
	}
	out, _ = walk(out, func(path, key, s string) string {
		if keepIn(path, key, s, args, defs) || !under(path, signed) {
			return s
		}
		t := s
		if remask != nil {
			if t = remask.Replace(s); t != s {
				total++
			}
		}
		// and a secret masked before, though nothing else in this request
		// has it now (the turn that brought it was compacted away): the
		// vendor wrote its placeholder there too. Not personal data: a
		// number or an address the vendor wrote of its own can be one
		// masked in another conversation (13800138000 is in many), and
		// masked, its text would no longer match its signature
		ko := Options{Secrets: o.Secrets, Route: o.Route}
		t, n := apply(t, sentSpans(sentKnown, t, ko, func() []span { return knownSpans(t, ko) }), o.put(), o)
		total += n
		return t
	})
	return out, total
}

// replacerOf swaps each value of pairs (value, placeholder, …) for its
// placeholder, the longest value first where two start at the same place.
func replacerOf(pairs []string) *strings.Replacer {
	type pair struct{ v, p string }
	var ps []pair
	have := map[string]bool{}
	for i := 0; i+1 < len(pairs); i += 2 {
		if !have[pairs[i]] {
			have[pairs[i]] = true
			ps = append(ps, pair{pairs[i], pairs[i+1]})
		}
	}
	sort.SliceStable(ps, func(i, j int) bool { return len(ps[i].v) > len(ps[j].v) })
	args := make([]string, 0, 2*len(ps))
	for _, p := range ps {
		args = append(args, p.v, p.p)
	}
	return strings.NewReplacer(args...)
}

// asJSON says a string under key holds JSON text of its own, so a value
// put back in it goes in escaped: a tool call's arguments, as a whole or
// streamed in pieces.
func asJSON(key, eventType string) bool {
	return key == "arguments" || key == "partial_json" || key == "delta" && strings.Contains(eventType, "arguments")
}

// typeOf is the type a JSON object says it is, wherever its "type" is
// among its keys. Read as the keys came, a Responses
// function_call_arguments.delta whose "delta" came before its "type", as
// a Go map writes them, had its value put back unescaped: a password
// with a quote in it ended the arguments' string there, and the arguments
// the agent put together were JSON no longer.
func typeOf(data []byte) string {
	typ := ""
	walk(data, func(path, _, s string) string {
		if path == "type" {
			typ = s
		}
		return s
	})
	return typ
}

// RestoreJSON puts the values back in a response body.
func RestoreJSON(body []byte) []byte { return restoreJSON(body, nil) }

// restoreJSON is RestoreJSON, counting in put as restore does.
func restoreJSON(body []byte, put map[string]int) []byte {
	if !heldIn(body) && !hasShapes(body) {
		return body
	}
	typ := typeOf(body)
	out, ok := walk(body, func(path, key, s string) string {
		return restore(s, asJSON(key, typ), put)
	})
	if !ok {
		return []byte(restore(string(body), false, put))
	}
	return out
}

// heldIn says b may have a placeholder of a value held, in any of the
// ways Restore takes one back: a kind, _ and the 8 of its hash, in any
// case, with its braces or without. Looked for "{{" alone, a whole reply
// whose model wrote PERSON_kb32hzkg or {PERSON_kb32hzkg} kept it, while a
// stream had it put back.
func heldIn(b []byte) bool {
	mu.RLock()
	defer mu.RUnlock()
	if len(values) == 0 {
		return false
	}
	for i := 0; ; {
		j := bytes.IndexByte(b[i:], '_')
		if j < 0 {
			return false
		}
		i += j + 1
		z := i + 8
		if z > len(b) || z < len(b) && isWordByte(b[z]) || !base32Lower(b[i:z]) {
			continue
		}
		hash := strings.ToLower(string(b[i:z]))
		for k := range kinds {
			if n := len(k); i-1 >= n && strings.EqualFold(string(b[i-1-n:i-1]), k) {
				if _, ok := values["{{"+strings.ToUpper(k)+"_"+hash+"}}"]; ok {
					return true
				}
			}
		}
	}
}

// base32Lower says b is of the letters a placeholder's hash has, in
// either case.
func base32Lower(b []byte) bool {
	for _, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '2' && c <= '7') {
			return false
		}
	}
	return true
}
