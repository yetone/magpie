package gateway

import (
	"bytes"
	"encoding/json"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
)

// Claude Code begins its system prompt with a billing line, a block of its
// own: x-anthropic-billing-header: cc_version=…; cc_entrypoint=…; cch=…;
// whose cch changes every turn. It is for Anthropic; any other vendor's
// model reads it as the prompt's first words, so its prefix cache
// (DeepSeek's, behind Command Code's plan) missed from the first byte on
// every turn: 4% of a day's input read from the cache, against 96–99%
// without the line (#1600, justrun1983). It goes on only to a Claude model
// asked on Anthropic's Messages API (Anthropic's own, a relay's, Bedrock's),
// as Claude Code sent it. The Claude subscription's run sends its own
// (withoutBillingHeader), another magpie decides for the provider it picks,
// and Code Assist is sent it as before (antigravityTurnsAway, #666).

// billingLine is a system block that is the billing line and nothing else.
var billingLine = lazyre.New(`^x-anthropic-billing-header:(\s*[A-Za-z_]+=[^;\s]*;)*\s*$`)

// keepsBillingLine says whether a request for model (wire, the vendor's name
// for it) asked on to keeps Claude Code's billing line: a Claude model on
// Anthropic's Messages API.
func keepsBillingLine(to provider.Protocol, model, wire string) bool {
	return to == provider.Anthropic && (anthropicModel.MatchString(model) || anthropicModel.MatchString(wire))
}

// dropsBillingLine says whether p's request for model asked on to goes
// without the billing line.
func dropsBillingLine(p provider.Provider, to provider.Protocol, model, wire string) bool {
	return !p.IsRemoteMagpie() && to != provider.CodeAssist && !keepsBillingLine(to, model, wire)
}

// withoutBillingLine is an Anthropic Messages body without Claude Code's
// billing line in its system: a block that is the line is left out, and the
// line at the start of the first block or of a system string is cut from
// it. Everything else goes as it was sent, byte for byte; a body without
// the line is body itself.
func withoutBillingLine(body []byte) []byte {
	if !bodyHas(body, "x-anthropic-billing-header:") {
		return body
	}
	sys := gjson.GetBytes(body, "system")
	if sys.Index <= 0 {
		return body
	}
	var out []byte
	switch {
	case sys.Type == gjson.String:
		cut := withoutBillingHeader(sys.Str)
		if cut == sys.Str {
			return body
		}
		out = billingJSON(cut)
	case sys.IsArray():
		var blocks []json.RawMessage
		if json.Unmarshal([]byte(sys.Raw), &blocks) != nil {
			return body
		}
		kept := make([][]byte, 0, len(blocks))
		changed := false
		for i, b := range blocks {
			text := gjson.GetBytes(b, "text")
			if gjson.GetBytes(b, "type").Str != "text" || text.Type != gjson.String {
				kept = append(kept, b)
				continue
			}
			if billingLine.MatchString(text.Str) {
				changed = true
				continue
			}
			if i == 0 {
				if cut := withoutBillingHeader(text.Str); cut != text.Str && text.Index > 0 {
					b = append(append(append([]byte(nil), b[:text.Index]...), billingJSON(cut)...), b[text.Index+len(text.Raw):]...)
					changed = true
				}
			}
			kept = append(kept, b)
		}
		if !changed {
			return body
		}
		if len(kept) == 0 {
			// a system that was the line alone: none
			return withoutFields(body, "system")
		}
		out = append([]byte("["), bytes.Join(kept, []byte(","))...)
		out = append(out, ']')
	default:
		return body
	}
	res := make([]byte, 0, len(body))
	res = append(res, body[:sys.Index]...)
	res = append(res, out...)
	return append(res, body[sys.Index+len(sys.Raw):]...)
}

// billingJSON is s as a JSON string, with <, > and & as they are.
func billingJSON(s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}
