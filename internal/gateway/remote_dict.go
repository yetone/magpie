package gateway

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"hash/maphash"
	"io"
	"math/bits"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// A magpie using another magpie in the cloud (a Remote magpie provider:
// a cloud or Docker deploy, a shared gateway) sent that magpie every
// request whole and uncompressed, and an agent's request is the whole
// conversation every turn: a long Claude Code session with a screenshot
// is megabytes, nearly all of it the request before (gaubeebangeel on X,
// after jixoai/reply9842, whose idea this follows: the body before as a
// zstd dictionary for the next).
//
// So the leg between two magpies, the one hop magpie holds both ends of,
// goes as RFC 9842's Dictionary-Compressed Zstandard, turned round for a
// request: a request to a remote magpie of dictMin bytes or more offers
// its body as a dictionary (KeepBodyHeader), and a magpie that keeps it
// says so on its reply with the body's SHA-256 (KeptBodyHeader). This
// side then keeps the body it sent under the route, and the next request
// goes as "dcz" against the kept body it shares the most with
// (dictBase), naming it in Available-Dictionary, its own SHA-256 in
// BodyDigestHeader; one that shares too little with any goes as plain
// zstd. A magpie that never said it keeps bodies (one from before this)
// is sent each body as it was, so nothing is asked of it.
//
// The other magpie keeps each body under the key it came with and the
// address it came from (dictScope), bounded per caller and in all
// (keptBodies), and decodes a "dcz" body only against a dictionary of the
// same scope: another key, or the same key from another computer, can't
// name one, nor learn whether it is held. A dictionary it doesn't hold
// (a restart, evicted, another scope), a body that doesn't decode, or a
// decoded body whose digest differs is answered 400 with
// DictMissHeader, before any provider is asked, and this side sends the
// request again whole (dictSend.after): a body is never guessed.
const (
	// KeepBodyHeader on a request to another magpie offers its body as a
	// dictionary for the next ("dcz")
	KeepBodyHeader = "X-Magpie-Keep-Body"
	// KeptBodyHeader on a magpie's reply is the SHA-256 of the request
	// body it kept, as an RFC 8941 byte sequence
	KeptBodyHeader = "X-Magpie-Kept-Body"
	// availableDictHeader is RFC 9842's: the SHA-256 of the dictionary a
	// "dcz" body was compressed against
	availableDictHeader = "Available-Dictionary"
	// BodyDigestHeader is the SHA-256 of the decoded body, checked after
	// decoding
	BodyDigestHeader = "X-Magpie-Body-Digest"
	// DictMissHeader on a 400 says the body wasn't decoded: send it whole
	DictMissHeader = "X-Magpie-Dictionary-Miss"

	// dictMin is the smallest body offered as a dictionary or compressed
	dictMin = 16 << 10
	// dictMax is the largest body kept as a dictionary, on either side
	dictMax = 48 << 20
	// dictTTL is how long a kept body is used
	dictTTL = 30 * time.Minute
	// dictPerScope is the most bodies kept per caller or route
	dictPerScope = 8
	// dictScopeBytes bounds one caller's kept bodies on the magpie kept for
	dictScopeBytes = 96 << 20
	// dictServerBytes bounds every caller's together
	dictServerBytes = 256 << 20
	// dictClientBytes bounds what this side keeps of bodies it sent
	dictClientBytes = 128 << 20
	// dictShareMin is the fewest of a body's anchors, about one a KB, a
	// kept body has to share to be its dictionary: the one sharing the
	// most is, however much is new (a screenshot added), as what it
	// shares goes as a few bytes, and a short system prompt repeated
	// has one anchor
	dictShareMin = 1
)

// dczMagic starts a "dcz" body (RFC 9842 §4.2), before the dictionary's
// SHA-256.
var dczMagic = []byte{0x5e, 0x2a, 0x4d, 0x18, 0x20, 0x00, 0x00, 0x00}

const dczHeaderLen = 8 + sha256.Size

type bodySum [sha256.Size]byte

// sfBinary is sum as an RFC 8941 byte sequence, as RFC 9842 writes it.
func sfBinary(sum bodySum) string { return ":" + base64.StdEncoding.EncodeToString(sum[:]) + ":" }

func parseSFBinary(v string) (bodySum, bool) {
	var sum bodySum
	v = strings.TrimSpace(v)
	if len(v) < 2 || v[0] != ':' || v[len(v)-1] != ':' {
		return sum, false
	}
	b, err := base64.StdEncoding.DecodeString(v[1 : len(v)-1])
	if err != nil || len(b) != sha256.Size {
		return sum, false
	}
	copy(sum[:], b)
	return sum, true
}

// dictWindow is the zstd window a body compressed against dict goes with:
// RFC 9842's, the larger of 8 MB and 1.25 times the dictionary, so every
// byte of it can be reached.
func dictWindow(dict int) int {
	n := max(8<<20, dict+dict/4)
	return 1 << bits.Len(uint(n-1))
}

// dczEncode is body as "dcz" against dict.
func dczEncode(body, dict []byte) ([]byte, error) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderDictRaw(0, dict), zstd.WithWindowSize(dictWindow(len(dict))),
		zstd.WithEncoderConcurrency(1), zstd.WithEncoderCRC(true), zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, err
	}
	defer enc.Close()
	sum := sha256.Sum256(dict)
	out := make([]byte, 0, dczHeaderLen+len(body)/16+256)
	out = append(append(out, dczMagic...), sum[:]...)
	return enc.EncodeAll(body, out), nil
}

// dczReader reads a "dcz" body r against dict, whose SHA-256 its header
// has to name.
func dczReader(r io.Reader, dict []byte) (io.ReadCloser, error) {
	head := make([]byte, dczHeaderLen)
	if _, err := io.ReadFull(r, head); err != nil {
		return nil, errDictMiss
	}
	sum := sha256.Sum256(dict)
	if !bytes.Equal(head[:8], dczMagic) || !bytes.Equal(head[8:], sum[:]) {
		return nil, errDictMiss
	}
	d, err := zstd.NewReader(r, zstd.WithDecoderDictRaw(0, dict), zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxWindow(uint64(dictWindow(dictMax))), zstd.WithDecoderMaxMemory(defaultBodyLimit))
	if err != nil {
		return nil, err
	}
	return d.IOReadCloser(), nil
}

var errDictMiss = errors.New("magpie doesn't hold the dictionary this body was compressed against, or it doesn't decode against it; send it whole")

// anchors are the places in a body chosen by its content alone, each
// known by a hash of the bytes after it: two bodies that share content
// share its anchors wherever it sits in each, a cheap measure of how
// much one compresses against the other.
type anchors map[uint64]struct{}

var (
	gear       [256]uint64
	anchorSeed = maphash.MakeSeed()
)

func init() {
	x := uint64(0x9e3779b97f4a7c15)
	for i := range gear {
		// splitmix64
		x += 0x9e3779b97f4a7c15
		z := x
		z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
		z = (z ^ (z >> 27)) * 0x94d049bb133111eb
		gear[i] = z ^ (z >> 31)
	}
}

const (
	anchorMask = 1<<10 - 1 // one in about 1 KB
	anchorSpan = 48
	anchorMost = 1 << 16
)

func anchorsOf(b []byte) anchors {
	a := anchors{}
	var h uint64
	for i, c := range b {
		h = h<<1 + gear[c]
		if h&anchorMask != 0 || i+anchorSpan > len(b) {
			continue
		}
		a[maphash.Bytes(anchorSeed, b[i:i+anchorSpan])] = struct{}{}
		if len(a) >= anchorMost {
			break
		}
	}
	return a
}

// shared is how many of a's anchors b has too.
func (a anchors) shared(b anchors) int {
	n := 0
	for k := range a {
		if _, ok := b[k]; ok {
			n++
		}
	}
	return n
}

// sentBodies is what this side keeps of the bodies it sent remote
// magpies that kept them, per route (prevRoute: the provider and its key).
type sentBodies struct {
	mu     sync.Mutex
	routes map[string][]*sentDict // the newest first
	speaks map[string]bool        // routes whose magpie said it keeps bodies
	size   int
	now    func() time.Time
}

type sentDict struct {
	sum  bodySum
	body []byte
	at   anchors
	when time.Time
}

var sentDicts = newSentBodies()

func newSentBodies() *sentBodies {
	return &sentBodies{routes: map[string][]*sentDict{}, speaks: map[string]bool{}, now: time.Now}
}

// base is the kept body of route that body shares the most with, nil for
// none sharing dictShareMin anchors; speaks, whether its magpie keeps bodies.
func (s *sentBodies) base(route string, at anchors) (best *sentDict, speaks bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	most := dictShareMin - 1
	for _, d := range s.routes[route] {
		if s.now().Sub(d.when) > dictTTL {
			continue
		}
		if v := at.shared(d.at); v > most {
			best, most = d, v
		}
	}
	return best, s.speaks[route]
}

func (s *sentBodies) put(route string, d *sentDict) {
	if len(d.body) > dictMax {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.speaks[route] = true
	ds := s.routes[route]
	for i, o := range ds {
		if o.sum == d.sum {
			ds = append(ds[:i], ds[i+1:]...)
			s.size -= len(o.body)
			break
		}
	}
	ds = append([]*sentDict{d}, ds...)
	s.size += len(d.body)
	for len(ds) > dictPerScope {
		s.size -= len(ds[len(ds)-1].body)
		ds = ds[:len(ds)-1]
	}
	s.routes[route] = ds
	// over the bound in all: the oldest of every route goes first
	for s.size > dictClientBytes {
		var oldR string
		var old *sentDict
		for r, ds := range s.routes {
			if last := ds[len(ds)-1]; old == nil || last.when.Before(old.when) {
				oldR, old = r, last
			}
		}
		ds := s.routes[oldR]
		s.size -= len(old.body)
		if ds = ds[:len(ds)-1]; len(ds) == 0 {
			delete(s.routes, oldR)
		} else {
			s.routes[oldR] = ds
		}
	}
}

// forget lets go of a route's kept bodies, after its magpie didn't hold
// one; it still speaks.
func (s *sentBodies) forget(route string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.routes[route] {
		s.size -= len(d.body)
	}
	delete(s.routes, route)
}

// unspoken: the route's magpie couldn't read the encoding at all (a
// magpie gone back to a version from before this): its bodies go as
// they are until it says again that it keeps them.
func (s *sentBodies) unspoken(route string) {
	s.forget(route)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.speaks, route)
}

// dictSend is one request to a remote magpie and how its body went.
type dictSend struct {
	route string
	body  []byte
	sum   bodySum
	at    anchors
	enc   string // "", "zstd" or "dcz"
}

// sendDict puts body on req as it goes to remote magpie p: offered kept,
// and against a body the other magpie keeps, or zstd, when it said it
// keeps bodies. It returns the bytes that go on the wire, and nil when
// the request is not one for this.
func sendDict(p provider.Provider, req *http.Request, body []byte) (*dictSend, int) {
	if !p.IsRemoteMagpie() || len(body) < dictMin || req.Method != http.MethodPost {
		return nil, len(body)
	}
	if enc := req.Header.Get("Content-Encoding"); enc != "" && !strings.EqualFold(enc, "identity") {
		return nil, len(body)
	}
	d := &dictSend{route: prevRoute(p), body: body, sum: sha256.Sum256(body), at: anchorsOf(body)}
	req.Header.Set(KeepBodyHeader, "dcz")
	base, speaks := sentDicts.base(d.route, d.at)
	if !speaks {
		return d, len(body)
	}
	if base != nil {
		if z, err := dczEncode(body, base.body); err == nil && len(z) < len(body) {
			provider.SetBody(req, z)
			req.Header.Set("Content-Encoding", "dcz")
			req.Header.Set(availableDictHeader, sfBinary(base.sum))
			req.Header.Set(BodyDigestHeader, sfBinary(d.sum))
			d.enc = "dcz"
			return d, len(z)
		}
	}
	z := zstdEncode(body)
	if len(z) >= len(body) {
		return d, len(body)
	}
	provider.SetBody(req, z)
	req.Header.Set("Content-Encoding", "zstd")
	d.enc = "zstd"
	return d, len(z)
}

var plainZstd = sync.OnceValue(func() *zstd.Encoder {
	e, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		panic(err)
	}
	return e
})

func zstdEncode(b []byte) []byte { return plainZstd().EncodeAll(b, make([]byte, 0, len(b)/4+64)) }

// after reads the other magpie's answer to the request: a body it
// couldn't decode goes again whole, by again; a body it kept is kept
// here too.
func (d *dictSend) after(req *http.Request, res *http.Response, err error, again func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	if err != nil || res == nil {
		return res, err
	}
	if d.enc != "" && (res.Header.Get(DictMissHeader) != "" || res.StatusCode == http.StatusUnsupportedMediaType) {
		if res.StatusCode == http.StatusUnsupportedMediaType {
			sentDicts.unspoken(d.route)
		} else {
			sentDicts.forget(d.route)
		}
		io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		res.Body.Close()
		provider.SetBody(req, d.body)
		req.Header.Del("Content-Encoding")
		req.Header.Del(availableDictHeader)
		req.Header.Del(BodyDigestHeader)
		d.enc = ""
		if res, err = again(req); err != nil || res == nil {
			return res, err
		}
	}
	for _, v := range res.Header.Values(KeptBodyHeader) {
		if sum, ok := parseSFBinary(v); ok && sum == d.sum {
			sentDicts.put(d.route, &sentDict{sum: d.sum, body: bytes.Clone(d.body), at: d.at, when: sentDicts.now()})
			break
		}
	}
	res.Header.Del(KeptBodyHeader)
	return res, nil
}

// keptBodies is what a magpie keeps of the bodies other magpies offered,
// per scope (dictScope), the least lately used going first over a bound.
type keptBodies struct {
	mu     sync.Mutex
	scopes map[string]map[bodySum]*list.Element
	lru    list.List // of *heldDict, the front the latest used
	size   int
	now    func() time.Time
}

type heldDict struct {
	scope string
	sum   bodySum
	body  []byte
	when  time.Time
}

var heldDicts = newKeptBodies()

func newKeptBodies() *keptBodies {
	return &keptBodies{scopes: map[string]map[bodySum]*list.Element{}, now: time.Now}
}

func (k *keptBodies) get(scope string, sum bodySum) []byte {
	k.mu.Lock()
	defer k.mu.Unlock()
	el, ok := k.scopes[scope][sum]
	if !ok {
		return nil
	}
	b := el.Value.(*heldDict)
	if k.now().Sub(b.when) > dictTTL {
		k.drop(el)
		return nil
	}
	b.when = k.now()
	k.lru.MoveToFront(el)
	return b.body
}

func (k *keptBodies) put(scope string, body []byte) bodySum {
	sum := sha256.Sum256(body)
	k.mu.Lock()
	defer k.mu.Unlock()
	if el, ok := k.scopes[scope][sum]; ok {
		el.Value.(*heldDict).when = k.now()
		k.lru.MoveToFront(el)
		return sum
	}
	if k.scopes[scope] == nil {
		k.scopes[scope] = map[bodySum]*list.Element{}
	}
	k.scopes[scope][sum] = k.lru.PushFront(&heldDict{scope: scope, sum: sum, body: body, when: k.now()})
	k.size += len(body)
	// the scope's own bounds, then every scope's
	n, held := 0, 0
	for el := k.lru.Front(); el != nil; {
		next := el.Next()
		if b := el.Value.(*heldDict); b.scope == scope {
			n, held = n+1, held+len(b.body)
			if n > dictPerScope || held > dictScopeBytes || k.now().Sub(b.when) > dictTTL {
				k.drop(el)
			}
		}
		el = next
	}
	for k.size > dictServerBytes && k.lru.Len() > 0 {
		k.drop(k.lru.Back())
	}
	return sum
}

func (k *keptBodies) drop(el *list.Element) {
	b := k.lru.Remove(el).(*heldDict)
	k.size -= len(b.body)
	delete(k.scopes[b.scope], b.sum)
	if len(k.scopes[b.scope]) == 0 {
		delete(k.scopes, b.scope)
	}
}

// dictScope is whose a kept body is: the gateway key the request came
// with and the address it came from. A LAN key is every computer's on
// the network, so the address tells them apart.
func dictScope(r *http.Request) string {
	return access.Caller(r.Context()).KeyID + "\x00" + clientIP(r)
}

type dictCtx struct{}

// dictOf is the dictionary keptBodyRequests found for r's "dcz" body.
func dictOf(ctx context.Context) []byte {
	b, _ := ctx.Value(dictCtx{}).([]byte)
	return b
}

// keptBodyRequests keeps the body of another magpie's request that offers
// it, and decodes one sent "dcz" against a body it kept, before the
// request's key limit and route read it. Its headers come off every
// request, so nothing passes them on.
func keptBodyRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offered := r.Header.Get(KeepBodyHeader) != ""
		avail, digest := r.Header.Get(availableDictHeader), r.Header.Get(BodyDigestHeader)
		r.Header.Del(KeepBodyHeader)
		r.Header.Del(availableDictHeader)
		r.Header.Del(BodyDigestHeader)
		dcz := strings.EqualFold(strings.TrimSpace(r.Header.Get("Content-Encoding")), "dcz")
		if r.Method != http.MethodPost || !magpieAsks(r) || !offered && !dcz {
			next.ServeHTTP(w, r)
			return
		}
		scope := dictScope(r)
		miss := func(why string) {
			w.Header().Set(DictMissHeader, "1")
			writeError(w, protoOfPath(r.URL.Path), http.StatusBadRequest, why)
		}
		var want bodySum
		if dcz {
			sum, ok := parseSFBinary(avail)
			var dict []byte
			if ok {
				dict = heldDicts.get(scope, sum)
			}
			if want, ok = parseSFBinary(digest); dict == nil || !ok {
				// before the body is read: nothing of it is used
				w.Header().Set("Connection", "close")
				miss(errDictMiss.Error())
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), dictCtx{}, dict))
		}
		var most int64
		if strings.HasPrefix(r.URL.Path, CodexPath+"/") {
			most = codexBodyLimit()
		}
		b, status, err := readBoundedRequestBody(w, r, requestLimits{body: most})
		if err != nil {
			if dcz && status == http.StatusBadRequest {
				miss(err.Error())
				return
			}
			writeError(w, protoOfPath(r.URL.Path), status, err.Error())
			return
		}
		if dcz && sha256.Sum256(b) != want {
			miss("the body decoded isn't the one sent; send it whole")
			return
		}
		r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(b)), int64(len(b))
		r.Header.Del("Content-Length")
		if offered && len(b) >= dictMin && len(b) <= dictMax {
			w.Header().Set(KeptBodyHeader, sfBinary(heldDicts.put(scope, b)))
		}
		next.ServeHTTP(w, r)
	})
}
