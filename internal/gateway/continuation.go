package gateway

// A translated reply whose stream the upstream cut off mid-way — the
// connection lost, an error out of nowhere — is asked of the same
// conversation again, with what the client already has of the reply sent
// back for the model to go on from: the client reads one reply that
// finished, not one cut short, and the turn doesn't fail the way it used
// to. Only where the upstream goes on from a reply's part sent back (an
// Anthropic Messages upstream natively, a Chat one in its vendor's own
// prefill mode — prefillHow): one that answers the message again from
// the start instead, as most Chat APIs do, would splice a reworded
// re-answer onto the part the client has, with no error to tell anything
// went wrong, so there the reply ends with the error, as it used to.
// Only a reply no tool call of has begun goes on: a call's arguments
// can't be prefilled, so one begun ends the reply as it used to. A
// refusal isn't asked again.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/yetone/magpie/internal/provider"
)

// streamRetries is how many times a cut reply is asked to go on before it
// ends with the error, as it used to at once.
const streamRetries = 2

// emptyRetries is how many times a reply that said nothing (#667) is asked
// again on the same account before it fails: once while its stream is
// held, which the request then asks again, or of another account — or the
// group of its next member — and as often as a cut reply once the client
// has the stream, when nobody asks again after it.
func emptyRetries(w http.ResponseWriter, ctx context.Context) int {
	if h, ok := w.(*holdWriter); ok && h.mayAskAgain() || inGroupTry(ctx) {
		return 1
	}
	return streamRetries
}

// errStreamCut ends a reply's read once its error event is in hand,
// without waiting on a vendor that keeps the connection open after it.
var errStreamCut = errors.New("stream cut mid-reply")

// errEndedShort is an Anthropic stream that ended, its connection closed
// cleanly, before it said its reply had: the passthrough's word for it.
var errEndedShort = errors.New("the reply ended before it was complete")

// anthropicEnds says whether an Anthropic stream's event ends the reply:
// its message_stop (a stop_reason is upstreamStop's), or an error.
func anthropicEnds(data string) bool {
	var v struct {
		Type string `json:"type"`
	}
	return json.Unmarshal([]byte(data), &v) == nil && lastEvent(v.Type)
}

// groupTryKey marks a try of a routing group's member. A member whose
// stream breaks with an error mid-reply is failed as it used to be — the
// group answers the agent's retry with another of its members (#733) —
// while one whose connection only cut, saying nothing, is still asked to
// go on.
type groupTryKey struct{}

func groupTry(ctx context.Context) context.Context {
	return context.WithValue(ctx, groupTryKey{}, true)
}

func inGroupTry(ctx context.Context) bool {
	v, _ := ctx.Value(groupTryKey{}).(bool)
	return v
}

// prefillHow is how the upstream a reply is translated to goes on from
// the part of it the client has, sent back as the conversation's last
// assistant message: an Anthropic Messages upstream natively, a Chat one
// only in its vendor's own mode for it (chatPrefill). "" where the
// upstream answers the message again from the start instead — most Chat
// APIs — and a cut reply ends with the error there, as it used to: the
// client can't tell a reworded re-answer from the reply's own rest.
func prefillHow(p provider.Provider, to provider.Protocol, model string) string {
	switch to {
	case provider.Anthropic:
		return "anthropic"
	case provider.Chat:
		return chatPrefill(p.Host(), model)
	}
	return ""
}

// chatPrefill is the prefill mode of a Chat upstream at host serving
// model: DeepSeek's prefix (its own API, where the mode is served under
// /beta, with "prefix": true on the message) or Kimi's partial
// ("partial": true). Partial is a field of the message itself and prefix
// a path the relay forwards, so a relay on this machine or the LAN
// serving one of their models is taken to front the vendor, as
// geminiCompat takes one to front Gemini. "" elsewhere.
func chatPrefill(host, model string) string {
	h := strings.ToLower(host)
	switch {
	case h == "api.deepseek.com" || strings.HasSuffix(h, ".deepseek.com"):
		return "prefix"
	case strings.Contains(h, "moonshot") || strings.Contains(h, "kimi"):
		return "partial"
	}
	if s, _, err := net.SplitHostPort(h); err == nil {
		h = s
	}
	h = strings.Trim(h, "[]")
	if h != "localhost" && !strings.HasSuffix(h, ".local") {
		if ip := net.ParseIP(h); ip == nil || !ip.IsLoopback() && !ip.IsPrivate() {
			return ""
		}
	}
	switch m := strings.ToLower(model); {
	case strings.Contains(m, "deepseek"):
		return "prefix"
	case strings.Contains(m, "kimi"):
		return "partial"
	}
	return ""
}

// continuation is what of a translated reply the client already has, so a
// cut reply's next try asks the model to go on from there and only what
// is new goes to the client.
type continuation struct {
	think, text strings.Builder // thinking and text the client has
	tools       bool            // a tool call was begun: the reply can't go on
	mode        string          // how the upstream goes on from a prefill (prefillHow), "" where it can't
	resume      bool            // this try is a continuation
	echo        string          // the text it was prefilled with: a full echo of it is dropped
	seen        string          // what of a possible echo has come
}

// possible says whether the reply can go on: the upstream goes on from a
// prefill at all, the client has something of it, and no tool call of it
// was begun.
func (c *continuation) possible() bool {
	return c.mode != "" && !c.tools && (c.think.Len() > 0 || c.text.Len() > 0)
}

// again says whether the cut reply goes on with another try, and readies
// it: not the vendor's refusal (code), nor the request itself turned away
// (a 400 the same ask gets again, a 404 — the mode's endpoint isn't
// there), the tries not up, the client still there.
func (c *continuation) again(status int, code string, again int, ctx context.Context) bool {
	if code != "" || status == http.StatusBadRequest || status == http.StatusUnprocessableEntity ||
		status == http.StatusNotFound || again >= streamRetries || ctx.Err() != nil || !c.possible() {
		return false
	}
	c.resume, c.echo, c.seen = true, c.prefillText(), ""
	return true
}

// prefillText is the reply's text as the prefill carries it: trailing
// whitespace trimmed, which an Anthropic upstream turns the whole request
// away for (400 "final assistant content cannot end with trailing
// whitespace") — the client has it already, so the reply loses nothing,
// and the model goes on from its last word.
func (c *continuation) prefillText() string {
	return strings.TrimRightFunc(c.text.String(), unicode.IsSpace)
}

// request is orig with what the client has of the reply sent back as the
// last message, for the model to go on from.
func (c *continuation) request(orig *Request) *Request {
	r := *orig
	var parts []Part
	if s := c.think.String(); s != "" {
		parts = append(parts, Part{Kind: Thinking, Text: s})
	}
	if s := c.prefillText(); s != "" {
		parts = append(parts, Part{Kind: Text, Text: s})
	}
	r.Messages = append(slices.Clone(orig.Messages), Message{Role: "assistant", Parts: parts})
	r.Resume = true
	return &r
}

// emit hands an event of the reply to the client, less what a
// continuation says again: its framing is the encoder's already, its
// thinking isn't shown a second time, and a full echo of the text it was
// prefilled with is dropped. What goes to the client is journaled, for a
// next try to go on from.
func (c *continuation) emit(enc streamEncoder, ev Event) {
	if c.resume {
		switch ev.Kind {
		case KThink, KSig:
			return
		case KText:
			if ev.Text = c.unecho(ev.Text); ev.Text == "" {
				return
			}
		}
	}
	c.add(ev)
	enc.event(ev)
}

// add journals an event the client has.
func (c *continuation) add(ev Event) {
	switch ev.Kind {
	case KThink:
		c.think.WriteString(ev.Text)
	case KText:
		c.text.WriteString(ev.Text)
	case KToolStart, KToolArgs:
		c.tools = true
	}
}

// unecho drops a continuation's full echo of the text it was prefilled
// with, holding what could still be one until it reads either way. What
// the model says instead of echoing goes whole.
func (c *continuation) unecho(s string) string {
	if c.echo == "" {
		return s
	}
	c.seen += s
	if len(c.seen) < len(c.echo) && strings.HasPrefix(c.echo, c.seen) {
		return "" // could still be the echo
	}
	if strings.HasPrefix(c.seen, c.echo) {
		s = c.seen[len(c.echo):]
	} else {
		s = c.seen
	}
	c.seen, c.echo = "", ""
	return s
}

// streamTranslated relays a translated reply's events to the client as
// they come. A reply the upstream's stream cut off mid-way is asked of
// the same conversation again, going on from what the client has, up to
// streamRetries times; one that can't go on ends with the error in the
// client's own protocol, as it used to.
func (s *Server) streamTranslated(w http.ResponseWriter, r *http.Request, p provider.Provider, from, to provider.Protocol, request *Request, model string, zen *zenReply, u *Usage) (int, string) {
	sw := newSSEWriter(w)
	enc := encoder(from, sw, request, u)
	cont := &continuation{mode: prefillHow(p, to, model)}
	var failed, failedCode string
	var failedStatus int
	var cut, errSent bool
	var empty bool // a reply in this protocol that says nothing fails (#667)
	empties := 0   // the times such a reply was asked again here
	said, stop := false, ""
	var kept []Event       // the reply's end, while nothing is said in it
	var before, this Usage // what the tries before this one billed, and this try
	emit := func(ev Event) {
		switch ev.Kind {
		case KError:
			if ev.Code == "" && cont.possible() && !inGroupTry(r.Context()) {
				// held while the reply may yet go on; sent when it can't
				failed, failedCode, failedStatus, cut = ev.Text, ev.Code, ev.Status, true
				return
			}
			failed, failedCode, failedStatus = ev.Text, ev.Code, ev.Status
			errSent = true
		case KStart:
			// the tries are each billed, so their usages are summed into
			// the reply's — the client's and the ledger's (a cut try's is
			// only what it said before the cut)
			this = ev.Usage
			ev.Usage = ev.Usage.plus(before, false)
			u.add(ev.Usage)
			u.add(Usage{Served: ev.Model}) // the model the vendor says answered
		case KUsage:
			this.add(ev.Usage)
			ev.Usage = ev.Usage.plus(before, true)
			u.add(ev.Usage)
		case KStop:
			stop = ev.Stop
		}
		if empty && !said {
			switch {
			case saysSomething(ev):
				said = true
			case ev.Kind == KStop, ev.Kind == KUsage:
				kept = append(kept, ev)
				return
			}
		}
		cont.emit(enc, ev)
	}
	see := zenSee(zen, emit)
	alive := func() {
		// the provider's keepalives aren't events to translate: while it
		// is heard from, the client hears from magpie (#436)
		if failed == "" && sw.quiet() >= keepaliveGap {
			enc.keepalive()
		}
	}
	var serr error
	for again := 0; ; again++ {
		failed, failedCode, failedStatus, cut, serr = "", "", 0, false, nil
		var held func() // what a textCallSee of this try holds back
		req := request
		if cont.resume {
			req = cont.request(request)
		}
		res, actual, err := s.forwardTranslated(r.Context(), p, to, req, model, r.Header)
		if err != nil {
			if !cont.resume {
				return writeError(w, from, 502, p.Name+": "+err.Error()), err.Error()
			}
			serr = err
		}
		if err == nil {
			u.RequestID = requestID(res.Header)
			empty = emptyFails(actual)
		}
		if err == nil && res.StatusCode >= 400 {
			b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			res.Body.Close()
			failed, failedStatus = p.Explain(p.Name+": "+provider.APIError(b, res.Status), res.StatusCode, b), res.StatusCode
			if wrongEndpoint(res.StatusCode, b) {
				failed += wrongAPINote(p, model, res, b)
			}
			if res.StatusCode == http.StatusTooManyRequests && accountAgent(p) == "antigravity" && antigravityTurnsAway(request.System) {
				failed += " — " + antigravityTurnedAwayHint
			}
			if !cont.resume {
				if res.StatusCode == http.StatusTooManyRequests && accountAgent(p) == "antigravity" && antigravityTurnsAway(request.System) {
					markAntigravityTurnsAway(w)
				}
				if p.Preset == "openrouter" && openRouterSharedPool(b) {
					markOpenRouterSharedPool(w)
				}
				keepRetry(w.Header(), res.Header, b)
				u.ErrType = provider.ErrorType(b)
				return writeError(w, from, res.StatusCode, failed), failed
			}
		}
		if err == nil && res.StatusCode < 400 {
			rd, sse := eventStream(res)
			if !sse {
				// the provider ignored stream:true; read the whole reply as
				// one event stream would be wrong, so give up cleanly
				b, _ := io.ReadAll(io.LimitReader(rd, 1<<20))
				res.Body.Close()
				failed = p.Name + " did not stream: " + provider.APIError(b, "unexpected reply")
				if !cont.resume {
					return writeError(w, from, 502, failed), failed
				}
			} else {
				dec := decoder(actual)
				attemptSee := see
				if names := textCallNames(request.Tools); names != nil {
					t := &textCallSee{names: names, see: see}
					attemptSee, held = t.event, t.release
				}
				ended := false // the upstream said its reply ended
				serr = readSSEAlive(rd, func(_, data string) error {
					st := upstreamStop([]byte(data))
					u.add(Usage{Upstream: upstreamOf([]byte(data)), Stop: st})
					ended = ended || st != "" || actual == provider.Anthropic && anthropicEnds(data)
					if err := dec(data, attemptSee); err != nil {
						return err
					}
					if cut {
						return errStreamCut
					}
					return nil
				}, alive)
				res.Body.Close()
				if errors.Is(serr, errStreamCut) {
					serr = nil
				}
				if serr == nil && failed == "" && !ended && actual == provider.Anthropic && r.Context().Err() == nil {
					// an Anthropic stream that just stopped — no
					// stop_reason, no message_stop — is a reply cut
					// short, not a finished one: a relay's (蓝猫 on
					// Discord) read as whole ended the agent's turn a
					// few words in, with nothing to say why
					serr = errEndedShort
				}
			}
		}
		if serr == nil && failed == "" {
			if held != nil {
				held()
			}
			if empty && !said && answersNothing(stop) {
				if empties < emptyRetries(w, r.Context()) && r.Context().Err() == nil {
					// asked again here first, for the agent to have the
					// answer rather than the error: Gemini on a long
					// conversation now and then ends with only its
					// reasoning several times running, and an agent told
					// the error stops its run (#667, Pi). What reasoning
					// the client has stays, the next try's follows it.
					empties++
					before, this = before.plus(this, false), Usage{}
					kept, stop = nil, ""
					enc.keepalive()
					select {
					case <-time.After(retryPause << (empties - 1)):
					case <-r.Context().Done():
					}
					continue
				}
				// as an error it is asked again, or of another account,
				// and an agent told it tries again rather than end its
				// turn (#667); a reply that said nothing has nothing to
				// go on from, so it isn't continued
				failed = p.Name + ": " + emptyReply
				break
			}
			for _, ev := range kept {
				enc.event(ev)
			}
			if zen != nil {
				zen.end(enc.event)
			}
			enc.finish()
			return 200, ""
		}
		// an error the client already has ends the reply: nothing of it
		// goes on after it (a group member's mid-reply error fails the
		// try at once, for the group's next member, #733)
		if errSent || !cont.again(failedStatus, failedCode, again, r.Context()) {
			if held != nil {
				// what was held goes as the text it is, as it used to;
				// a reply that goes on reads it again instead
				held()
			}
			break
		}
		before, this = before.plus(this, false), Usage{}
		enc.keepalive() // the client waits while the same conversation is asked again
		select {
		case <-time.After(retryPause << again):
		case <-r.Context().Done():
		}
	}
	if serr != nil && failed == "" {
		// the upstream died mid-reply: say so in the client's own
		// protocol instead of finishing as if all went well
		failed = cutMidReply(p.Name, serr)
		if errors.Is(serr, errEndedShort) {
			failed = p.Name + ": " + errEndedShort.Error()
		}
	}
	if failed != "" {
		// the same refusal as the 429's, said inside the reply
		markAntigravityRefused(w, p, request.System, failed)
	}
	if !errSent {
		enc.event(Event{Kind: KError, Text: failed, Code: failedCode})
	}
	return 200, failed
}
