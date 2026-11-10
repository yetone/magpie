package gateway

import (
	"bytes"
	"encoding/json"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
)

// OpenCode 2 (its @ai-sdk/openai-compatible) puts "strict": false on every
// function tool it offers on Chat, and Codex and the AI SDK's Responses
// client do on Responses. false is the field's default, but an upstream
// that validates Chat strictly turns the whole request away for it
// ("Unsupported Chat Completions API field: tools[0].function.strict",
// #1980, thonatos's custom provider). Such an upstream is asked once more
// with strict left out of its tools, and so from then on; one that takes
// it still gets it as sent.

// toolStrictRefusal is an upstream refusing the strict field of a request's
// tools: the field's path (tools[0].function.strict, tools.0.strict, a
// segment in between as a relay may mask it) said unsupported, unknown or
// not allowed, before or after it, or Pydantic's extra_forbidden located
// there. An invalid value or a schema strict mode can't take is a
// different fault and doesn't match.
var toolStrictRefusal = lazyre.New(`(?i)(?:unsupported|unknown|unrecognized|not (?:supported|allowed|permitted)|extra (?:fields?|inputs?|parameters?)|forbidden)[^\n{}]{0,120}?\btools(?:\[\d+\]|\.\d+)(?:\.[^\s.'"\x60]+)?\.strict\b|\btools(?:\[\d+\]|\.\d+)(?:\.[^\s.'"\x60]+)?\.strict\b['"\x60]?[^\n{}]{0,40}?(?:unsupported|unknown|unrecognized|not (?:supported|allowed|permitted)|extra_forbidden|forbidden)|"loc"\s*:\s*\[\s*(?:"body"\s*,\s*)?"tools"\s*,\s*\d+\s*,\s*(?:"function"\s*,\s*)?"strict"\s*\][^{}]*"type"\s*:\s*"extra_forbidden"|"type"\s*:\s*"extra_forbidden"[^{}]*"loc"\s*:\s*\[\s*(?:"body"\s*,\s*)?"tools"\s*,\s*\d+\s*,\s*(?:"function"\s*,\s*)?"strict"\s*\]`)

// toolStrictRefused is how unfit remembers a provider refusing it: the
// provider's validator, not a model, turns it away.
const toolStrictRefused = "tools[].strict"

// withoutToolStrict is a Chat or Responses body with strict left out of
// its function tools (a Responses namespace's included), and whether any
// was.
func withoutToolStrict(proto provider.Protocol, body []byte) ([]byte, bool) {
	if (proto != provider.Chat && proto != provider.Responses) || !bytes.Contains(body, []byte(`"strict"`)) {
		return body, false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return body, false
	}
	tools, _ := m["tools"].([]any)
	if !dropToolStrict(proto, tools) {
		return body, false
	}
	out, err := marshalPlain(m)
	if err != nil {
		return body, false
	}
	return out, true
}

// dropToolStrict deletes strict from the function tools among tools and
// reports whether it deleted any.
func dropToolStrict(proto provider.Protocol, tools []any) bool {
	changed := false
	for _, t := range tools {
		tm, _ := t.(map[string]any)
		if tm == nil {
			continue
		}
		if proto == provider.Responses && tm["type"] == "namespace" {
			nested, _ := tm["tools"].([]any)
			if dropToolStrict(proto, nested) {
				changed = true
			}
			continue
		}
		fn := tm
		if proto == provider.Chat {
			fn, _ = tm["function"].(map[string]any)
		}
		if _, ok := fn["strict"]; ok {
			delete(fn, "strict")
			changed = true
		}
	}
	return changed
}
