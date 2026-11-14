package gateway

// The vendor's reply headers passed on to the agent (#4145): its request
// id by default, and whatever else the provider lists (ReplyHeaders).
//
// Every request that may become the agent's reply goes out through
// forward (each API, translated or not, a plugin's fetch) or retrieveAs
// (embeddings, audio), and both say what they heard to the try that asked
// (heardReply). A try's writer keeps it until the try has its status:
// the holdWriter of a candidate's try, so a try that fails and is let go
// takes its vendor's headers with it, and the agent has those of the
// reply it is given alone.

import (
	"context"
	"net/http"
	"slices"

	"github.com/yetone/magpie/internal/provider"
)

type replySinkKey struct{}

// replySink is where a try's vendor replies' passed headers go: the
// provider the try is of, and what takes them.
type replySink struct {
	id  string
	put func(http.Header)
}

// withReplySink is ctx whose requests to the provider id say the headers
// their reply passes on to put, the last reply's in place of those before.
func withReplySink(ctx context.Context, id string, put func(http.Header)) context.Context {
	return context.WithValue(ctx, replySinkKey{}, replySink{id: id, put: put})
}

// heardReply tells the try of ctx the headers res passes on, when res is
// its provider's reply: none for a request the try makes elsewhere.
func heardReply(ctx context.Context, p provider.Provider, res *http.Response) {
	s, ok := ctx.Value(replySinkKey{}).(replySink)
	if !ok || res == nil || s.id != p.ID {
		return
	}
	s.put(tierThere(p, res.Header, p.PassedReplyHeaders(res.Header)))
}

// passReplyHeaders puts what the vendor's reply res passes on into dst,
// for a reply written straight to the agent.
func passReplyHeaders(dst http.Header, p provider.Provider, res *http.Response) {
	if res == nil {
		return
	}
	for k, vs := range p.PassedReplyHeaders(res.Header) {
		dst[k] = vs
	}
}

// heardUpstream sets the headers the vendor's reply passes on, in place of
// those of an earlier reply of the same try (an ask again after a refused
// beta or shape). Once the try has its status they are what it is: a
// reply asked for after it (a cut reply's continuation) changes nothing.
func (h *holdWriter) heardUpstream(got http.Header) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.status != 0 {
		return
	}
	for _, k := range h.upstreamKeys {
		h.header.Del(k)
	}
	h.upstreamKeys = h.upstreamKeys[:0]
	for k, vs := range got {
		h.header[k] = slices.Clone(vs)
		h.upstreamKeys = append(h.upstreamKeys, k)
	}
}

// heardHeaders keeps the last reply's passed headers, for a try that
// writes the agent's reply itself.
type heardHeaders struct{ h http.Header }

func (s *heardHeaders) put(h http.Header) { s.h = h }

// into puts them into dst.
func (s *heardHeaders) into(dst http.Header) {
	for k, vs := range s.h {
		dst[k] = vs
	}
}
