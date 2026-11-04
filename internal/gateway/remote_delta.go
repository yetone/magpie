package gateway

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// Two magpies that share their gateways with each other (LotusDecoder on
// X: one in China, one in the US, each the other's provider) sent the
// leg between them the whole conversation every turn, over a slow link,
// though Codex sends magpie only its new input on its WebSocket. Codex
// says store false, so neither prevResponses nor prevDelta keep or cut
// anything for it.
//
// So, on a Responses request to a remote magpie, this magpie asks the
// other to keep the turn whatever store says (KeepTurnHeader), and a
// magpie that does says so on its reply (KeptTurnHeader). This side keeps
// what it sent that route and scope: a digest of each input item as it
// went (after masking and every translation), the reply's output items'
// type and ids, and the other magpie's id for the reply (remoteSent). The
// next request whose input starts with exactly those items, then as many
// items as the reply had of the same type and call_id or id, goes as the
// items after them with previous_response_id, store as it was. Anything
// else goes whole: another history, an edited one, a request that names
// a previous response itself, a magpie that never said it keeps turns
// (one from before this), or a route whose magpie refused the id within
// remoteRefusedWait (it lost it to a restart, or let it go under its own
// cap), as prevRefused finds it.
//
// The other magpie keeps the turn under prevScope, in prevResponses,
// bounded by prevCap and prevTTL like any other. It takes the header only
// from another magpie (keepTurnAsks), and an id
// a magpie asking so names that it doesn't hold is answered
// previous_response_not_found before anything is sent: relayed as
// written, a vendor that ignores the field would answer without the
// conversation. A client that sends magpie's User-Agent to make magpie
// keep its turns is kept to its own scope and the caps, as store true
// would have it, and the header comes off every request as it arrives,
// so no provider is ever sent it (withCaller); it goes on only to a
// remote magpie (forwardOnce).

const (
	// KeepTurnHeader asks a remote magpie to keep the turn though the
	// request says store false
	KeepTurnHeader = "X-Magpie-Keep-Turn"
	// KeptTurnHeader on a magpie's reply says it kept the turn when asked
	KeptTurnHeader = "X-Magpie-Kept-Turn"
	// remoteRefusedWait is how long a route whose magpie refused the id
	// is sent whole before a delta is tried again
	remoteRefusedWait = 30 * time.Minute
	// remoteSentCap bounds the bytes of what is kept of turns sent
	remoteSentCap = 32 << 20
	// remotePerRoute is the most conversations kept per route and scope
	remotePerRoute = 16
)

type itemSum [sha256.Size]byte

// itemMark is what of a reply's output item its next request must match.
type itemMark struct{ typ, id, callID string }

// remoteSent is one turn sent a remote magpie and answered.
type remoteSent struct {
	key    string // route and scope
	input  []itemSum
	output []itemMark
	id     string // the other magpie's id for the reply
	at     time.Time
	el     *list.Element
}

func (t *remoteSent) size() int { return len(t.input)*sha256.Size + len(t.output)*64 + len(t.id) }

type remoteSentStore struct {
	mu      sync.Mutex
	m       map[string][]*remoteSent
	order   list.List // front the newest
	size    int
	refused map[string]time.Time // route: when its magpie refused an id
	now     func() time.Time
}

var remoteTurns = newRemoteSentStore()

func newRemoteSentStore() *remoteSentStore {
	return &remoteSentStore{m: map[string][]*remoteSent{}, refused: map[string]time.Time{}, now: time.Now}
}

// match is the newest turn kept under key that items continue: its input
// digests the first items' own, then items of its output's types and ids,
// and at least one item after them. n is how many items it covers.
func (s *remoteSentStore) match(key string, sums []itemSum, items []json.RawMessage) (best *remoteSent, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.m[key] {
		if s.now().Sub(t.at) > prevTTL {
			continue
		}
		c := len(t.input) + len(t.output)
		if c >= len(items) || c <= n || !sumsEqual(sums[:len(t.input)], t.input) || !marksMatch(t.output, items[len(t.input):c]) {
			continue
		}
		best, n = t, c
	}
	return best, n
}

func (s *remoteSentStore) put(t, replaces *remoteSent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if replaces != nil && replaces.el != nil {
		s.drop(replaces)
	}
	t.at = s.now()
	if t.size() > remoteSentCap/2 {
		return
	}
	if l := s.m[t.key]; len(l) >= remotePerRoute {
		oldest := l[0]
		for _, o := range l[1:] {
			if o.at.Before(oldest.at) {
				oldest = o
			}
		}
		s.drop(oldest)
	}
	t.el = s.order.PushFront(t)
	s.m[t.key] = append(s.m[t.key], t)
	s.size += t.size()
	for s.size > remoteSentCap {
		s.drop(s.order.Back().Value.(*remoteSent))
	}
	for e := s.order.Back(); e != nil && s.now().Sub(e.Value.(*remoteSent).at) > prevTTL; e = s.order.Back() {
		s.drop(e.Value.(*remoteSent))
	}
}

// drop is called with mu held.
func (s *remoteSentStore) drop(t *remoteSent) {
	if t.el == nil {
		return
	}
	s.order.Remove(t.el)
	t.el = nil
	s.size -= t.size()
	l := s.m[t.key]
	for i, o := range l {
		if o == t {
			l = append(l[:i:i], l[i+1:]...)
			break
		}
	}
	if len(l) == 0 {
		delete(s.m, t.key)
	} else {
		s.m[t.key] = l
	}
}

func (s *remoteSentStore) forget(t *remoteSent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drop(t)
}

func (s *remoteSentStore) refuse(route string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refused[route] = s.now()
}

func (s *remoteSentStore) waiting(route string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.refused[route]
	if ok && s.now().Sub(at) > remoteRefusedWait {
		delete(s.refused, route)
		return false
	}
	return ok
}

func sumsEqual(a, b []itemSum) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func markOf(item []byte) itemMark {
	r := gjson.GetManyBytes(item, "type", "id", "call_id")
	return itemMark{typ: r[0].String(), id: r[1].String(), callID: r[2].String()}
}

// marksMatch is whether items are the reply's output as the client gave
// it back: each of the same type and call_id, and of the same id where
// both have one (Codex sends its items back without theirs).
func marksMatch(want []itemMark, items []json.RawMessage) bool {
	if len(want) != len(items) {
		return false
	}
	for i, w := range want {
		got := markOf(items[i])
		if got.typ != w.typ || got.callID != w.callID || w.id != "" && got.id != "" && got.id != w.id {
			return false
		}
	}
	return true
}

// remoteTurn is one Responses request to a remote magpie, as passthrough
// sends it: the whole input's digests, and the turn it continues if a
// delta is sent.
type remoteTurn struct {
	key, route string
	sums       []itemSum
	from       *remoteSent
}

type keepTurnKey struct{}

// remoteTurnFor is the request's turn to a remote magpie, nil for one
// whose body names a previous response itself or whose input isn't a
// list of items: those go as they are, and aren't asked kept.
func remoteTurnFor(r *http.Request, p provider.Provider, body []byte) *remoteTurn {
	if !p.IsRemoteMagpie() {
		return nil
	}
	if v := gjson.GetBytes(body, "previous_response_id"); v.Exists() && v.Type != gjson.Null {
		return nil
	}
	in := gjson.GetBytes(body, "input")
	if !in.IsArray() {
		return nil
	}
	items, ok := inputList(json.RawMessage(in.Raw))
	if !ok {
		return nil
	}
	route := prevRoute(p)
	t := &remoteTurn{key: route + "\x00" + prevScope(r), route: route, sums: make([]itemSum, len(items))}
	for i, it := range items {
		t.sums[i] = sha256.Sum256(it)
	}
	return t
}

// delta is body with the input after the turn it continues alone, and
// that turn's id, when one is kept and its route hasn't refused lately.
func (t *remoteTurn) delta(body []byte) ([]byte, bool) {
	if remoteTurns.waiting(t.route) {
		return body, false
	}
	items, ok := inputList(json.RawMessage(gjson.GetBytes(body, "input").Raw))
	if !ok || len(items) != len(t.sums) {
		return body, false
	}
	from, n := remoteTurns.match(t.key, t.sums, items)
	if from == nil {
		return body, false
	}
	var q map[string]json.RawMessage
	if decodeJSONView(body, &q) != nil {
		return body, false
	}
	in, err := json.Marshal(items[n:])
	if err != nil {
		return body, false
	}
	id, _ := json.Marshal(from.id)
	out := make(map[string]json.RawMessage, len(q)+1)
	for k, v := range q {
		out[k] = v
	}
	out["input"], out["previous_response_id"] = in, id
	nb, err := json.Marshal(out)
	if err != nil {
		return body, false
	}
	t.from = from
	return nb, true
}

// refused notes that the route's magpie didn't hold the id: the turn is
// forgotten, and the route sent whole for remoteRefusedWait.
func (t *remoteTurn) refused() {
	if t.from != nil {
		remoteTurns.forget(t.from)
		t.from = nil
	}
	remoteTurns.refuse(t.route)
}

// tee reads the reply as it is relayed; finish keeps the turn once it is
// read, if it completed.
func (t *remoteTurn) tee(rc io.ReadCloser, ctype string) (io.ReadCloser, func()) {
	sp := &spool{limit: prevReplyMax, pattern: "magpie-remoteturn-*"}
	tr := &teeBody{ReadCloser: rc, sp: sp, ctype: ctype}
	return tr, func() {
		defer sp.discard()
		tr.mu.Lock()
		defer tr.mu.Unlock()
		if sp.cut() {
			return
		}
		reply, err := sp.read()
		if err != nil || len(reply) == 0 {
			return
		}
		id, output, ok := prevReply(tr.ctype, reply)
		if !ok {
			return
		}
		kept := &remoteSent{key: t.key, input: t.sums, id: id, output: make([]itemMark, len(output))}
		for i, it := range output {
			kept.output[i] = markOf(it)
		}
		remoteTurns.put(kept, t.from)
	}
}

type teeBody struct {
	io.ReadCloser
	mu    sync.Mutex
	sp    *spool
	ctype string
}

func (b *teeBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.mu.Lock()
	b.sp.add(p[:n])
	b.mu.Unlock()
	return n, err
}

// withKeepTurn marks a request's context for forwardOnce to ask the
// remote magpie to keep the turn.
func withKeepTurn(ctx context.Context) context.Context {
	return context.WithValue(ctx, keepTurnKey{}, true)
}

func keepTurnAsked(ctx context.Context) bool {
	v, _ := ctx.Value(keepTurnKey{}).(bool)
	return v
}

// keepsTurnFor is whether a request asks this magpie to keep its turn
// though it says store false: only another magpie's (callerFrom).
func keepsTurnFor(r *http.Request) bool { return callerOf(r).keepTurn }

// keptTurnLost is the id a magpie asking its turns kept named that this
// one doesn't hold: answered previous_response_not_found, never relayed.
func keptTurnLost(r *http.Request) string {
	if !keepsTurnFor(r) {
		return ""
	}
	return prevLost(r)
}

// keepTurnAsks is whether r is another magpie asking its turn kept: the
// header names the magpie that asks (magpie/<version>), and the request
// comes as a magpie's to another does, with magpie's User-Agent or, for a
// Codex turn, Codex's, which forwardOnce passes on as Codex sent it (a
// relay that serves only Codex knows it by them). A plain client's header
// is ignored. A client that copies both is kept to its own scope and the
// caps, as store true would have it.
func keepTurnAsks(r *http.Request) bool {
	if !strings.HasPrefix(r.Header.Get(KeepTurnHeader), "magpie/") {
		return false
	}
	return magpieAsks(r) || fromCodex(r.Header)
}

// magpieAsks is whether r comes from another magpie, by its User-Agent.
func magpieAsks(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("User-Agent"), "magpie/")
}
