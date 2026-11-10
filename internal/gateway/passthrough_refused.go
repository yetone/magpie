package gateway

import (
	"bytes"
	"encoding/json"
	"strconv"

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

// withoutPassthroughIf drops each passthrough member drop says, and the
// whole passthrough where drop("") does.
func withoutPassthroughIf(body []byte, drop func(field string) bool) ([]byte, bool) {
	if !gjson.ValidBytes(body) {
		return body, false
	}
	n := int(gjson.GetBytes(body, "input.#").Int())
	type cut struct {
		at, end int
		with    []byte
	}
	var cuts []cut
	for i := 0; i < n; i++ {
		item := gjson.GetBytes(body, "input."+strconv.Itoa(i))
		pt := gjson.GetBytes(body, "input."+strconv.Itoa(i)+"."+passthroughKey)
		if !pt.Exists() || pt.Index <= 0 || item.Index <= 0 {
			continue
		}
		if drop("") {
			var m map[string]json.RawMessage
			if json.Unmarshal([]byte(item.Raw), &m) != nil {
				continue
			}
			delete(m, passthroughKey)
			nb, err := json.Marshal(m)
			if err != nil {
				continue
			}
			cuts = append(cuts, cut{item.Index, item.Index + len(item.Raw), nb})
			continue
		}
		if !pt.IsObject() {
			continue
		}
		// the members kept as they were written, in their order
		nb := []byte{'{'}
		had := false
		pt.ForEach(func(k, v gjson.Result) bool {
			if k.String() != "" && drop(k.String()) {
				had = true
				return true
			}
			if len(nb) > 1 {
				nb = append(nb, ',')
			}
			nb = append(append(append(nb, k.Raw...), ':'), v.Raw...)
			return true
		})
		if !had {
			continue
		}
		nb = append(nb, '}')
		cuts = append(cuts, cut{pt.Index, pt.Index + len(pt.Raw), nb})
	}
	if len(cuts) == 0 {
		return body, false
	}
	// from the last, so each earlier cut's offsets still hold
	out := body
	for i := len(cuts) - 1; i >= 0; i-- {
		c := cuts[i]
		nb := make([]byte, 0, len(out)-(c.end-c.at)+len(c.with))
		nb = append(nb, out[:c.at]...)
		nb = append(nb, c.with...)
		out = append(nb, out[c.end:]...)
	}
	return out, true
}

// withoutRefusedPassthrough is body without the passthrough members
// providerID turned away before.
func (s *Server) withoutRefusedPassthrough(providerID string, proto provider.Protocol, body []byte) []byte {
	if proto != provider.Responses || !bytes.Contains(body, []byte(passthroughKey)) {
		return body
	}
	if !s.fits(providerID, passthroughRefused(""), proto) {
		b, _ := withoutPassthrough(body, "")
		return b
	}
	b, _ := withoutPassthroughIf(body, func(f string) bool {
		return f != "" && !s.fits(providerID, passthroughRefused(f), proto)
	})
	return b
}
