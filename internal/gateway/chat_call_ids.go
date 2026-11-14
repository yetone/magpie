package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
)

// chatCallIDs gives every tool call of a Chat request that came with no
// id ("", null or none) one, and the tool messages after it that answer
// with no tool_call_id that same id, in the calls' order (#2611,
// RunMintOn). An upstream that sometimes leaves a streamed call's id out
// (OpenCode Zen's free DeepSeek) has the agent keep the call as "", which
// that upstream takes back; a strict one (WorkBuddy AI, 400 code 11133 on
// a streamed request) turns every later request of the session away. The
// id comes from the call's place in the request and what it says, never
// at random, so the next turn sends the same history byte for byte and
// the upstream's prompt cache still holds. It runs in attempt for every
// Chat request, relayed or translated, a plugin's included. A body whose
// calls all have ids comes back byte for byte.
func chatCallIDs(body []byte) []byte {
	if callIDsSet(body) {
		return body
	}
	return giveCallIDs(body)
}

// giveCallIDs is chatCallIDs past its check, decoding every message.
func giveCallIDs(body []byte) []byte {
	at := -1
	var open []string // the latest call turn's made ids no result has taken yet
	return editMessages(body, []string{"tool_calls"}, func(m map[string]json.RawMessage) bool {
		at++
		var role string
		decodeJSON(m["role"], &role)
		switch role {
		case "assistant":
			var calls []map[string]json.RawMessage
			if decodeJSON(m["tool_calls"], &calls) != nil || len(calls) == 0 {
				return false
			}
			open = nil
			for i, c := range calls {
				if callID(c["id"]) != "" {
					continue
				}
				var fn struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				}
				decodeJSON(c["function"], &fn)
				id := placedCallID(at, i, fn.Name, string(fn.Arguments))
				c["id"], _ = marshalPlain(id)
				open = append(open, id)
			}
			if open == nil {
				return false
			}
			m["tool_calls"], _ = marshalPlain(calls)
			return true
		case "tool":
			if len(open) == 0 || callID(m["tool_call_id"]) != "" {
				return false
			}
			m["tool_call_id"], _ = marshalPlain(open[0])
			open = open[1:]
			return true
		}
		return false
	})
}

// callID reads a call's id or a result's tool_call_id: "" for none, null
// or anything but a string.
func callID(raw json.RawMessage) string {
	var id string
	decodeJSON(raw, &id)
	return id
}

// placedCallID is the id chatCallIDs gives the i-th call of the message at
// msg: the same for the same call at the same place on every request.
func placedCallID(msg, i int, name, args string) string {
	sum := sha256.Sum256([]byte(strconv.Itoa(msg) + "." + strconv.Itoa(i) + "\x00" + name + "\x00" + args))
	return "call_" + hex.EncodeToString(sum[:12])
}

// streamCallIDs is what chatTidy keeps to give a streamed tool call the
// upstream sent no id for one: the calls seen, by choice and index.
type streamCallIDs map[[2]int]bool

// mend gives the first fragment of each call of a delta's tool_calls
// that has no id one, and leaves an empty id out of a later fragment,
// where a client that takes each field a fragment carries would write ""
// over it. It reports whether it changed any.
func (s streamCallIDs) mend(choice int, raw json.RawMessage) (json.RawMessage, bool) {
	var calls []map[string]json.RawMessage
	if decodeJSON(raw, &calls) != nil {
		return raw, false
	}
	changed := false
	for k, c := range calls {
		idx := k
		decodeJSON(c["index"], &idx)
		key := [2]int{choice, idx}
		idRaw, has := c["id"]
		if !s[key] {
			s[key] = true
			if callID(idRaw) == "" {
				c["id"], _ = marshalPlain("call_" + newID())
				changed = true
			}
		} else if has && callID(idRaw) == "" {
			delete(c, "id")
			changed = true
		}
	}
	if !changed {
		return raw, false
	}
	out, err := json.Marshal(calls)
	if err != nil {
		return raw, false
	}
	return out, true
}

// wholeCallIDs gives each tool call of a whole reply's message that came
// with no id one, and reports whether it did.
func wholeCallIDs(msg map[string]json.RawMessage) bool {
	var calls []map[string]json.RawMessage
	if decodeJSON(msg["tool_calls"], &calls) != nil {
		return false
	}
	changed := false
	for _, c := range calls {
		if callID(c["id"]) == "" {
			c["id"], _ = marshalPlain("call_" + newID())
			changed = true
		}
	}
	if changed {
		msg["tool_calls"], _ = marshalPlain(calls)
	}
	return changed
}

// wholeNameless drops the tool calls of a whole reply's message whose
// function names nothing in a JSON string — no name, null, another kind
// (#1475) — and reports whether any was dropped.
func wholeNameless(msg map[string]json.RawMessage) bool {
	var calls []map[string]json.RawMessage
	if decodeJSON(msg["tool_calls"], &calls) != nil {
		return false
	}
	var kept []map[string]json.RawMessage
	for _, c := range calls {
		var fn map[string]json.RawMessage
		if decodeJSON(c["function"], &fn) == nil && aName(fn["name"]) {
			kept = append(kept, c)
		}
	}
	if len(kept) == len(calls) {
		return false
	}
	if kept == nil {
		delete(msg, "tool_calls")
		return true
	}
	msg["tool_calls"], _ = marshalPlain(kept)
	return true
}

// wholeNamedCall says whether a whole reply's message still carries a
// tool call whose function names itself in a string.
func wholeNamedCall(msg map[string]json.RawMessage) bool {
	var calls []map[string]json.RawMessage
	if decodeJSON(msg["tool_calls"], &calls) != nil {
		return false
	}
	for _, c := range calls {
		var fn map[string]json.RawMessage
		if decodeJSON(c["function"], &fn) == nil && aName(fn["name"]) {
			return true
		}
	}
	return false
}

// callIDsSet says, without decoding body, that chatCallIDs has nothing to
// give: every call has a non-empty string id, so no tool message after
// one is given an id either. False is "maybe": the decode decides.
// Nearly every request with tools is so, and decoding each message to
// see was ~17% of a 2 MB Chat request's CPU.
func callIDsSet(body []byte) bool {
	msgs, found, valid := topMember(body, "messages")
	if !valid || !found || len(msgs) == 0 || msgs[0] != '[' {
		return !found && valid
	}
	set := func(v []byte) bool {
		s, ok := stringBytes(v)
		return ok && len(s) > 0
	}
	all := true
	eachElement(msgs, func(el []byte) bool {
		if el[0] != '{' {
			all = false
			return false
		}
		var calls []byte
		eachMember(el, func(k, v []byte) bool {
			if keyIs(k, "tool_calls") {
				calls = v
			}
			return true
		})
		if len(calls) == 0 || string(calls) == "null" {
			return true
		}
		if calls[0] != '[' {
			all = false
			return false
		}
		eachElement(calls, func(c []byte) bool {
			if c[0] != '{' {
				all = false
				return false
			}
			var id []byte
			eachMember(c, func(k, v []byte) bool {
				if keyIs(k, "id") {
					id = v
				}
				return true
			})
			all = set(id)
			return all
		})
		return all
	})
	return all
}
