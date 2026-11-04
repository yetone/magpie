package gateway

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// Some replicas of GLM's flash models stitch the fragments of a streamed
// tool call's arguments with CR: each JSON \r escape in the text is a CR
// they put at a token boundary (yetone/magpie#4256), and the client reads
// one broken line a CR. An argument's text says a line ending as the pair
// \r\n, which stays; a \n or a \t stands for itself; a \\r is a backslash
// and an r, no CR at all. Every other \r escape goes, with the little
// content that carries a lone CR of its own losing it, which is the
// cure's cost. The cure runs only on the routes the defect was seen on:
// another model's reply goes as it came, healthy or not.

// glmFlash is whether id, the model as magpie or its upstream names it,
// is one of those flash models, under whatever vendor prefix a relay or
// router gives it.
func glmFlash(id string) bool {
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	return strings.HasPrefix(strings.ToLower(id), "glm-5.3-flash")
}

// glmServed is whether a request for model on p goes to one of those
// models, by magpie's id for it or the name it is sent upstream under.
func glmServed(wires map[string]string, p provider.Provider, model string) bool {
	return glmFlash(model) || glmFlash(provider.UpstreamNameIn(wires, p.ID, model))
}

// crDropper takes a stream of an argument's text, a piece at a time, and
// gives it back without the \r escapes, keeping them the pair \r\n begins.
// An escape cut between two pieces waits in pend for the next one.
type crDropper struct {
	pend string
}

func (d *crDropper) write(s string) string {
	if d.pend != "" {
		s, d.pend = d.pend+s, ""
	}
	return d.scan(s, false)
}

// flush gives back what is pending at the end of the stream: a trailing
// \r escape is alone (nothing follows to pair with it), and goes; a
// trailing backslash is said as it came.
func (d *crDropper) flush() string {
	s := d.pend
	d.pend = ""
	return d.scan(s, true)
}

func (d *crDropper) scan(s string, final bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if c := s[i]; c != '\\' {
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 >= len(s) {
			if final {
				b.WriteByte(s[i])
				i++
				continue
			}
			d.pend = s[i:] // the next piece says what the escape is
			return b.String()
		}
		if s[i+1] == 'r' {
			if i+4 <= len(s) {
				if s[i+2] == '\\' && s[i+3] == 'n' {
					b.WriteString(s[i : i+4]) // \r\n: a line ending, kept
					i += 4
				} else {
					i += 2 // a CR stitched between fragments goes
				}
				continue
			}
			if final { // nothing after it: alone
				i += 2
				continue
			}
			d.pend = s[i:] // whether a \n follows is the next piece's to tell
			return b.String()
		}
		b.WriteByte(s[i])
		b.WriteByte(s[i+1])
		i += 2
	}
	return b.String()
}

// dropCR is a call's whole arguments text with the stitched CR gone, for
// the replies said at once: the text arrives complete, so the dropper's
// whole stream is one piece.
func dropCR(s string) string {
	var d crDropper
	return d.write(s) + d.flush()
}

// crBytes takes a custom tool's raw input text, a piece at a time, and
// gives it back without the lone CR bytes: a CRLF pair is the line ending
// it says, and stays. A patch carries real bytes, not JSON escapes, so
// the same cure reads them as bytes; a backslash and an r are two of the
// text's own characters and stand.
type crBytes struct {
	pend string
}

func (d *crBytes) write(s string) string {
	if d.pend != "" {
		s, d.pend = d.pend+s, ""
	}
	return d.scan(s, false)
}

// flush gives back what is pending at the end of the stream: a trailing
// CR is alone, and goes.
func (d *crBytes) flush() string {
	s := d.pend
	d.pend = ""
	return d.scan(s, true)
}

func (d *crBytes) scan(s string, final bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\r' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) {
			if s[i+1] == '\n' {
				b.WriteString("\r\n") // a line ending, kept
				i++
			} // else a CR stitched between fragments goes
			continue
		}
		if final { // alone at the end: gone
			continue
		}
		d.pend = s[i:] // whether an LF follows is the next piece's to tell
		return b.String()
	}
	return b.String()
}

// dropRawCR is a custom tool's whole input text with the lone CR bytes
// gone.
func dropRawCR(s string) string {
	var d crBytes
	return d.write(s) + d.flush()
}

// crEncoder hands its encoder one of those models' reply with the stitched
// CR of its calls' arguments dropped, streamed in pieces. An escape cut at
// a piece's end waits in the dropper for the next, so the events that end
// a call say what it holds: a \r that turned out alone is already gone,
// and a bare \ is said as it came.
type crEncoder struct {
	streamEncoder
	args crDropper
}

func (e *crEncoder) event(ev Event) {
	switch ev.Kind {
	case KToolArgs:
		if ev.Text = e.args.write(ev.Text); ev.Text == "" {
			return // held: what the next piece may finish
		}
	case KToolStart:
		e.say(e.args.flush()) // the last call's tail, before this one opens
		e.args = crDropper{}
	case KStop:
		e.say(e.args.flush())
	}
	e.streamEncoder.event(ev)
}

// say gives a call's held tail to its encoder: say gets "" for a tail
// that turned out to be a lone \r, and says anything else as it came.
func (e *crEncoder) say(tail string) {
	if tail != "" {
		e.streamEncoder.event(Event{Kind: KToolArgs, Text: tail})
	}
}

// crResult is a whole reply from one of those models with the stitched CR
// of its calls' arguments dropped.
func crResult(res Result) Result {
	var parts []Part
	for i, p := range res.Parts {
		if p.Kind != ToolCall || len(p.Args) == 0 {
			continue
		}
		if a := dropCR(string(p.Args)); a != string(p.Args) {
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

// crSeq is the two bytes every JSON \r escape begins with.
var crSeq = []byte{'\\', 'r'}

// crTidy drops, in one of those models' reply relayed as it came, the
// stitched CR of its calls' arguments: Responses' function_call items and
// argument deltas (a custom tool's input the same way), Chat's tool_calls,
// and Messages' tool_use input. Which events carry arguments is read from
// the event's own type or shape, never from a text's words. A stream's
// lines pass as they came unless one carries the escape or answers a cut
// one; a reply that isn't a stream is held whole and gone through at the
// end. A line's end is looked for in the new bytes of a read alone, and a
// region without the escape comes back as it came, so a healthy reply
// pays a scan and a copy it was going to pay anyway.
type crTidy struct {
	proto provider.Protocol
	sse   bool
	buf   []byte
	calls map[string]*crDropper // a streamed call's cut escape, by its index or item id
	texts map[string]*crBytes   // a custom tool's input, by its item id
	// looked is how many bytes were searched for a line's end or the
	// escape, what a test bounds by the stream's length (#5282)
	looked int
}

func (t *crTidy) write(b []byte) []byte {
	if !t.sse {
		t.buf = append(t.buf, b...)
		return nil
	}
	old := len(t.buf)
	t.buf = append(t.buf, b...)
	// the bytes before these were searched for a line's end when they came
	t.looked += len(b)
	i := bytes.LastIndexByte(b, '\n')
	if i < 0 {
		return nil
	}
	end := old + i + 1
	// what a line's end leaves waits where the said lines were, so a
	// region the filter did not change is said from a copy of itself
	// then; when nothing waits it is said from where it lies
	out := t.lines(t.buf[:end], end < len(t.buf))
	t.buf = append(t.buf[:0], t.buf[end:]...)
	return out
}

func (t *crTidy) flush() []byte {
	b := t.buf
	t.buf, t.calls, t.texts = nil, nil, nil
	if !t.sse {
		if nb, ok := t.event(b); ok {
			return nb
		}
		return b
	}
	return t.lines(b, false)
}

// cut is whether a call's text waits for the piece that says what its cut
// escape is: the lines of that call must be read even when they carry no
// escape of their own.
func (t *crTidy) cut() bool {
	for _, d := range t.calls {
		if d.pend != "" {
			return true
		}
	}
	for _, d := range t.texts {
		if d.pend != "" {
			return true
		}
	}
	return false
}

// lines is a region's lines, each said once. copy says whether the region
// still has a tail waiting behind it, in whose room an unchanged region
// would not survive being said from where it lies.
func (t *crTidy) lines(b []byte, copy bool) []byte {
	t.looked += len(b)
	if !bytes.Contains(b, crSeq) && !t.cut() {
		if copy {
			return append([]byte(nil), b...)
		}
		return b
	}
	var out []byte
	rest := b
	for len(rest) > 0 {
		line := rest
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			line, rest = rest[:i+1], rest[i+1:]
		} else {
			rest = nil
		}
		nb, changed := t.line(line)
		if !changed {
			if out != nil {
				out = append(out, line...)
			}
			continue
		}
		if out == nil { // the lines before the first changed one, as they came
			out = append([]byte(nil), b[:len(b)-len(rest)-len(line)]...)
		}
		out = append(out, nb...)
	}
	if out == nil {
		if copy {
			return append([]byte(nil), b...)
		}
		return b
	}
	return out
}

// line is the line, its ending kept, with the stitched CR of the arguments
// its event carries gone. A line with none of the escape, and no cut one
// to answer, goes as it came.
func (t *crTidy) line(l []byte) ([]byte, bool) {
	t.looked += len(l)
	if !bytes.Contains(l, crSeq) && !t.cut() {
		return l, false
	}
	body := bytes.TrimRight(l, "\r\n")
	data, ok := bytes.CutPrefix(body, []byte("data:"))
	if !ok {
		return l, false
	}
	nb, ok := t.event(bytes.TrimSpace(data))
	if !ok {
		return l, false
	}
	return append(append([]byte("data: "), nb...), l[len(body):]...), true
}

// event is an event, or a reply that came whole, with its calls'
// arguments' stitched CR dropped, and whether that changed it.
func (t *crTidy) event(data []byte) ([]byte, bool) {
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

// stream is the state of the streamed call at key, made on first sight.
func (t *crTidy) stream(key string) *crDropper {
	if t.calls == nil {
		t.calls = map[string]*crDropper{}
	}
	d := t.calls[key]
	if d == nil {
		d = &crDropper{}
		t.calls[key] = d
	}
	return d
}

// rawText is the same for a custom tool's input text.
func (t *crTidy) rawText(key string) *crBytes {
	if t.texts == nil {
		t.texts = map[string]*crBytes{}
	}
	d := t.texts[key]
	if d == nil {
		d = &crBytes{}
		t.texts[key] = d
	}
	return d
}

func (t *crTidy) responses(ev map[string]any) bool {
	changed := false
	// a call said whole: a function call's arguments are JSON text, a
	// custom tool's input the raw text
	item := func(it map[string]any) {
		if it == nil {
			return
		}
		switch it["type"] {
		case "function_call":
			changed = set(it, "arguments", dropCR) || changed
		case "custom_tool_call":
			changed = set(it, "input", dropRawCR) || changed
		}
	}
	it, _ := ev["item"].(map[string]any)
	item(it)
	// the event's own type says what its delta carries: a call's
	// fragments travel here, a text or reasoning delta never
	switch ev["type"] {
	case "response.function_call_arguments.delta":
		if set(ev, "delta", t.stream(callKeyOf(ev)).write) {
			changed = true
		}
	case "response.custom_tool_call_input.delta":
		if set(ev, "delta", t.rawText(callKeyOf(ev)).write) {
			changed = true
		}
	case "response.function_call_arguments.done":
		delete(t.calls, callKeyOf(ev))
		if set(ev, "arguments", dropCR) {
			changed = true
		}
	case "response.custom_tool_call_input.done":
		delete(t.texts, callKeyOf(ev))
		if set(ev, "input", dropRawCR) {
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

func (t *crTidy) chat(ev map[string]any) bool {
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
				if set(fn, "arguments", t.stream(jsonText(cm["index"])+"/"+idx).write) {
					changed = true
				}
			}
		}
		// a whole reply's message
		if m, _ := cm["message"].(map[string]any); m != nil {
			calls, _ := m["tool_calls"].([]any)
			for _, tc := range calls {
				tm, _ := tc.(map[string]any)
				if fn, _ := tm["function"].(map[string]any); fn != nil && set(fn, "arguments", dropCR) {
					changed = true
				}
			}
		}
	}
	return changed
}

func (t *crTidy) anthropic(ev map[string]any) bool {
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
		if out := dropCR(string(in)); out != string(in) {
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
			if set(d, "partial_json", t.stream(jsonText(ev["index"])).write) {
				changed = true
			}
		}
	}
	// the block's end: a cut escape waited for a piece that never comes.
	// A \r it held was alone, and its going is the cure; a bare \ the
	// client never saw said, which only malformed arguments reach.
	if ev["type"] == "content_block_stop" {
		delete(t.calls, jsonText(ev["index"]))
	}
	return changed
}
