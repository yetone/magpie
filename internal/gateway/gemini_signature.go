package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// Gemini's thought signatures on tool calls (#687). Gemini 3 signs the
// first function call of each step it thinks through, and turns the next
// request away without the signature back on that call: 400 "Function call
// is missing a thought_signature in functionCall parts". Its
// OpenAI-compatible API gives the signature beside the call, in
// tool_calls[].extra_content.google.thought_signature, and takes it back
// there. A Chat client knows no such field (Pi's openai-completions drops
// it, as the AI SDK does), but every client sends a call's id back as it
// was given. So the signature rides in the id the client is given,
// <id>__ts<n>__<signature, base64url>, and comes off it on the way back:
// into extra_content for Gemini, and left out for any other upstream,
// which gets the id the call had.
//
// n is how many characters the signature has in the id. Some clients
// shorten an id they were given (one caps them at 40 characters, #1536),
// and what is left of a signature often still decodes as base64, so only
// n tells a shortened signature from a whole one. A signature that isn't
// whole is none: Gemini answers a partial one with 400 "Corrupted thought
// signature".
//
// A call with no signature to give back — made by another model, or an
// id a client shortened — goes to Gemini with the value Google documents
// for history it didn't sign, skip_thought_signature_validator, as the
// Code Assist path sends every call.
//
// A signature grows with the step's thinking, to several KB, and an id
// that long broke a client that indexes ids (#2411, liangxyz: LobeHub's
// PostgreSQL index takes 2704 bytes). So the signature itself is kept in
// magpie's folder (sigStore) and the id carries a short reference to it,
// <id>__tsk16__<key>. A reference magpie no longer has reads as no
// signature, as a shortened id does.

const (
	// sigMark begins the signature in an id: __ts<n>__ for the signature's
	// own bytes, __tsr<n>__ for one that isn't canonical base64, its text.
	// Ids magpie gave before #1536 have no n (__ts__, __tsr__); they are
	// still read, with nothing to check them by.
	sigMark = "__ts"
	sigRaw  = "r"
	sigKept = "k"
	sigEnd  = "__"
)

// signedID is a tool call's id carrying its thought signature.
func signedID(id, sig string) string {
	if sig == "" {
		return id
	}
	if id == "" {
		id = "call_" + newID()
	}
	if key := keepSignature(sig); key != "" {
		return id + sigMark + sigKept + strconv.Itoa(len(key)) + sigEnd + key
	}
	kind, enc := sigRaw, base64.RawURLEncoding.EncodeToString([]byte(sig))
	if b, err := base64.StdEncoding.DecodeString(sig); err == nil && base64.StdEncoding.EncodeToString(b) == sig {
		kind, enc = "", base64.RawURLEncoding.EncodeToString(b)
	}
	return id + sigMark + kind + strconv.Itoa(len(enc)) + sigEnd + enc
}

// unsignedID splits an id signedID made into the call's own id and its
// signature; any other id, or one whose signature was cut short, comes
// back as it is, with no signature.
func unsignedID(id string) (string, string) {
	for at := 1; at < len(id); {
		i := strings.Index(id[at:], sigMark)
		if i < 0 {
			break
		}
		i += at
		at = i + 1
		rest := id[i+len(sigMark):]
		raw := strings.HasPrefix(rest, sigRaw)
		if raw {
			rest = rest[len(sigRaw):]
		}
		kept := !raw && strings.HasPrefix(rest, sigKept)
		if kept {
			rest = rest[len(sigKept):]
		}
		d := 0
		for d < len(rest) && rest[d] >= '0' && rest[d] <= '9' {
			d++
		}
		enc, ok := strings.CutPrefix(rest[d:], sigEnd)
		if !ok {
			continue // not a mark: part of the call's own id
		}
		if kept && d == 0 {
			continue // a kept signature's mark always has its length
		}
		// the first mark decides: a signature cut short is none
		if d > 0 && rest[:d] != strconv.Itoa(len(enc)) {
			return id, ""
		}
		if kept {
			if sig := keptSignature(enc); sig != "" {
				return id[:i], sig
			}
			return id, ""
		}
		b, err := base64.RawURLEncoding.DecodeString(enc)
		if err != nil || len(b) == 0 {
			return id, ""
		}
		if raw {
			return id[:i], string(b)
		}
		return id[:i], base64.StdEncoding.EncodeToString(b)
	}
	return id, ""
}

// unsignCalls takes the signatures off a parsed request's call ids: a tool
// call keeps its own id and has the signature as its Signature, a result
// answers that id. Messages are copied before they change.
func unsignCalls(r *Request) {
	for i, m := range r.Messages {
		var parts []Part
		for j, p := range m.Parts {
			switch p.Kind {
			case ToolCall:
				id, sig := unsignedID(p.ID)
				if sig == "" {
					continue
				}
				p.ID = id
				if p.Signature == "" {
					p.Signature = sig
				}
			case ToolResult:
				id, sig := unsignedID(p.CallID)
				if sig == "" {
					continue
				}
				p.CallID = id
			default:
				continue
			}
			if parts == nil {
				parts = slices.Clone(m.Parts)
			}
			parts[j] = p
		}
		if parts != nil {
			r.Messages[i].Parts = parts
		}
	}
}

// googleSignature is the thought signature in a tool call's extra_content.
func googleSignature(extra json.RawMessage) string {
	if len(extra) == 0 {
		return ""
	}
	var v struct {
		Google struct {
			ThoughtSignature string `json:"thought_signature"`
		} `json:"google"`
	}
	json.Unmarshal(extra, &v)
	return v.Google.ThoughtSignature
}

// googleExtra is extra_content carrying a thought signature.
func googleExtra(sig string) map[string]any {
	return map[string]any{"google": map[string]any{"thought_signature": sig}}
}

// chatCallSignatures is a Chat request relayed as it is to provider by
// with the signatures off its call ids: for Gemini (gemini) each call has
// its own in extra_content, or skip_thought_signature_validator in a turn
// with none; for another upstream the ids are only the calls' own again.
// A signature another provider gave (foreignSignature) goes to none, nor
// does any when keep is false (signatureRefused: asked again without).
func chatCallSignatures(body []byte, gemini bool, by string, keep bool) []byte {
	signed := bytes.Contains(body, []byte(sigMark))
	if !signed && !(gemini && bytes.Contains(body, []byte(`"tool_calls"`))) {
		return body
	}
	return editMessages(body, []string{"tool_calls", "tool_call_id"}, func(m map[string]json.RawMessage) bool {
		changed := false
		if raw, ok := m["tool_call_id"]; ok {
			var id string
			if json.Unmarshal(raw, &id) == nil {
				if own, sig := unsignedID(id); sig != "" {
					m["tool_call_id"], _ = marshalPlain(own)
					changed = true
				}
			}
		}
		var calls []map[string]json.RawMessage
		if json.Unmarshal(m["tool_calls"], &calls) != nil || len(calls) == 0 {
			return changed
		}
		sigs := make([]string, len(calls))
		signedStep := false
		for i, c := range calls {
			var id string
			json.Unmarshal(c["id"], &id)
			own, sig := unsignedID(id)
			if sig != "" {
				c["id"], _ = marshalPlain(own)
				changed = true
			}
			if s := googleSignature(c["extra_content"]); s != "" {
				sig = s
			}
			if sig != "" && (!keep || foreignSignature(sig, by)) {
				// another provider's, or one turned away: the call goes
				// as one this provider didn't sign
				sig = ""
				if googleSignature(c["extra_content"]) != "" {
					delete(c, "extra_content")
					changed = true
				}
			}
			sigs[i] = sig
			signedStep = signedStep || sig != ""
		}
		if gemini {
			for i, c := range calls {
				sig := sigs[i]
				if sig == "" && signedStep {
					continue // a later call of a signed step: Gemini signs the first
				}
				if sig == "" {
					sig = skipSignature
				}
				if googleSignature(c["extra_content"]) == sig {
					continue
				}
				c["extra_content"], _ = marshalPlain(googleExtra(sig))
				changed = true
			}
		}
		if changed {
			m["tool_calls"], _ = marshalPlain(calls)
		}
		return changed
	})
}

// sigTidy gives, in a Chat reply from Gemini relayed as it is, each tool
// call that came with a thought signature an id carrying it (signedID).
// Lines, or a whole reply, pass as they came unless one has a signature.
//
// Each signature is noted as provider by's (noteSigner). For a Gemini
// client's reply (native), which carries its signatures where Gemini
// gives them, the signatures are only noted, and the reply goes as it
// came.
type sigTidy struct {
	sse    bool
	by     string
	native bool
	buf    []byte
}

var thoughtSignatureField = []byte(`"thought_signature"`)

// nativeNoteBytes bounds the copy of a whole Gemini reply kept to note
// its signatures: one past it has none noted, and its signatures go back
// as before signers were kept.
const nativeNoteBytes = 8 << 20

func (t *sigTidy) write(b []byte) []byte {
	if t.native {
		// the reply goes on as it comes; what it signed is read off a
		// copy, by its lines on a stream, whole at the end otherwise
		if !t.sse && len(t.buf)+len(b) > nativeNoteBytes {
			t.buf, t.by = nil, ""
		}
		if t.by != "" {
			t.buf = append(t.buf, b...)
		}
		if t.sse {
			if i := bytes.LastIndexByte(t.buf, '\n'); i >= 0 {
				noteGeminiSigners(t.buf[:i+1], t.by)
				t.buf = append(t.buf[:0], t.buf[i+1:]...)
			}
		}
		return b
	}
	t.buf = append(t.buf, b...)
	if !t.sse {
		return nil
	}
	i := bytes.LastIndexByte(t.buf, '\n')
	if i < 0 {
		return nil
	}
	out := t.lines(t.buf[:i+1])
	t.buf = append(t.buf[:0], t.buf[i+1:]...)
	return out
}

func (t *sigTidy) flush() []byte {
	b := t.buf
	t.buf = nil
	if t.native {
		noteGeminiSigners(b, t.by)
		return nil
	}
	if !t.sse {
		if out, ok := signReply(b, t.by); ok {
			return out
		}
		return b
	}
	return t.lines(b)
}

func (t *sigTidy) lines(b []byte) []byte {
	if !bytes.Contains(b, thoughtSignatureField) {
		return append([]byte(nil), b...)
	}
	var out []byte
	for len(b) > 0 {
		line := b
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			line, b = b[:i+1], b[i+1:]
		} else {
			b = nil
		}
		body := bytes.TrimRight(line, "\r\n")
		data, ok := bytes.CutPrefix(body, []byte("data:"))
		if !ok || !bytes.Contains(data, thoughtSignatureField) {
			out = append(out, line...)
			continue
		}
		nb, ok := signReply(data, t.by)
		if !ok {
			out = append(out, line...)
			continue
		}
		out = append(append(append(out, "data: "...), nb...), line[len(body):]...)
	}
	return out
}

// signReply is a chunk, or a whole reply, with the signed calls' ids
// carrying their signatures, noted as provider by's, and whether there
// was one.
func signReply(data []byte, by string) ([]byte, bool) {
	if !bytes.Contains(data, thoughtSignatureField) {
		return nil, false
	}
	var reply map[string]json.RawMessage
	if json.Unmarshal(data, &reply) != nil {
		return nil, false
	}
	var choices []map[string]json.RawMessage
	if json.Unmarshal(reply["choices"], &choices) != nil {
		return nil, false
	}
	changed := false
	for _, c := range choices {
		for _, k := range []string{"delta", "message"} {
			var msg map[string]json.RawMessage
			if json.Unmarshal(c[k], &msg) != nil {
				continue
			}
			var calls []map[string]json.RawMessage
			if json.Unmarshal(msg["tool_calls"], &calls) != nil {
				continue
			}
			signed := false
			for _, call := range calls {
				sig := googleSignature(call["extra_content"])
				if sig == "" {
					continue
				}
				noteSigner(sig, by)
				var id string
				json.Unmarshal(call["id"], &id)
				if _, had := unsignedID(id); had != "" {
					continue
				}
				call["id"], _ = marshalPlain(signedID(id, sig))
				signed = true
			}
			if signed {
				msg["tool_calls"], _ = marshalPlain(calls)
				c[k], _ = marshalPlain(msg)
				changed = true
			}
		}
	}
	if !changed {
		return nil, false
	}
	reply["choices"], _ = marshalPlain(choices)
	out, err := marshalPlain(reply)
	if err != nil {
		return nil, false
	}
	return out, true
}

// A signature goes back only to the provider that gave it (mimi001234 on
// Discord: Pi on a group of Vertex AI and AI Studio; Vertex cooled down
// on turn 15 and AI Studio was sent Vertex's signatures, and answered
// every turn with 400 "Corrupted thought signature"). Google documents
// skip_thought_signature_validator for a function call it didn't sign
// ("when transferring history from a model that does not include thought
// signatures"), which a step's first call is given in place of another
// provider's signature; a text part's signature only helps, and goes.

// signersNoted is dec with each signature in its events noted as provider
// by's: a tool call's, which rides in its id (signedID), and a text's.
func signersNoted(dec func(string, func(Event)) error, by string) func(string, func(Event)) error {
	return func(data string, emit func(Event)) error {
		return dec(data, func(ev Event) {
			switch ev.Kind {
			case KToolStart:
				if _, sig := unsignedID(ev.ID); sig != "" {
					noteSigner(sig, by)
				}
			case KTextSig:
				noteSigner(ev.Text, by)
			}
			emit(ev)
		})
	}
}

// ownSignatures is req for provider by with no signature another provider
// gave on its calls and texts (foreignSignature). Messages are copied
// before they change.
func ownSignatures(req *Request, by string) *Request {
	return dropSignatures(req, func(sig string) bool { return foreignSignature(sig, by) })
}

// dropSignatures is req with the Gemini signatures drop says on its calls
// and texts taken off. Claude's signed thinking is no Gemini signature,
// and stays.
func dropSignatures(req *Request, drop func(sig string) bool) *Request {
	var msgs []Message
	for i, m := range req.Messages {
		var parts []Part
		for j, p := range m.Parts {
			if (p.Kind != ToolCall && p.Kind != Text) || p.Signature == "" || !drop(p.Signature) {
				continue
			}
			if parts == nil {
				parts = slices.Clone(m.Parts)
			}
			parts[j].Signature = ""
		}
		if parts == nil {
			continue
		}
		if msgs == nil {
			msgs = slices.Clone(req.Messages)
		}
		msgs[i].Parts = parts
	}
	if msgs == nil {
		return req
	}
	r := *req
	r.Messages = msgs
	return &r
}

// signatureRefused is Google turning a signature away as one it didn't
// give: Vertex AI's 400 "Invalid thought signature.", AI Studio's 400
// "Corrupted thought signature.". It is about the request's history, not
// the account: the request is asked again without signatures, and no
// account rests for it.
var signatureRefused = regexp.MustCompile(`(?i)\b(invalid|corrupted) thought signature`)

// signaturesReach says a request built for provider p on API to carries
// Gemini's signatures there: Gemini's own API (Vertex AI, AI Studio, Code
// Assist) or its OpenAI-compatible one.
func signaturesReach(p provider.Provider, to provider.Protocol, model string) bool {
	return to == provider.Gemini || to == provider.CodeAssist || p.IsVertex() || to == provider.Chat && geminiCompat(p.Host(), model)
}

var geminiSignatureField = []byte(`"thoughtSignature"`)

// noteGeminiSigners notes each thoughtSignature in a Gemini reply, a chunk
// or a whole one (an object, or streamGenerateContent's array of them),
// as provider by's.
func noteGeminiSigners(b []byte, by string) {
	if !bytes.Contains(b, geminiSignatureField) {
		return
	}
	note := func(r gjson.Result) {
		for _, c := range r.Get("candidates").Array() {
			for _, p := range c.Get("content.parts").Array() {
				noteSigner(p.Get("thoughtSignature").String(), by)
			}
		}
		for _, c := range r.Get("response.candidates").Array() {
			for _, p := range c.Get("content.parts").Array() {
				noteSigner(p.Get("thoughtSignature").String(), by)
			}
		}
	}
	all := func(r gjson.Result) {
		if r.IsArray() {
			r.ForEach(func(_, v gjson.Result) bool { note(v); return true })
		} else {
			note(r)
		}
	}
	if gjson.ValidBytes(b) {
		// a whole reply, however it is laid out
		all(gjson.ParseBytes(b))
		return
	}
	for len(b) > 0 {
		line := b
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			line, b = b[:i], b[i+1:]
		} else {
			b = nil
		}
		line = bytes.TrimSpace(line)
		if d, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			line = bytes.TrimSpace(d)
		}
		if !bytes.Contains(line, geminiSignatureField) {
			continue
		}
		all(gjson.ParseBytes(line))
	}
}

// geminiBodySignatures is a Gemini client's request relayed as it is to
// provider by with no signature another provider gave (foreignSignature),
// nor any when keep is false: a function call that had one goes waved
// through (skipSignature), a text without.
func geminiBodySignatures(body []byte, by string, keep bool) []byte {
	if !bytes.Contains(body, geminiSignatureField) {
		return body
	}
	var q map[string]json.RawMessage
	var contents []map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil || json.Unmarshal(q["contents"], &contents) != nil {
		return body
	}
	changed := false
	for _, c := range contents {
		var parts []map[string]json.RawMessage
		if json.Unmarshal(c["parts"], &parts) != nil {
			continue
		}
		edited := false
		for _, p := range parts {
			var sig string
			if json.Unmarshal(p["thoughtSignature"], &sig) != nil || sig == "" || sig == skipSignature {
				continue
			}
			if keep && !foreignSignature(sig, by) {
				continue
			}
			if _, call := p["functionCall"]; call {
				p["thoughtSignature"], _ = marshalPlain(skipSignature)
			} else {
				delete(p, "thoughtSignature")
			}
			edited = true
		}
		if edited {
			c["parts"], _ = marshalPlain(parts)
			changed = true
		}
	}
	if !changed {
		return body
	}
	q["contents"], _ = marshalPlain(contents)
	out, err := marshalPlain(q)
	if err != nil {
		return body
	}
	return out
}
