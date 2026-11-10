package gateway

import (
	"bytes"
	"encoding/json"
	"strconv"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/lazyre"
)

// Codex offers custom tools on Responses (exec in code mode, apply_patch):
// a tool of type custom whose call carries free-form input. A vendor's
// Responses API may take none of them, or only some: DeepSeek's answers
// 400 "Unsupported custom tool: 'exec'. Only 'apply_patch' is supported."
// to a request that only offers exec (#2656). A relay sends the tools as
// they came, so such a request is translated instead, on the same API:
// the translation offers each custom tool as a function taking its input
// (customSchema) and gives a call to one back as the custom_tool_call
// Codex runs. Once the model has turned them away it is translated from
// the start.

// customToolRefusal is an upstream turning a custom tool away: DeepSeek's
// "Unsupported custom tool: 'exec'", or the type named unsupported — as
// xAI names it, Rust's own wording, "unknown variant `custom`" (#3392). A
// refusal of anything else about a tool doesn't match.
var customToolRefusal = lazyre.New(`(?i)unsupported custom tool|custom tools? (?:is |are )?not (?:supported|allowed)|(?:unsupported|unknown|invalid) tool type[^\n{}]{0,20}?['"\x60]custom['"\x60]|tool type ['"\x60]custom['"\x60] (?:is )?(?:not supported|unsupported)|unknown variant[^\n{}]{0,20}?['"\x60]custom['"\x60]`)

// customToolRefused is how unfit remembers model turning custom tools
// away.
func customToolRefused(model string) string { return "custom tools\x00" + model }

// hasCustomTool reports whether a Responses body offers a custom tool:
// among its tools, in a namespace there, or in an input item that carries
// tools (Responses Lite's additional_tools, a tool search's output).
func hasCustomTool(body []byte) bool {
	if !bytes.Contains(body, []byte(`"custom"`)) {
		return false
	}
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return false
	}
	if customIn(m["tools"]) {
		return true
	}
	items, _ := m["input"].([]any)
	for _, it := range items {
		if im, _ := it.(map[string]any); im != nil && customIn(im["tools"]) {
			return true
		}
	}
	return false
}

// customIn reports whether a list of tools has a custom one, a namespace's
// included.
func customIn(v any) bool {
	tools, _ := v.([]any)
	for _, t := range tools {
		tm, _ := t.(map[string]any)
		if tm == nil {
			continue
		}
		if tm["type"] == "custom" || tm["type"] == "namespace" && customIn(tm["tools"]) {
			return true
		}
	}
	return false
}

// customStream gives a custom tool's input as its call's arguments come,
// a piece at a time: the text of the input string in {"input":"…"} (or of
// a lone JSON string), unescaped, as the custom_tool_call_input deltas a
// client reads while the model writes a patch. What can't be read yet (an
// escape or a character cut off) waits for the next piece. The finished
// item's input is still customInput's of the whole arguments; arguments
// of another shape stream nothing.
type customStream struct {
	pend  []byte // of the input string, read and not given yet
	depth int
	str   bool   // in a string that isn't the input
	esc   bool   // after its backslash
	key   []byte // the depth-1 key being read, raw
	inKey bool   // the string being read is that key
	want  bool   // a key is next, at depth 1
	last  string // the last depth-1 key
	value bool   // the depth-1 value next is the input's
	in    bool   // in the input string
	over  bool   // the input is given, or there is none
}

// feed reads the next piece of the arguments and gives what it adds to
// the input.
func (c *customStream) feed(s string) string {
	if c.over {
		return ""
	}
	if c.in {
		c.pend = append(c.pend, s...)
		return c.take()
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		switch {
		case c.str:
			if c.inKey {
				c.key = append(c.key, b)
			}
			switch {
			case c.esc:
				c.esc = false
			case b == '\\':
				c.esc = true
			case b == '"':
				c.str = false
				if c.inKey {
					c.inKey = false
					c.last = ""
					json.Unmarshal(append([]byte{'"'}, c.key...), &c.last)
				}
			}
		case b == '"' && (c.depth == 0 || c.depth == 1 && c.value):
			c.in = true
			c.pend = append(c.pend, s[i+1:]...)
			return c.take()
		case b == '"':
			c.str = true
			if c.depth == 1 && c.want {
				c.inKey, c.key, c.want = true, c.key[:0], false
			}
		case b == '{' || b == '[':
			c.depth++
			if c.depth == 1 && b == '{' {
				c.want = true
			}
		case b == '}' || b == ']':
			if c.depth--; c.depth <= 0 {
				c.over = true
				return ""
			}
		case b == ':' && c.depth == 1:
			c.value = c.last == "input"
		case b == ',' && c.depth == 1:
			c.want, c.value = true, false
		case b == ' ' || b == '\t' || b == '\n' || b == '\r':
		case c.depth == 0:
			c.over = true // not JSON this reads
			return ""
		case c.depth == 1:
			c.value = false // the input isn't a string
		}
	}
	return ""
}

// take unescapes what can be read of the input string so far, up to its
// closing quote.
func (c *customStream) take() string {
	n, end := 0, false
scan:
	for n < len(c.pend) {
		switch c.pend[n] {
		case '"':
			end = true
			break scan
		case '\\':
			switch {
			case n+1 >= len(c.pend):
				break scan
			case c.pend[n+1] != 'u':
				n += 2
			case n+6 > len(c.pend):
				break scan
			case !highSurrogate(c.pend[n+2 : n+6]):
				n += 6
			case n+8 > len(c.pend):
				break scan
			case c.pend[n+6] != '\\' || c.pend[n+7] != 'u':
				n += 6 // a lone half, read as JSON reads it
			case n+12 > len(c.pend):
				break scan // the low half is cut off
			default:
				n += 12
			}
		default:
			n++
		}
	}
	// a character cut off waits too
	for k := 0; k < utf8.UTFMax-1 && n > 0 && !end && !utf8.Valid(c.pend[:n]); k++ {
		n--
	}
	var text string
	if json.Unmarshal(append(append([]byte{'"'}, c.pend[:n]...), '"'), &text) != nil {
		c.over = true // not a string JSON reads: the finished item says it
		return ""
	}
	c.pend = c.pend[n:]
	if end {
		c.over, c.pend = true, nil
	}
	return text
}

// highSurrogate is whether the four hex digits of a \u escape are the
// first half of a pair.
func highSurrogate(hex []byte) bool {
	r, err := strconv.ParseUint(string(hex), 16, 16)
	return err == nil && r >= 0xD800 && r < 0xDC00
}
