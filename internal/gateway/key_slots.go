package gateway

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/access"
)

// A gateway key's MaxConcurrency (young02036 on Discord: providers have
// one, keys should too) is how many of its requests may be in flight at
// once. One more waits in the key's queue, in the order it came, with no
// bound, as over a provider's MaxConcurrency (concurrency.go): it reads as
// waiting for a slot ("slot") in Active, and what it waited is its try's
// Queued. Its agent gone while it waits, it leaves the queue and nothing
// is sent. A slot is held until the handler returns: a stream's to its
// end. A key issued under another (issue.go) waits for a slot of its own,
// then one of its issuer's, so the keys it issues hold no more at once
// than it does. With no cap nothing is counted or waited.
//
// Every route that asks a model is held, whichever way the request came
// (the LAN, a Remote magpie, a socket's turn): keySlots sits in Handler
// beside keyLimited, which each of them passes. keySlotRoutes names those
// routes; TestKeySlotEveryRoute fails on a route judged neither way.
// count_tokens isn't held, as a provider's MaxConcurrency doesn't count
// it, nor MCP, nor a GET (a poll, a list).
//
// A route whose requests go through serve's tries (lazy) waits there, in
// the try (keySlot), so a streaming agent is kept alive as it is while it
// waits for a provider's slot (keepQueued); the others (eager) wait before
// their handler begins. A way of a lazy route that asks upstream outside a
// try (forward, codexUpstreamOn) waits there, so nothing reaches a vendor
// unheld.

// keySlotKind is how a route waits for its key's slot.
type keySlotKind int

const (
	keySlotEager keySlotKind = iota + 1
	keySlotLazy
)

// keySlotRoutes are the routes, by pattern, whose requests are held to
// their key's MaxConcurrency, and how; every other route of routes() is in
// keySlotFree (key_slots_test.go).
var keySlotRoutes = map[string]keySlotKind{
	"POST /v1/chat/completions":                     keySlotLazy,
	"POST /chat/completions":                        keySlotLazy,
	"POST /v1/responses":                            keySlotLazy,
	"POST /responses":                               keySlotLazy,
	"POST /v1/messages":                             keySlotLazy,
	"POST /messages":                                keySlotLazy,
	"POST /api/chat":                                keySlotLazy,
	"POST /api/generate":                            keySlotLazy,
	"POST /v1beta/models/{call...}":                 keySlotLazy,
	CodexPath + "/":                                 keySlotLazy,
	"POST /v1/systemone":                            keySlotEager,
	"POST /v1" + AlphaSearchPath:                    keySlotEager,
	"POST " + AlphaSearchPath:                       keySlotEager,
	"POST /v1/images/generations":                   keySlotEager,
	"POST /images/generations":                      keySlotEager,
	"POST /v1/images/edits":                         keySlotEager,
	"POST /images/edits":                            keySlotEager,
	"POST /v1/embeddings":                           keySlotEager,
	"POST /embeddings":                              keySlotEager,
	"POST /v1/rerank":                               keySlotEager,
	"POST /rerank":                                  keySlotEager,
	"POST /v1/decisions":                            keySlotEager,
	"POST /decisions":                               keySlotEager,
	"POST /v1/audio/transcriptions":                 keySlotEager,
	"POST /audio/transcriptions":                    keySlotEager,
	"POST /v1/audio/translations":                   keySlotEager,
	"POST /audio/translations":                      keySlotEager,
	"POST /v1/audio/speech":                         keySlotEager,
	"POST /audio/speech":                            keySlotEager,
	"POST /v1/videos":                               keySlotEager,
	"POST /videos":                                  keySlotEager,
	"POST " + NativePath + "{provider}/{path...}":   keySlotEager,
	"PUT " + NativePath + "{provider}/{path...}":    keySlotEager,
	"PATCH " + NativePath + "{provider}/{path...}":  keySlotEager,
	"DELETE " + NativePath + "{provider}/{path...}": keySlotEager,
}

// keyCapped says whether who's key, or the one it was issued under, has a
// MaxConcurrency.
func keyCapped(who access.Identity) bool {
	return who.MaxConcurrency > 0 || who.Issuer != nil && who.Issuer.MaxConcurrency > 0
}

// keySlots holds each request of a key with a MaxConcurrency to it, on
// the routes keySlotRoutes names.
func (s *Server) keySlots(mux *routeMux, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who := access.Caller(r.Context())
		if !keyCapped(who) || r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions || strings.HasSuffix(r.URL.Path, ":countTokens") {
			next.ServeHTTP(w, r)
			return
		}
		_, pattern := mux.Handler(r)
		kind := keySlotRoutes[pattern]
		if kind == 0 {
			next.ServeHTTP(w, r)
			return
		}
		h := &keyHold{s: s, who: who, turn: make(chan struct{}, 1)}
		defer h.release()
		if kind == keySlotEager {
			if _, err := h.take(r.Context()); err != nil {
				return // its agent went away while it waited: nothing was sent
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyHoldKey{}, h)))
	})
}

type keyHoldKey struct{}

// keyHold is one request's slot in its key's lane, and its issuer's.
type keyHold struct {
	s   *Server
	who access.Identity
	// turn is held by the one taking the slot, so two parts of a request
	// asking at once take one slot between them; it waits on no lock
	// release needs
	turn chan struct{}

	mu     sync.Mutex
	held   bool
	closed bool // the request is done: what is taken after goes back at once
	rels   []func()
}

// take waits for the request's slot, once: taken already, or the request
// done, it returns at once. waited is how long it stood in the queues; err
// is ctx's when it ended first, with nothing taken, and a later take
// waits again.
func (h *keyHold) take(ctx context.Context) (waited time.Duration, err error) {
	select {
	case h.turn <- struct{}{}:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	defer func() { <-h.turn }()
	h.mu.Lock()
	done := h.held || h.closed
	h.mu.Unlock()
	if done {
		return 0, nil
	}
	var rels []func()
	for _, k := range []*access.Identity{&h.who, h.who.Issuer} {
		if k == nil || k.MaxConcurrency <= 0 {
			continue
		}
		rel, w, err := h.s.keyLanes.take(ctx, keyLane(k.KeyID), k.MaxConcurrency, 0, 0, nil)
		waited += w
		if err != nil {
			for _, f := range rels {
				f()
			}
			return waited, err
		}
		rels = append(rels, rel)
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		for _, f := range rels {
			f()
		}
		return waited, nil
	}
	h.held, h.rels = true, rels
	h.mu.Unlock()
	return waited, nil
}

// release gives the slots back, once the request is done.
func (h *keyHold) release() {
	h.mu.Lock()
	h.closed = true
	rels := h.rels
	h.rels = nil
	h.mu.Unlock()
	for _, f := range rels {
		f()
	}
}

// keyLane is a gateway key's lane in keyLanes, apart from every
// provider's.
func keyLane(id string) string { return "key:" + id }

// keySlot waits for the slot of the key ctx's request came with, when
// its route holds it (keySlots) and it isn't taken yet; 0 and nil when
// there is none to wait for.
func keySlot(ctx context.Context) (time.Duration, error) {
	h, _ := ctx.Value(keyHoldKey{}).(*keyHold)
	if h == nil {
		return 0, nil
	}
	return h.take(ctx)
}

// KeyLanes is how each gateway key with a MaxConcurrency stands, by its
// id: its requests in flight and those waiting their turn.
func (s *Server) KeyLanes() map[string]Lane {
	s.keyLanes.mu.Lock()
	defer s.keyLanes.mu.Unlock()
	out := map[string]Lane{}
	for who, ln := range s.keyLanes.m {
		out[strings.TrimPrefix(who, "key:")] = Lane{Busy: ln.busy, Waiting: len(ln.queue), Limit: ln.limit}
	}
	return out
}
