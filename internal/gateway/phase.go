package gateway

import (
	"bytes"
	"encoding/json"

	"github.com/tidwall/gjson"
)

// An assistant message in a Responses reply says its phase, as OpenAI's
// own models' do: "commentary" for what the model said on the way (before
// a call), "final_answer" for the answer that ends the turn. Codex folds a
// finished turn's commentary and calls under "Worked for …" and leaves the
// final answer out, and reads a message with no phase as an answer, so a
// model magpie translates had every progress line left unfolded (#2437).
//
// A message's phase is known only from what comes after it: a call, a
// search or another message makes it commentary; the reply ending with
// nothing but reasoning after it makes it the final answer. A reply cut
// short (incomplete, failed, cut mid-stream) guesses no final answer, and
// a phase the upstream wrote is kept.

const (
	phaseCommentary = "commentary"
	phaseFinal      = "final_answer"
)

// heldMost is how many events may wait behind a message whose phase isn't
// known yet; past it they go on, and the message without a phase.
const heldMost = 512

// unphased reports whether an output item is an assistant message with no
// phase of its own.
func unphased(it map[string]any) bool {
	if it == nil || it["type"] != "message" {
		return false
	}
	if r, ok := it["role"]; ok && r != "assistant" {
		return false
	}
	p, _ := it["phase"].(string)
	return p == ""
}

// anyUnphased reports whether a reply's output has an item unphased
// would give a phase, read in place: a reply whose messages all have one
// (OpenAI's own) goes as it came without being decoded whole.
func anyUnphased(output gjson.Result) bool {
	if !output.IsArray() {
		return false
	}
	found := false
	output.ForEach(func(_, it gjson.Result) bool {
		typ, role, phase := it.Get("type"), it.Get("role"), it.Get("phase")
		if typ.Type != gjson.String || typ.Str != "message" {
			return true
		}
		if role.Exists() && (role.Type != gjson.String || role.Str != "assistant") {
			return true
		}
		found = phase.Type != gjson.String || phase.Str == ""
		return !found
	})
	return found
}

// phaseOutput gives each assistant message in a reply's output its phase,
// and reports whether it gave any. done is whether the reply ended
// completed: only then is the last message the final answer.
func phaseOutput(output []any, done bool) bool {
	last := -1 // the last item that isn't reasoning
	for i, it := range output {
		if m, _ := it.(map[string]any); m != nil && m["type"] != "reasoning" {
			last = i
		}
	}
	changed := false
	for i, it := range output {
		m, _ := it.(map[string]any)
		if !unphased(m) {
			continue
		}
		switch {
		case i < last:
			m["phase"] = phaseCommentary
		case i == last && done:
			m["phase"] = phaseFinal
		default:
			continue
		}
		changed = true
	}
	return changed
}

// phaseTidy gives, in a relayed Responses reply, its assistant messages a
// phase where the upstream wrote none, and their output_text parts the
// annotations list an upstream left out (output_text_shape.go). In a stream a message's
// output_item.done waits, with what comes after it, until the next item
// that isn't reasoning (commentary) or the reply's end (final answer); a
// reply that isn't a stream is held whole and gone through at the end.
type phaseTidy struct {
	sse  bool
	buf  []byte   // a stream's part of an event, or a whole reply
	held [][]byte // events waiting on the first's message's phase
}

func (t *phaseTidy) write(b []byte) []byte {
	t.buf = append(t.buf, b...)
	if !t.sse {
		return nil
	}
	out, rest := tidyEvents(t.buf, t.event)
	t.buf = rest
	return out
}

// tidyEvents is each whole event in buf through event, and buf's part of
// an event that hasn't ended, moved to buf's front so the next write
// appends into the same bytes. What event gives may be its ev, a slice of
// buf, so it is copied out before buf is written again.
func tidyEvents(buf []byte, event func(ev []byte) []byte) (out, rest []byte) {
	at := 0
	for {
		n := eventEnd(buf[at:])
		if n < 0 {
			break
		}
		if out == nil {
			out = make([]byte, 0, len(buf))
		}
		out = append(out, event(buf[at:at+n])...)
		at += n
	}
	return out, append(buf[:0], buf[at:]...)
}

func (t *phaseTidy) flush() []byte {
	b := t.buf
	t.buf = nil
	if !t.sse {
		if out, ok := phaseWhole(b); ok {
			return out
		}
		return b
	}
	// a stream that ended with a message still waiting: nothing says it
	// ended the turn, so it goes as it came
	out := t.release("")
	return append(out, b...)
}

// eventData is an event's data, joined as SSE joins it. An event of one
// data line, as nearly every upstream's is, gets ev's own bytes, so what
// it gives is only read.
func eventData(ev []byte) []byte {
	var data, first []byte
	n := 0
	for rest := ev; len(rest) > 0; {
		var line []byte
		line, rest, _ = bytes.Cut(rest, []byte("\n"))
		d, ok := bytes.CutPrefix(bytes.TrimRight(line, "\r"), []byte("data:"))
		if !ok {
			continue
		}
		d = bytes.TrimPrefix(d, []byte(" "))
		switch n {
		case 0:
			first = d
		case 1:
			data = append(append(append([]byte(nil), first...), '\n'), d...)
		default:
			data = append(append(data, '\n'), d...)
		}
		n++
	}
	if n == 1 {
		return first
	}
	return data
}

// withData is ev with its data lines put back as data.
func withData(ev, data []byte) []byte {
	var out []byte
	put := false
	for _, line := range bytes.SplitAfter(ev, []byte("\n")) {
		body := bytes.TrimRight(line, "\r\n")
		if bytes.HasPrefix(body, []byte("data:")) {
			if !put {
				out = append(append(append(out, "data: "...), data...), line[len(body):]...)
				put = true
			}
			continue
		}
		out = append(out, line...)
	}
	return out
}

func (t *phaseTidy) event(ev []byte) []byte {
	data := eventData(ev)
	typ := gjson.GetBytes(data, "type").String()
	if nb, ok := annotatedEvent(typ, data); ok {
		ev, data = withData(ev, nb), nb
	}
	if t.held != nil {
		switch typ {
		case "response.output_item.added":
			if gjson.GetBytes(data, "item.type").String() == "reasoning" {
				return t.hold(ev)
			}
			return append(t.release(phaseCommentary), t.event(ev)...)
		case "response.completed":
			return append(t.release(phaseFinal), t.ended(ev, data, true)...)
		case "response.incomplete", "response.failed", "error":
			return append(t.release(""), t.ended(ev, data, false)...)
		}
		return t.hold(ev)
	}
	switch typ {
	case "response.output_item.done":
		it := gjson.GetBytes(data, "item")
		if it.Get("type").String() == "message" && it.Get("role").String() != "user" && it.Get("phase").String() == "" {
			t.held = [][]byte{append([]byte(nil), ev...)}
			return nil
		}
	case "response.completed":
		return t.ended(ev, data, true)
	case "response.incomplete":
		return t.ended(ev, data, false)
	}
	return ev
}

func (t *phaseTidy) hold(ev []byte) []byte {
	t.held = append(t.held, append([]byte(nil), ev...))
	if len(t.held) > heldMost {
		return t.release("")
	}
	return nil
}

// release is the waiting events, the message among them given phase (or
// left as it came, for "").
func (t *phaseTidy) release(phase string) []byte {
	held := t.held
	t.held = nil
	if len(held) == 0 {
		return nil
	}
	if phase != "" {
		if nb, ok := phasedEvent(eventData(held[0]), phase); ok {
			held[0] = withData(held[0], nb)
		}
	}
	return bytes.Join(held, nil)
}

// ended is the reply's last event, its output's messages given their phase
// as the stream gave them.
func (t *phaseTidy) ended(ev, data []byte, done bool) []byte {
	if output := gjson.GetBytes(data, "response.output"); !anyUnphased(output) && !anyBareText(output) {
		return ev
	}
	var e map[string]any
	if decodeNumbers(data, &e) != nil {
		return ev
	}
	res, _ := e["response"].(map[string]any)
	out, _ := res["output"].([]any)
	phased := phaseOutput(out, done)
	if !annotateOutput(out) && !phased {
		return ev
	}
	nb, err := marshalPlain(e)
	if err != nil {
		return ev
	}
	return withData(ev, nb)
}

// phasedEvent is an output_item.done event with its message given phase.
func phasedEvent(data []byte, phase string) ([]byte, bool) {
	var e map[string]any
	if decodeNumbers(data, &e) != nil {
		return nil, false
	}
	it, _ := e["item"].(map[string]any)
	if !unphased(it) {
		return nil, false
	}
	it["phase"] = phase
	nb, err := marshalPlain(e)
	return nb, err == nil
}

// phaseWhole is a whole Responses reply with its messages' phases and
// their parts' annotations.
func phaseWhole(b []byte) ([]byte, bool) {
	var res map[string]any
	if decodeNumbers(b, &res) != nil || res == nil || res["object"] != "response" {
		return nil, false
	}
	out, _ := res["output"].([]any)
	phased := phaseOutput(out, res["status"] == "completed")
	if !annotateOutput(out) && !phased {
		return nil, false
	}
	nb, err := marshalPlain(res)
	return nb, err == nil
}

func decodeNumbers(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	return dec.Decode(v)
}
