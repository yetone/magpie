package gateway

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// grokToolPatterns is body with each "pattern" in its tools' schemas
// spelled so a Rust regex reads it as the ECMAScript regex JSON Schema
// says it is. xAI turns away a tool whose pattern Rust's regex syntax
// can't parse, and there a "[" inside a character class opens a nested
// class (and "&&", "--" and "~~" are set operations), where ECMAScript
// reads a plain character. A tool in a Claude Code session on Windows has
// "^[^\s[\]]+$" for a breakpoint's class_name and selector, so a Grok
// model was turned away on every turn: "Invalid request content: Schema
// validation failed: … pattern: "^[^\\s[\\]]+$" is not a "regex"" (#2317,
// grok-4.6 through a relay). Such a character is escaped instead, which is
// the same character to ECMAScript.
//
// It is Grok's whoever serves it (grokModel), the model named in the body
// as it goes upstream, or xAI's own API; every other model's tools go as
// they came.
func grokToolPatterns(p provider.Provider, to provider.Protocol, body []byte) []byte {
	// the cheap look first: most requests hold no pattern, and finding the
	// model walks the whole body when it comes after the conversation
	if !bodyHas(body, `"pattern"`) {
		return body
	}
	if p.VendorHost() != "api.x.ai" && !grokModel(gjson.GetBytes(body, "model").String()) {
		return body
	}
	return toolPatterns(body, ecmaClassForRust)
}

// ecmaClassForRust escapes, inside each character class of an ECMAScript
// pattern, a "[" and the second character of "&&", "--" and "~~", which a
// Rust regex would read as a nested class or a set operation. Escapes are
// kept as they are, and nothing outside a class changes.
func ecmaClassForRust(pat string) string {
	var b strings.Builder
	in := false
	for i := 0; i < len(pat); i++ {
		c := pat[i]
		switch {
		case c == '\\' && i+1 < len(pat):
			b.WriteByte(c)
			b.WriteByte(pat[i+1])
			i++
			continue
		case !in && c == '[':
			in = true
		case in && c == ']':
			in = false
		case in && c == '[':
			b.WriteString(`\[`)
			continue
		case in && (c == '&' || c == '-' || c == '~') && i+1 < len(pat) && pat[i+1] == c:
			b.WriteByte(c)
			b.WriteByte('\\')
			b.WriteByte(c)
			i++
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
