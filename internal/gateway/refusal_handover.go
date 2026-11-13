package gateway

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// refusalHandover is a try's stream let through on its reasoning, or on what
// only frames a reply, while another candidate stands behind it: the
// vendor's safety filter may still refuse it before a word of the answer
// is said, and the refusal then goes to the next candidate, whose stream
// follows in the same response, as one refused with nothing sent does
// (#248). Before, the agent had the reasoning, then the refusal, and the
// next candidate was never asked: GLM's thinking shown as it came, then
// its filter's stop, and the turn ended with nothing (yhwangl on X).
//
// What went through is read event by event to know where the next
// stream's events go: an Anthropic stream's content blocks, a Responses
// stream's output items and sequence numbers. Once an answer's text or a
// call begins (spoke), a refusal is the agent's, as before (refusalAfter).
type refusalHandover struct {
	proto   provider.Protocol
	pending []byte // an event not yet whole
	spoke   bool   // the answer began: nothing is handed over any more

	blocks int          // Anthropic: content blocks begun
	open   map[int]bool // Anthropic: begun, not yet stopped
	items  int          // Responses: output items added
	seq    int          // Responses: the last sequence_number sent, -1 for none
}

// errHandedOver is what a try's writes get once its refusal was handed to
// the next candidate.
var errHandedOver = errors.New("refused: handed to the next candidate")

// handsOver says whether a try's refusal after its reasoning went through
// may go to the next candidate in the same stream: the agent asked for a
// stream in a protocol whose events can be joined (spliceAfter), another
// candidate is left, and a refusal is asked again (refusalAsksAgain). A
// reply wanted whole is held to its end anyway (wantWhole). A Gemini
// stream is not joined: its chunks carry no index to move and nothing
// says where one reply ends and the next begins.
func handsOver(streams, last, wantWhole bool, from provider.Protocol, refusals int) bool {
	if !streams || last || wantWhole || !refusalAsksAgain(refusals+1) {
		return false
	}
	return from == provider.Chat || from == provider.Anthropic || from == provider.Responses
}

// overActive says whether what the try writes is read for a refusal to
// hand over: it is passing, a stream, and has said nothing yet.
func (h *holdWriter) overActive() bool {
	return h.over != nil && !h.over.spoke && h.passing && h.stream && !h.handedOver
}

// passOver writes what a passing try said while nothing of its answer
// has been: its reasoning and frames go as they come, an event at a time,
// and a refusal among them is handed over (handOver), not sent. An SSE
// comment goes at once. Once the answer begins, the rest goes as it comes
// and is read for a refusal as any reply that has begun is.
func (h *holdWriter) passOver(b []byte) {
	o := h.over
	o.pending = append(o.pending, b...)
	for !o.spoke && !h.handedOver {
		p := o.pending
		if len(p) > 0 && p[0] == ':' {
			// a comment, keepalive's: no event, gone at once
			i := bytes.IndexByte(p, '\n')
			if i < 0 {
				break
			}
			h.out(p[:i+1])
			o.pending = p[i+1:]
			continue
		}
		end := eventEnd(p)
		if end < 0 {
			if len(p) > holdMost {
				o.spoke = true // no events to read: let it go as it comes
			}
			break
		}
		ev := p[:end]
		switch kind, status, msg := streamEvent(ev); kind {
		case eventRefusal, eventRefusalError:
			o.pending = nil
			h.handOver(status, msg, true)
			return
		case eventError:
			// a failure the next candidate may answer, nothing yet said
			// (Anthropic's overloaded_error after its thinking): handed
			// over as a refusal is, as it went to the next candidate
			// while the reasoning was held (#4166). One that isn't
			// retryable is the agent's, as before.
			if !retryable(status, []byte(msg)) {
				o.spoke = true
				break
			}
			o.pending = nil
			h.handOver(status, msg, false)
			return
		case eventThinking, eventLead, eventBuffering:
			o.track(ev)
			h.out(ev)
			o.pending = p[end:]
		default:
			o.spoke = true
		}
	}
	if o.spoke && len(o.pending) > 0 {
		rest := o.pending
		o.pending = nil
		h.refusalAfter(rest)
		if !h.handedOver { // the refusal in it handed the try over (handOffAfter)
			h.out(rest)
		}
	}
	h.flush()
}

// out writes b to the agent.
func (h *holdWriter) out(b []byte) {
	if len(b) == 0 {
		return
	}
	h.sent(b)
	h.w.Write(b)
}

// handsOff says whether a refusal that is the vendor's own finish code,
// once its answer has gone out (refusalAfter), may hand the try over: the
// reply is passing — what is still held goes through the scan, whose own
// refusal paths (flow, handOver) ask the next candidate without it — a
// candidate stands behind it reading Chat chunks, whose pieces carry no
// index for the next stream's to collide with, and no call of the reply
// has gone by, for the agent to be left holding beside the next member's.
// An Anthropic or Responses client's stream is numbered, and what of it
// has gone by untracked would number the next one's blocks wrong; its
// refusal goes as it came, as before.
func (h *holdWriter) handsOff() bool {
	return h.passing && h.over != nil && h.over.proto == provider.Chat && !h.calls
}

// handOffAfter ends a passing try at the vendor's finish code saying the
// turn it had begun was filtered (#5075): whatever it said first — the
// canned line a word-list filter answers with — the code says the turn
// was never answered, and a client that reads finish reasons fails the
// whole turn on it, so the members left are asked in the same stream, as
// a refusal after reasoning is (handOver). What went out first stays, as
// that reasoning does; the refusal's own end (the finish chunk, usage,
// [DONE]) isn't sent, and the next one's chunks follow without a second
// 200. Its account doesn't rest: the request was refused, not the
// account (#248).
func (h *holdWriter) handOffAfter(status int, msg string) {
	h.flush()
	h.handedOver, h.passing = true, false
	h.refused, h.failure, h.failMsg = true, status, msg
	h.ended, h.tail = false, nil
	if h.alive != nil {
		h.alive.sent = true
	}
	if h.stop != nil {
		h.stop()
	}
}

// handOver ends a passing try at its refusal (passOver): the agent has
// what went before it, an Anthropic block still open is stopped, and the
// try fails as a refusal with nothing said does, for the next candidate's
// stream to follow in the same response. Its account doesn't rest: the
// request was refused, not the account (#248). A retryable error handed
// over (refused false) fails the try as one held would have, its status
// and its words for the account's rest.
func (h *holdWriter) handOver(status int, msg string, refused bool) {
	o := h.over
	if o.proto == provider.Anthropic && len(o.open) > 0 {
		open := make([]int, 0, len(o.open))
		for i := range o.open {
			open = append(open, i)
		}
		slices.Sort(open)
		for _, i := range open {
			h.out(fmt.Appendf(nil, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", i))
		}
		o.open = nil
	}
	h.flush()
	h.handedOver, h.passing = true, false
	h.refused, h.failure, h.failMsg = refused, status, msg
	// the refusal's own end (message_stop, [DONE]) wasn't sent: the
	// reply isn't whole, and the next one's stream follows without its
	// 200 and headers again
	h.ended, h.tail = false, nil
	if h.alive != nil {
		h.alive.sent = true
	}
	if h.stop != nil {
		h.stop()
	}
}

// track notes where an event that went through leaves the stream: the
// content blocks an Anthropic one has begun and not stopped, the output
// items and the last sequence number of a Responses one.
func (o *refusalHandover) track(ev []byte) {
	data := eventData(ev)
	if data == nil {
		return
	}
	switch o.proto {
	case provider.Anthropic:
		typ := gjson.GetBytes(data, "type").String()
		idx := gjson.GetBytes(data, "index")
		if !idx.Exists() {
			return
		}
		i := int(idx.Int())
		switch typ {
		case "content_block_start":
			o.blocks = max(o.blocks, i+1)
			if o.open == nil {
				o.open = map[int]bool{}
			}
			o.open[i] = true
		case "content_block_stop":
			delete(o.open, i)
		}
	case provider.Responses:
		if oi := gjson.GetBytes(data, "output_index"); oi.Exists() {
			o.items = max(o.items, int(oi.Int())+1)
		}
		if sq := gjson.GetBytes(data, "sequence_number"); sq.Exists() {
			o.seq = int(sq.Int())
		}
	}
}

// spliceAfter is where the next try writes once a try's refusal was
// handed over (handOver): its stream goes on in the same response, as the
// same reply. An Anthropic one's message_start is dropped and its content
// blocks numbered after those already sent; a Responses one's
// response.created, response.in_progress and response.queued are dropped,
// its output items numbered after those sent, and its sequence numbers go
// on from the last one sent. A Chat one's chunks need nothing.
func spliceAfter(w http.ResponseWriter, o *refusalHandover) http.ResponseWriter {
	if o.proto == provider.Chat {
		return w
	}
	off := o.blocks
	if o.proto == provider.Responses {
		off = o.items
	}
	return &splicedWriter{w: w, proto: o.proto, off: off, seq: o.seq + 1}
}

// splicedWriter writes a try's stream after another's (spliceAfter).
type splicedWriter struct {
	w       http.ResponseWriter
	proto   provider.Protocol
	off     int // added to each content block's or output item's index
	seq     int // Responses: the next sequence_number
	pending []byte
}

func (s *splicedWriter) Header() http.Header { return s.w.Header() }

// WriteHeader: the agent has the stream's 200 already.
func (s *splicedWriter) WriteHeader(int) {}

func (s *splicedWriter) Unwrap() http.ResponseWriter { return s.w }

func (s *splicedWriter) Flush() {
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *splicedWriter) Write(b []byte) (int, error) {
	s.pending = append(s.pending, b...)
	for {
		p := s.pending
		if len(p) > 0 && p[0] == ':' {
			i := bytes.IndexByte(p, '\n')
			if i < 0 {
				break
			}
			if _, err := s.w.Write(p[:i+1]); err != nil {
				return 0, err
			}
			s.pending = p[i+1:]
			continue
		}
		end := eventEnd(p)
		if end < 0 {
			if len(p) > holdMost {
				// not events: as it comes
				s.pending = nil
				if _, err := s.w.Write(p); err != nil {
					return 0, err
				}
			}
			break
		}
		ev := s.rewrite(p[:end])
		s.pending = p[end:]
		if len(ev) > 0 {
			if _, err := s.w.Write(ev); err != nil {
				return 0, err
			}
		}
	}
	if len(s.pending) == 0 {
		s.pending = nil
	}
	return len(b), nil
}

// rewrite is an event of the next try's as it goes after the one handed
// over: nil to drop it.
func (s *splicedWriter) rewrite(ev []byte) []byte {
	data := eventData(ev)
	if data == nil {
		return ev
	}
	typ := gjson.GetBytes(data, "type").String()
	var fields []string
	switch s.proto {
	case provider.Anthropic:
		if typ == "message_start" {
			return nil
		}
		fields = []string{"index"}
	case provider.Responses:
		switch typ {
		case "response.created", "response.in_progress", "response.queued":
			return nil
		}
		fields = []string{"output_index", "sequence_number"}
	}
	type edit struct {
		at, n int
		to    string
	}
	var edits []edit
	for _, f := range fields {
		r := gjson.GetBytes(data, f)
		if !r.Exists() || r.Type != gjson.Number || r.Index <= 0 {
			continue
		}
		var to int
		if f == "sequence_number" {
			to = s.seq
			s.seq++
		} else {
			to = int(r.Int()) + s.off
		}
		edits = append(edits, edit{r.Index, len(r.Raw), strconv.Itoa(to)})
	}
	if len(edits) == 0 {
		return ev
	}
	slices.SortFunc(edits, func(a, b edit) int { return b.at - a.at })
	nd := append([]byte(nil), data...)
	for _, e := range edits {
		nd = append(nd[:e.at:e.at], append([]byte(e.to), nd[e.at+e.n:]...)...)
	}
	at := bodyIndexBytes(ev, data)
	if at < 0 {
		return ev
	}
	out := make([]byte, 0, len(ev)+len(nd)-len(data))
	out = append(out, ev[:at]...)
	out = append(out, nd...)
	return append(out, ev[at+len(data):]...)
}
