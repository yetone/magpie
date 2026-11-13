package gateway

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// Models write a whole number in a tool call's arguments with a fraction
// of zeros ("yield_time_ms":2500.0, "session_id":17277.0), whoever serves
// them: Grok's do it on every turn (#1431, 11 of 11 turns on grok-4.6 and
// grok-4.7 through a relay), and muse-spark wrote tool_search's limit as
// 8.0 through the gateway (#3152). Codex reads such fields as u64, usize,
// i32 or i64 (exec_command, write_stdin, shell's timeout_ms, sleep,
// tool_search's limit, wait), though their schemas say "number", and serde
// turns the whole call away: "failed to parse function arguments: invalid
// type: floating point `2500.0`, expected u64". Any client that types its
// arguments strictly does the same.
//
// In a reply, a number in a call's arguments written with a fraction of
// zeros and no exponent loses that fraction: 2500.0 is written 2500. JSON
// has one kind of number, so 2500 is the same value to every client, and
// one that took 2500.0 takes 2500; a tool's schema isn't asked, since
// Codex's own say "number" for the integers it reads. Strings, keys, other
// fractions (2.5) and exponents (2.5e3) stay as written.
//
// The rule is applied to the arguments as a stream of text, so it gives
// the same arguments whether they come whole or in deltas cut anywhere:
// what the deltas of a call add up to is what its done events say.

// grokModel is whether id, the model as magpie or its upstream names it, is
// one of Grok's, under whatever vendor prefix a relay or router gives it
// (xai/grok-4.5, x-ai/grok-4). Grok's schema quirks (grok_schema.go) are
// still Grok's alone; whole floats in calls' arguments are every model's.
func grokModel(id string) bool {
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	return strings.HasPrefix(strings.ToLower(id), "grok")
}

// wholeNums drops the zero fraction of each number in a stream of JSON
// text, fed to it a piece at a time. It holds only a fraction of zeros
// (".0", ".00") until it knows whether the number ends there, in which
// case the fraction is dropped, or goes on (2500.05, 2500.0e3), in which
// case it is written as it came; so a piece never waits on the next one
// for anything but such a fraction, and nothing is held at the end.
type wholeNums struct {
	str, esc bool // in a string, after its backslash
	num      bool // in a number
	asIs     bool // the rest of this number goes as written
	zeros    int  // a fraction of zeros held: its dot, then its zeros
}

func (n *wholeNums) feed(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if n.num {
			switch {
			case n.zeros > 0 && c == '0':
				n.zeros++
				continue
			case n.zeros > 0 && (c >= '1' && c <= '9' || c == 'e' || c == 'E'):
				// not a fraction of zeros: what was held goes as it came
				b.WriteByte('.')
				b.WriteString(strings.Repeat("0", n.zeros-1))
				n.zeros, n.asIs = 0, true
				b.WriteByte(c)
				continue
			case n.zeros == 0 && c == '.' && !n.asIs:
				n.zeros = 1
				continue
			case n.zeros == 0 && (c >= '0' && c <= '9' || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-'):
				if c == 'e' || c == 'E' || c == '.' {
					n.asIs = true
				}
				b.WriteByte(c)
				continue
			}
			// the number ended: a fraction of zeros held is dropped
			n.num, n.asIs, n.zeros = false, false, 0
		}
		switch {
		case n.str:
			if n.esc {
				n.esc = false
			} else if c == '\\' {
				n.esc = true
			} else if c == '"' {
				n.str = false
			}
		case c == '"':
			n.str = true
		case c == '-' || c >= '0' && c <= '9':
			n.num = true
		}
		b.WriteByte(c)
	}
	return b.String()
}

// wholeArgs is a call's whole arguments with the zero fraction of each of
// their numbers dropped.
func wholeArgs(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	var n wholeNums
	return n.feed(s)
}

// wholeEncoder hands its encoder a reply with the zero fractions dropped
// from its calls' arguments, streamed in pieces.
type wholeEncoder struct {
	streamEncoder
	args wholeNums
}

func (e *wholeEncoder) event(ev Event) {
	switch ev.Kind {
	case KToolStart:
		e.args = wholeNums{}
	case KToolArgs:
		if ev.Text = e.args.feed(ev.Text); ev.Text == "" {
			return // held: a fraction of zeros the next piece may end
		}
	}
	e.streamEncoder.event(ev)
}

// wholeResult is a whole reply with its calls' arguments' zero fractions
// dropped.
func wholeResult(res Result) Result {
	var parts []Part
	for i, p := range res.Parts {
		if p.Kind != ToolCall || len(p.Args) == 0 {
			continue
		}
		if a := wholeArgs(string(p.Args)); a != string(p.Args) {
			if parts == nil {
				parts = append([]Part(nil), res.Parts...)
			}
			parts[i].Args = json.RawMessage(a)
		}
	}
	if parts != nil {
		res.Parts = parts
	}
	return res
}

// intTidy drops, in a reply relayed as it came, the zero fractions of its
// calls' arguments: Responses' function_call items and argument deltas,
// Chat's tool_calls, and Messages' tool_use input. A stream's lines pass
// as they came unless one carries arguments that change; a reply that
// isn't one is held whole and gone through at the end.
type intTidy struct {
	proto provider.Protocol
	sse   bool
	buf   []byte
	calls map[string]*wholeNums // a streamed call's arguments so far, by its index or item id
}

func (t *intTidy) write(b []byte) []byte {
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

func (t *intTidy) flush() []byte {
	b := t.buf
	t.buf = nil
	if !t.sse {
		// every model's whole reply comes here now: one with no call in
		// it is said as it came, without being decoded
		if !t.carries(b) {
			return b
		}
		if out, ok := t.event(b); ok {
			return out
		}
		return b
	}
	return t.lines(b)
}

// carries is whether b may carry a call's arguments.
func (t *intTidy) carries(b []byte) bool {
	switch t.proto {
	case provider.Responses:
		return bodyHas(b, `function_call`)
	case provider.Chat:
		return bodyHas(b, `"tool_calls"`)
	case provider.Anthropic:
		return bodyHas(b, `"tool_use"`) || bodyHas(b, `"input_json_delta"`)
	}
	return false
}

func (t *intTidy) lines(b []byte) []byte {
	if !t.carries(b) {
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
		if !ok || !t.carries(data) {
			out = append(out, line...)
			continue
		}
		nb, ok := t.event(data)
		if !ok {
			out = append(out, line...)
			continue
		}
		out = append(append(append(out, "data: "...), nb...), line[len(body):]...)
	}
	return out
}

// event is an event, or a whole reply, with its calls' arguments' zero
// fractions dropped, and whether that changed it.
func (t *intTidy) event(data []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber() // the event's own numbers as they came
	var ev map[string]any
	if dec.Decode(&ev) != nil || ev == nil {
		return nil, false
	}
	var changed bool
	switch t.proto {
	case provider.Responses:
		changed = t.responses(ev)
	case provider.Chat:
		changed = t.chat(ev)
	case provider.Anthropic:
		changed = t.anthropic(ev)
	}
	if !changed {
		return nil, false
	}
	nb, err := marshalPlain(ev)
	if err != nil {
		return nil, false
	}
	return nb, true
}

// stream is the arguments so far of the streamed call at key.
func (t *intTidy) stream(key string) *wholeNums {
	if t.calls == nil {
		t.calls = map[string]*wholeNums{}
	}
	n := t.calls[key]
	if n == nil {
		n = &wholeNums{}
		t.calls[key] = n
	}
	return n
}

// set writes m[k], a string of arguments, through f, and says whether it
// changed.
func set(m map[string]any, k string, f func(string) string) bool {
	s, ok := m[k].(string)
	if !ok {
		return false
	}
	if out := f(s); out != s {
		m[k] = out
		return true
	}
	return false
}

func (t *intTidy) responses(ev map[string]any) bool {
	changed := false
	item := func(it map[string]any) {
		if it != nil && it["type"] == "function_call" && set(it, "arguments", wholeArgs) {
			changed = true
		}
	}
	it, _ := ev["item"].(map[string]any)
	item(it)
	switch ev["type"] {
	case "response.function_call_arguments.delta":
		key := callKeyOf(ev)
		if set(ev, "delta", t.stream(key).feed) {
			changed = true
		}
	case "response.function_call_arguments.done":
		delete(t.calls, callKeyOf(ev))
		if set(ev, "arguments", wholeArgs) {
			changed = true
		}
	}
	res, _ := ev["response"].(map[string]any)
	if res == nil && ev["object"] == "response" {
		res = ev // a whole reply
	}
	if res != nil {
		out, _ := res["output"].([]any)
		for _, o := range out {
			im, _ := o.(map[string]any)
			item(im)
		}
	}
	return changed
}

// callKeyOf is the key a Responses argument event's call is streamed under:
// its item id, or where it is in the output.
func callKeyOf(ev map[string]any) string {
	if id, _ := ev["item_id"].(string); id != "" {
		return id
	}
	return "#" + jsonText(ev["output_index"])
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (t *intTidy) chat(ev map[string]any) bool {
	changed := false
	choices, _ := ev["choices"].([]any)
	for _, c := range choices {
		cm, _ := c.(map[string]any)
		if cm == nil {
			continue
		}
		// a stream's delta, its arguments in pieces by the call's index
		if d, _ := cm["delta"].(map[string]any); d != nil {
			calls, _ := d["tool_calls"].([]any)
			for j, tc := range calls {
				tm, _ := tc.(map[string]any)
				fn, _ := tm["function"].(map[string]any)
				if fn == nil {
					continue
				}
				idx := jsonText(tm["index"])
				if tm["index"] == nil {
					idx = strconv.Itoa(j)
				}
				if set(fn, "arguments", t.stream(jsonText(cm["index"])+"/"+idx).feed) {
					changed = true
				}
			}
		}
		// a whole reply's message
		if m, _ := cm["message"].(map[string]any); m != nil {
			calls, _ := m["tool_calls"].([]any)
			for _, tc := range calls {
				tm, _ := tc.(map[string]any)
				if fn, _ := tm["function"].(map[string]any); fn != nil && set(fn, "arguments", wholeArgs) {
					changed = true
				}
			}
		}
	}
	return changed
}

func (t *intTidy) anthropic(ev map[string]any) bool {
	changed := false
	// a whole reply's tool_use blocks, their input an object
	block := func(b map[string]any) {
		if b == nil || b["type"] != "tool_use" || b["input"] == nil {
			return
		}
		in, err := marshalPlain(b["input"])
		if err != nil {
			return
		}
		if out := wholeArgs(string(in)); out != string(in) {
			b["input"] = json.RawMessage(out)
			changed = true
		}
	}
	content, _ := ev["content"].([]any)
	for _, c := range content {
		cm, _ := c.(map[string]any)
		block(cm)
	}
	cb, _ := ev["content_block"].(map[string]any)
	block(cb)
	// a stream's input, in pieces by the block's index
	if ev["type"] == "content_block_delta" {
		if d, _ := ev["delta"].(map[string]any); d != nil && d["type"] == "input_json_delta" {
			if set(d, "partial_json", t.stream(jsonText(ev["index"])).feed) {
				changed = true
			}
		}
	}
	return changed
}
