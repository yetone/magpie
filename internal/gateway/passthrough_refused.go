package gateway

import (
	"encoding/json"
	"strings"
	"sync/atomic"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
)

// Codex puts its own bookkeeping on each input item it sends OpenAI, in
// internal_chat_message_metadata_passthrough: the turn, when it was made,
// what kinds of content it holds. A newer Codex app adds a member to it
// before the ChatGPT backend takes it for every request: xuanwo on X got
// 400 "[ObjectParam] [input[55].internal_chat_message_metadata_passthrough.
// content_token_count] [unknown_parameter]" on every new conversation
// with two agents, a member no open-source Codex sends (openai/codex#52313
// was the same with content_item_metadata in 0.163.0-alpha.1). The member
// is Codex's attribution, not the conversation, so a request turned away
// over it is asked once more without it on every item, and the provider
// isn't sent it again.

const passthroughKey = "internal_chat_message_metadata_passthrough"

// passthroughRefusal is OpenAI's refusal of one member of the passthrough,
// or of the passthrough itself, as its path in the request: the member's
// name, empty for the whole passthrough.
var passthroughRefusal = lazyre.New(`input\[\d+\]\.` + passthroughKey + `(?:\.([A-Za-z0-9_]+))?\]?\s*\[unknown_parameter\]|(?i:unknown parameter):?\s*'input\[\d+\]\.` + passthroughKey + `(?:\.([A-Za-z0-9_]+))?'`)

// refusedPassthrough is the member msg says the upstream doesn't know,
// "" for the passthrough as a whole.
func refusedPassthrough(msg []byte) (field string, ok bool) {
	m := passthroughRefusal.FindSubmatch(wordsOf(msg))
	if m == nil {
		return "", false
	}
	if len(m[1]) > 0 {
		return string(m[1]), true
	}
	return string(m[2]), true
}

// passthroughRefused is how unfit remembers an upstream turning field
// away; "" is the passthrough as a whole.
func passthroughRefused(field string) string {
	if field == "" {
		return "input[]." + passthroughKey
	}
	return "input[]." + passthroughKey + "." + field
}

// withoutPassthrough is a Responses request with no item's passthrough
// holding field, or with no passthrough at all for "", every other byte as
// it was; false when no item had it.
func withoutPassthrough(body []byte, field string) ([]byte, bool) {
	return withoutPassthroughIf(body, func(f string) bool { return f == field })
}

// passthroughScans counts the bodies withoutPassthroughIf reads.
var passthroughScans atomic.Int64

// withoutPassthroughIf drops each passthrough member drop says, and the
// whole passthrough where drop("") does. It reads body once: each item
// is found by walking the input array, never looked up again from the
// start of the body, so a long conversation costs its size and not its
// size times its items (BigFish on Discord, #feedback).
func withoutPassthroughIf(body []byte, drop func(field string) bool) ([]byte, bool) {
	passthroughScans.Add(1)
	if !gjson.ValidBytes(body) {
		return body, false
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() || !placedIn(body, input) {
		return body, false
	}
	type cut struct {
		at, end int
		with    []byte
	}
	var cuts []cut
	whole := drop("")
	input.ForEach(func(_, item gjson.Result) bool {
		// ForEach and Get give offsets into body, since input's is one;
		// each is checked against body before it is cut
		if !item.IsObject() || !placedIn(body, item) {
			return true
		}
		pt := item.Get(passthroughKey)
		if !pt.Exists() || !placedIn(body, pt) {
			return true
		}
		if whole {
			var m map[string]json.RawMessage
			if json.Unmarshal([]byte(item.Raw), &m) != nil {
				return true
			}
			delete(m, passthroughKey)
			nb, err := json.Marshal(m)
			if err != nil {
				return true
			}
			cuts = append(cuts, cut{item.Index, item.Index + len(item.Raw), nb})
			return true
		}
		if !pt.IsObject() {
			return true
		}
		had := false
		pt.ForEach(func(k, _ gjson.Result) bool {
			had = k.String() != "" && drop(k.String())
			return !had
		})
		if !had {
			return true
		}
		// the members kept as they were written, in their order
		nb := []byte{'{'}
		pt.ForEach(func(k, v gjson.Result) bool {
			if k.String() != "" && drop(k.String()) {
				return true
			}
			if len(nb) > 1 {
				nb = append(nb, ',')
			}
			nb = append(append(append(nb, k.Raw...), ':'), v.Raw...)
			return true
		})
		nb = append(nb, '}')
		cuts = append(cuts, cut{pt.Index, pt.Index + len(pt.Raw), nb})
		return true
	})
	if len(cuts) == 0 {
		return body, false
	}
	// one copy, the cuts in the order they come
	n := len(body)
	for _, c := range cuts {
		n += len(c.with) - (c.end - c.at)
	}
	out := make([]byte, 0, n)
	from := 0
	for _, c := range cuts {
		out = append(append(out, body[from:c.at]...), c.with...)
		from = c.end
	}
	return append(out, body[from:]...), true
}

// placedIn is whether r's Index places its Raw in body, as a cut needs.
func placedIn(body []byte, r gjson.Result) bool {
	return r.Index > 0 && r.Index+len(r.Raw) <= len(body) && string(body[r.Index:r.Index+len(r.Raw)]) == r.Raw
}

// refusedPassthroughs is what of the passthrough providerID turned away
// on proto: all of it, or the members it named.
func (s *Server) refusedPassthroughs(providerID string, proto provider.Protocol) (whole bool, fields map[string]bool) {
	pre := providerID + "\x00" + passthroughRefused("")
	suf := "\x00" + string(proto)
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.unfit {
		if !v || len(k) < len(pre)+len(suf) || !strings.HasPrefix(k, pre) || !strings.HasSuffix(k, suf) {
			continue
		}
		switch f := k[len(pre) : len(k)-len(suf)]; {
		case f == "":
			whole = true
		case f[0] == '.' && len(f) > 1:
			if fields == nil {
				fields = map[string]bool{}
			}
			fields[f[1:]] = true
		}
	}
	return whole, fields
}

// withoutRefusedPassthrough is body without the passthrough members
// providerID turned away before. A provider that turned none away, as
// nearly every one, gets body as it came, unread.
func (s *Server) withoutRefusedPassthrough(providerID string, proto provider.Protocol, body []byte) []byte {
	if proto != provider.Responses {
		return body
	}
	whole, fields := s.refusedPassthroughs(providerID, proto)
	if !whole && len(fields) == 0 || !bodyHas(body, passthroughKey) {
		return body
	}
	if whole {
		b, _ := withoutPassthrough(body, "")
		return b
	}
	b, _ := withoutPassthroughIf(body, func(f string) bool { return fields[f] })
	return b
}
