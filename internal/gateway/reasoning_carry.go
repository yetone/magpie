package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"slices"
	"strconv"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A Responses upstream's sealed reasoning across a client of another API
// (jenryai on X: Grok through Claude Desktop repeats itself and writes
// broken calls, where Grok Build doesn't). A model that reasons between
// tool calls reads the step's reasoning back on the next request: Grok
// Build sends every reasoning item it was given, encrypted_content and
// all, before the calls it led to. A Messages, Chat or Gemini client
// keeps no such item, so each step of its tool loop reached Grok with
// none of the reasoning that made the calls before it.
//
// So the reasoning items that come before a step's first call ride in
// that call's id, as Gemini's signatures do (gemini_signature.go): they
// are kept in magpie's folder (keepSignature, by their hash) and the id
// carries the key, <id>__rk16__<key>. Every client sends a call's id back
// as it was given, and on the way back the items go in front of the
// assistant turn that made the call, and the id is the call's own again.
// A key magpie no longer has (or an id a client shortened) is no
// reasoning: the step goes as before.

const (
	reasonMark = "__rk"
	reasonEnd  = "__"
)

// reasonKeyLen is a kept item's key's length in an id.
var reasonKeyLen = len(sigKey(""))

// reasonTail is what an id carrying reasoning ends with before its key.
var reasonTail = reasonMark + strconv.Itoa(reasonKeyLen) + reasonEnd

// asksSealedReasoning says whether a request from a client of from is
// made to ask p for its sealed reasoning, to carry it in the calls' ids:
// a Grok subscription's plugin, asked as Grok Build asks it. A Responses
// client asks for itself, and keeps the items it is given.
func asksSealedReasoning(p provider.Provider, from provider.Protocol) bool {
	return from != provider.Responses && p.PluginProvider() == "grok"
}

// askSealedReasoning adds reasoning.encrypted_content to r's include when
// asksSealedReasoning says so (buildResponses sends it only with reasoning).
func askSealedReasoning(p provider.Provider, from provider.Protocol, r *Request) {
	const v = "reasoning.encrypted_content"
	if asksSealedReasoning(p, from) && !slices.Contains(r.Include, v) {
		r.Include = append(slices.Clip(r.Include), v)
	}
}

// reasonedID is a call's id carrying the reasoning items before it, or the
// id as it was when they couldn't be kept.
func reasonedID(id string, items []json.RawMessage) string {
	if len(items) == 0 {
		return id
	}
	b, err := json.Marshal(items)
	if err != nil {
		return id
	}
	key := keepSignature(string(b))
	if key == "" {
		return id
	}
	if id == "" {
		id = "call_" + newID()
	}
	return id + reasonTail + key
}

// unreasonedID splits an id reasonedID made into the call's own id and
// the reasoning items it refers to (none when magpie no longer has them).
// Any other id comes back as it is.
func unreasonedID(id string) (string, []json.RawMessage) {
	at := len(id) - reasonKeyLen - len(reasonTail)
	if at < 1 || id[at:at+len(reasonTail)] != reasonTail {
		return id, nil
	}
	own, key := id[:at], id[at+len(reasonTail):]
	var items []json.RawMessage
	if kept := keptSignature(key); kept != "" {
		if json.Unmarshal([]byte(kept), &items) != nil {
			items = nil
		}
	}
	return own, items
}

// carryReasoning is dec for a client of from, with the sealed reasoning a
// Responses upstream gives before a call carried in that call's id, when
// the client is of another API.
func carryReasoning(dec func(string, func(Event)) error, from, actual provider.Protocol) func(string, func(Event)) error {
	if from == provider.Responses || actual != provider.Responses {
		return dec
	}
	var held []json.RawMessage
	return func(data string, emit func(Event)) error {
		return dec(data, func(ev Event) {
			switch {
			case ev.Kind == KSealed && ev.Name == sealResponses && ev.Text != "":
				held = append(held, json.RawMessage(ev.Text))
			case ev.Kind == KToolStart && len(held) > 0:
				ev.ID = reasonedID(ev.ID, held)
				held = nil
			}
			emit(ev)
		})
	}
}

// unreasonCalls takes the reasoning off a parsed request's call ids: each
// assistant turn leads with the items its calls carried, sealed as the
// Responses upstream gave them (buildResponses sends them back; another
// API's build leaves them out), and a call and its result have the call's
// own id. Messages are copied before they change.
func unreasonCalls(r *Request) {
	for i, m := range r.Messages {
		var parts, lead []Part
		for j, p := range m.Parts {
			var items []json.RawMessage
			switch p.Kind {
			case ToolCall:
				id, its := unreasonedID(p.ID)
				if id == p.ID {
					continue
				}
				p.ID, items = id, its
			case ToolResult:
				id, _ := unreasonedID(p.CallID)
				if id == p.CallID {
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
			for _, it := range items {
				lead = append(lead, Part{Kind: Thinking, Sealed: it, SealedBy: sealResponses})
			}
		}
		if parts != nil {
			r.Messages[i].Parts = append(lead, parts...)
		}
	}
}

// A carried item reaches whichever Responses upstream a conversation goes
// to next, not only the one that sealed it: after a fallback, a switch of
// account or a group's next turn, Codex refuses Grok's ("The encrypted
// content … could not be verified") and Grok Codex's ("Could not decrypt
// the provided encrypted_content"), and the call's id stays in the
// client's history, so every later turn carried the same item and was
// refused the same way (#4798, zane668: Pi's Chat to Grok, then Codex;
// ZanderH-code: Claude Desktop's Messages to Codex). A Responses client's
// own items are asked again without them (withoutSeals) and left out
// of the conversation's later requests there (withoutRefused); a carried
// item goes the same way: refused, the items the request carried are
// remembered as refused by that key or account in the conversation
// (refuseCarried), the request is asked again, and the translation leaves
// them out of this and every later request to it (withoutRefusedCarry).
// The calls, their results and the text stay as they were; the call's id
// is its own either way. Another key or account, the one that sealed it
// among them, is still given them.

// sealScopeKey holds a try's sealScope in its context.
type sealScopeKey struct{}

// sealScope is whose refusals of carried reasoning a try's translation
// reads: the conversation (stickKey), and the key or account
// (candidate.vendor) of the provider it goes to.
type sealScope struct {
	id, key, who string
}

// withSealScope is ctx for a try to provider id, as who, in the
// conversation key.
func withSealScope(ctx context.Context, id, key, who string) context.Context {
	return context.WithValue(ctx, sealScopeKey{}, sealScope{id: id, key: key, who: who})
}

// carriedSeal is the hash refusedSeals knows a carried reasoning part by:
// its item's encrypted_content, as seals hashes a Responses client's.
func carriedSeal(p Part) ([sha256.Size]byte, bool) {
	if !p.sealedBy(sealResponses) {
		return [sha256.Size]byte{}, false
	}
	var it sealedItem
	if decodeJSON(p.Sealed, &it) != nil || !sealedKinds[it.Type] || it.Enc == "" {
		return [sha256.Size]byte{}, false
	}
	return sha256.Sum256([]byte(it.Enc)), true
}

// withoutRefusedCarry leaves out of req the carried reasoning the key or
// account of ctx's try to p refused earlier in the conversation. Nothing
// else changes; a request with none goes as it is.
func withoutRefusedCarry(ctx context.Context, p provider.Provider, req *Request) {
	sc, ok := ctx.Value(sealScopeKey{}).(sealScope)
	if !ok || sc.id != p.ID {
		return
	}
	refusedSeals.Lock()
	r, ok := refusedSeals.m[sc.key+"|"+sc.who]
	refusedSeals.Unlock()
	if !ok || time.Since(r.at) > stickKeep {
		return
	}
	for i, m := range req.Messages {
		gone := func(p Part) bool {
			h, ok := carriedSeal(p)
			return ok && r.seals[h]
		}
		if slices.ContainsFunc(m.Parts, gone) {
			req.Messages[i].Parts = slices.DeleteFunc(slices.Clone(m.Parts), gone)
		}
	}
}

// refuseCarried remembers the reasoning body's calls carry, in a request
// from a client of from, as refused by who in the conversation key; true
// when any of it wasn't already, so that asking again leaves something
// out.
func refuseCarried(key, who string, from provider.Protocol, body []byte) bool {
	if from == provider.Responses || !bodyHas(body, reasonTail) {
		return false
	}
	req, err := parseRequest(from, body, false, true)
	if err != nil {
		return false
	}
	var ss [][sha256.Size]byte
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			if h, ok := carriedSeal(p); ok {
				ss = append(ss, h)
			}
		}
	}
	k := key + "|" + who
	refusedSeals.Lock()
	r := refusedSeals.m[k]
	fresh := false
	for _, h := range ss {
		if !r.seals[h] || time.Since(r.at) > stickKeep {
			fresh = true
		}
	}
	refusedSeals.Unlock()
	if !fresh {
		return false
	}
	noteRefused(key, who, ss)
	return true
}
