package provider

// A provider's extraBody (Luc-xz, #1604): JSON fields the user adds to the
// body of every request magpie sends the provider — a relay that turns a
// Responses request away without a prompt_cache_key, say — as Headers adds
// headers. They are merged at the top level of the body as it goes out,
// whichever API it is on (Chat, Responses, Messages, Gemini) and whether it
// was relayed or translated (gateway.forwardOnce):
//
//   - A field the body has already — the client's own, or what magpie put
//     there for it — is kept: the user's value fills in what is missing.
//   - A name written with a "!" before it ("!prompt_cache_key") is forced:
//     it replaces the body's own.
//   - ${sessionId} in a string value, at any depth, is the conversation's
//     id: the session the agent names (its session header), else one
//     magpie derives from how the conversation opens (the gateway's
//     conversationID), the same for every turn of it. A fixed string would put every session on one
//     cache key. ${threadId} is the thread's (conversation.go); Headers
//     take both too.
//   - ${subagentId} in a string value is the Claude Code subagent the
//     request is of (x-claude-code-agent-id), "" on the conversation's
//     own turns, which never send the header. "${sessionId}:${subagentId}"
//     gives a relay one prompt_cache_key per subagent, where ${sessionId}
//     alone carries every subagent's turns on one key (yetone/magpie#2937).
//     Headers take it too.
//   - The fields that carry the conversation, or that magpie must decide
//     for routing and streaming to work (ExtraBodyReserved), can't be set:
//     a save naming one is refused, and one written into the file by hand
//     is left out.
//
// The editor's Test and Detect send them too, on their own session
// (tested), so a relay that turns a request away without them still
// answers its Test. A request that only counts tokens gets none of them,
// nor one to another API (embeddings, images, video, a decision API's):
// the fields are for asking the model for a reply. Neither does a
// signed-in account's: its body is the agent's own, as its headers are —
// except a ChatGPT (Codex) account's, whose requests magpie builds as Codex
// CLI would (codexBody), and which take fields Codex CLI adds by choice
// (access_programs, OpenAI's Daybreak, magpie-releases#18). On it the
// fields codexBody decides for the backend to answer at all — not stored,
// tool_choice auto, its reasoning carried — can't be set either
// (codexOwned).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// ExtraBody is a provider's extra body fields, each value as the user
// wrote it, a name that begins with "!" forced.
type ExtraBody map[string]json.RawMessage

// UnmarshalJSON reads an object; anything else written by hand (a string,
// a list) is read as none, rather than failing the whole providers file.
func (e *ExtraBody) UnmarshalJSON(b []byte) error {
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		*e = nil
		return nil
	}
	*e = m
	return nil
}

// ExtraBodyReserved are the fields extraBody can't set: the model and
// whether to stream, which magpie decides for routing and reads the reply
// by, and the conversation itself — its messages, input or contents, its
// system prompt and instructions, its tools.
var ExtraBodyReserved = []string{"model", "stream", "messages", "input", "contents", "system", "systemInstruction", "system_instruction", "instructions", "tools"}

// codexOwned are the fields of a ChatGPT account's request that extraBody
// can't set beside ExtraBodyReserved: codexBody sets them on every request
// as Codex CLI does, and the backend turns a request away without them
// (store true, a request that keeps nothing without its sealed reasoning).
var codexOwned = []string{"store", "tool_choice", "include"}

// extraBodyTakes says p's requests take extraBody's fields, and which
// fields beside ExtraBodyReserved it can't set.
func (p Provider) extraBodyTakes() (owned []string, ok bool) {
	switch {
	case p.Account == nil:
		return nil, true
	case p.Account.Agent == "codex":
		return codexOwned, true
	}
	return nil, false
}

// SessionPlaceholder is what a string value of extraBody writes for the
// conversation's id.
const SessionPlaceholder = "${sessionId}"

// extraName is a field's name without its "!", and whether it is forced.
func extraName(k string) (string, bool) {
	n, forced := strings.CutPrefix(strings.TrimSpace(k), "!")
	return strings.TrimSpace(n), forced
}

// ExtraBodyOK says what is wrong with an extraBody, "" when nothing is.
func ExtraBodyOK(e ExtraBody) string {
	seen := map[string]bool{}
	for _, k := range slices.Sorted(maps.Keys(e)) {
		n, _ := extraName(k)
		switch {
		case n == "":
			return "an extra body field needs a name"
		case slices.Contains(ExtraBodyReserved, n):
			return fmt.Sprintf("extra body can't set %q: magpie sets the model, stream and the conversation itself", n)
		case seen[n]:
			return fmt.Sprintf("extra body names %q twice", n)
		case !json.Valid(e[k]):
			return fmt.Sprintf("extra body's %q is not JSON", n)
		}
		seen[n] = true
	}
	return ""
}

// cleanExtraBody drops what can't be sent (ExtraBodyOK's faults, for a
// file written by hand), and is nil when nothing is left.
func cleanExtraBody(e ExtraBody) ExtraBody {
	out := ExtraBody{}
	seen := map[string]bool{}
	for _, k := range slices.Sorted(maps.Keys(e)) {
		n, forced := extraName(k)
		if n == "" || slices.Contains(ExtraBodyReserved, n) || seen[n] || !json.Valid(e[k]) {
			continue
		}
		seen[n] = true
		v := bytes.TrimSpace(e[k])
		if forced {
			out["!"+n] = slices.Clone(v)
		} else {
			out[n] = slices.Clone(v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// WithExtraBody is body with the provider's extra fields merged in, as the
// package's comment says; c is the conversation for ${sessionId} and
// ${threadId}, its Subagent for ${subagentId}. A body that isn't a JSON
// object goes as it came.
func (p Provider) WithExtraBody(body []byte, c Conversation) []byte {
	owned, takes := p.extraBodyTakes()
	if len(p.ExtraBody) == 0 || !takes {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return body
	}
	changed := false
	for k, raw := range p.ExtraBody {
		n, forced := extraName(k)
		if n == "" || slices.Contains(ExtraBodyReserved, n) || slices.Contains(owned, n) {
			continue
		}
		if _, has := m[n]; has && !forced {
			continue
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var v any
		if d.Decode(&v) != nil {
			continue
		}
		m[n], changed = withSession(v, c), true
	}
	if !changed {
		return body
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false) // <, > and & as the agent wrote them
	if enc.Encode(m) != nil {
		return body
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n"))
}

// withSession is v with ${sessionId}, ${threadId} and ${subagentId} in its
// strings, at any depth, as c's ids.
func withSession(v any, c Conversation) any {
	switch x := v.(type) {
	case string:
		return c.expand(x)
	case map[string]any:
		for k, e := range x {
			x[k] = withSession(e, c)
		}
	case []any:
		for i, e := range x {
			x[i] = withSession(e, c)
		}
	}
	return v
}

// tested is a test's or a detection's tiny request body for p as it goes
// out: the extra fields, on a session of the test's own, then what the
// backend adjusts; ctx carries the same session, for the headers Sign
// puts on it. The conversation names no subagent, so ${subagentId} in an
// extra body value goes out filled with "". A body for an embeddings or
// rerank url (tinyRetrieval) goes as it is, as the gateway sends one.
func (p Provider) tested(ctx context.Context, url, body string) (context.Context, []byte) {
	c := OwnConversation("magpie-test-")
	ctx = WithConversation(ctx, c)
	if strings.HasSuffix(url, "/embeddings") || strings.HasSuffix(url, "/rerank") {
		return ctx, []byte(body)
	}
	return ctx, p.Prepare(p.WithExtraBody([]byte(body), c))
}
