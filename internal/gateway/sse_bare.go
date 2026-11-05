package gateway

import (
	"bytes"
	"encoding/json"
	"io"
)

// An upstream's event stream can end with its error written as a JSON
// object on a line of its own, no "data:" before it: Antigravity's Code
// Assist answered gemini-4-argon-high with a 200 stream, a few thinking
// events, then
//
//	{"error":{"code":400,"message":"Request contains an invalid argument.","status":"INVALID_ARGUMENT"}}
//
// and the end of the body (zRain on Discord). SSE has no such field, so
// every reader skipped the line, and the reply was told as one that
// "ended before it was complete" instead of the vendor's 400 (a request
// fault, not the account's). bareErrors hands such a line on as the data
// event it means, for every reader of the stream: the translated routes'
// decoders, a relayed stream's sniffer and held-stream scan, and the
// client a stream is relayed to as it came.

// bareLimit is how much of a bare JSON value is held while its lines come:
// past it, it is no error object, and goes on as it came.
const bareLimit = 1 << 20

// bareErrors is r with every line that is a JSON error object of its own
// (bareError) written as a data event. Lines that start anything else go
// on as they come, a line not yet ended included; only a line that may
// be such an object is held, until it ends (Google writes its errors
// pretty-printed elsewhere, so the lines after it too, until they parse).
func bareErrors(r io.Reader) io.Reader {
	return &bareErrorReader{r: r, blank: true}
}

type bareErrorReader struct {
	r   io.Reader
	in  []byte // read and not yet judged: a line not ended
	out []byte // judged, to hand on
	err error
	// mid: what went on last stopped inside a line, whose rest goes on
	// as it comes
	mid bool
	// bare is a JSON value's lines so far, held until they parse
	bare []byte
	// blank: the last line handed on was blank, or none was; an event
	// written in is set apart from a data line before it otherwise
	blank   bool
	scratch []byte
}

func (b *bareErrorReader) Read(p []byte) (int, error) {
	for len(b.out) == 0 {
		if b.err != nil {
			if len(b.in) > 0 || len(b.bare) > 0 {
				b.judge(true)
				continue
			}
			return 0, b.err
		}
		if b.scratch == nil {
			b.scratch = make([]byte, 32<<10)
		}
		n, err := b.r.Read(b.scratch)
		b.in = append(b.in, b.scratch[:n]...)
		b.err = err
		if n > 0 {
			b.judge(false)
		}
	}
	n := copy(p, b.out)
	b.out = b.out[n:]
	if len(b.out) == 0 {
		b.out = nil
	}
	return n, nil
}

// judge moves what of in can be judged to out; at the body's end
// (eof) all of it.
func (b *bareErrorReader) judge(eof bool) {
	for len(b.in) > 0 || eof && len(b.bare) > 0 {
		i := bytes.IndexByte(b.in, '\n')
		if b.mid {
			// the rest of a line that went on already
			if i < 0 {
				b.out = append(b.out, b.in...)
				b.in = b.in[:0]
				return
			}
			b.out = append(b.out, b.in[:i+1]...)
			b.in = b.in[i+1:]
			b.mid, b.blank = false, false
			continue
		}
		var line []byte
		switch {
		case i >= 0:
			line, b.in = b.in[:i+1], b.in[i+1:]
		case eof:
			line, b.in = b.in, b.in[:0]
		case b.bare == nil && !mayBeBare(b.in):
			// a line under way that can't be one: on now, as it came
			b.out = append(b.out, b.in...)
			b.in = b.in[:0]
			b.mid = true
			return
		default:
			return // held until it ends
		}
		t := bytes.TrimSpace(line)
		if b.bare != nil {
			if len(t) == 0 || sseField(t) {
				// an event's line: what was held was no JSON of its own
				b.release()
			} else {
				b.bare = append(b.bare, line...)
				b.settle()
				continue
			}
		}
		if len(line) == 0 {
			continue
		}
		if len(t) > 0 && (t[0] == '{' || t[0] == '[') {
			b.bare = append([]byte{}, line...)
			b.settle()
			continue
		}
		b.out = append(b.out, line...)
		b.blank = len(t) == 0
	}
}

// settle hands on the bare value held once it parses: as a data event
// when it is an error, as it came when it isn't. One grown past
// bareLimit goes as it came.
func (b *bareErrorReader) settle() {
	t := bytes.TrimSpace(b.bare)
	if !json.Valid(t) {
		if len(b.bare) > bareLimit {
			b.release()
		}
		return
	}
	data, ok := bareError(t)
	if !ok {
		b.release()
		return
	}
	if !b.blank {
		b.out = append(b.out, '\n')
	}
	b.out = append(b.out, "data: "...)
	b.out = append(b.out, data...)
	b.out = append(b.out, "\n\n"...)
	b.bare, b.blank = nil, true
}

func (b *bareErrorReader) release() {
	b.out = append(b.out, b.bare...)
	b.blank = len(bytes.TrimSpace(b.bare)) == 0
	b.bare = nil
}

// mayBeBare says whether a line begun with start may be a bare JSON
// value: nothing yet but spaces, or a { or [ first.
func mayBeBare(start []byte) bool {
	t := bytes.TrimLeft(start, " \t\r")
	return len(t) == 0 || t[0] == '{' || t[0] == '['
}

// sseField says whether a line is one of SSE's own: a field or a comment.
func sseField(t []byte) bool {
	for _, f := range [][]byte{[]byte("data:"), []byte("event:"), []byte("id:"), []byte("retry:"), []byte(":")} {
		if bytes.HasPrefix(t, f) {
			return true
		}
	}
	return false
}

// bareError is the data event a JSON value written on its own in an
// event stream stands for, when it is an error: Google's
// {"error":{"code","message","status"}}, OpenAI's {"error":{"message",
// "type","code"}}, Anthropic's {"type":"error","error":{…}}, or a list
// of one of them (Google's JSON array stream). On one line, its member
// order kept. Anything else is no event: SSE ignores it, as before.
func bareError(t []byte) ([]byte, bool) {
	if len(t) > 0 && t[0] == '[' {
		var list []json.RawMessage
		if json.Unmarshal(t, &list) != nil || len(list) != 1 {
			return nil, false
		}
		t = bytes.TrimSpace(list[0])
	}
	if len(t) == 0 || t[0] != '{' {
		return nil, false
	}
	var v struct {
		Type  json.RawMessage `json:"type"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(t, &v) != nil {
		return nil, false
	}
	var typ string
	json.Unmarshal(v.Type, &typ)
	if !carries(v.Error) && typ != "error" {
		return nil, false
	}
	var one bytes.Buffer
	if json.Compact(&one, t) != nil {
		return nil, false
	}
	return one.Bytes(), true
}
