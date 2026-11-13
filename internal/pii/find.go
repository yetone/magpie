package pii

import (
	"container/list"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/redact"
)

// Find is redact's Model: what the model finds in each of texts. A
// request's history comes back turn after turn, so what was found in a
// text is kept (by its hash, on disk too: cache.go) and the model is
// asked only about texts it hasn't seen. Those go to it joined into
// chunks of up to chunkSize: one pass over a few joined texts takes about
// what one over a single short text does (100 short messages: 683 ms one
// by one, 44 ms joined, on an M4 Pro's GPU). A text longer than
// pieceSize goes in overlapping pieces (pieces), each kept on its own, so
// a long text is read a piece at a time and what was read is kept even
// when the rest isn't (#3682).
func Find(texts []string) ([][]redact.Found, error) {
	return FindContext(context.Background(), texts)
}

// FindContext is Find for a caller that may stop waiting: when ctx ends
// (the client went away, or the caller's own bound) it returns at once,
// whether its turn hadn't come or pf-serve is on it, with what was known
// of the texts by then and a *Partial. The chunk pf-serve is on is finished all the same
// and its answers kept, so the request sent again finds them; the chunks
// after it are left. Between chunks the turn is given up and taken again,
// so a short request waiting for it goes before the rest of a long one.
func FindContext(ctx context.Context, texts []string) ([][]redact.Found, error) {
	return pf.find(ctx, texts)
}

// backend is one model's side of FindContext: its turn and the requests
// waiting for it, what it found (cache, on disk at path), and how it is
// asked (classify, with turn held) unless it failed a while ago
// (heldBack).
type backend struct {
	turn     chan struct{}
	waiting  *atomic.Int32
	cache    *lru
	disk     *diskState
	path     func() string
	classify func([]string) ([][]redact.Found, error)
	heldBack func() error
	// chunk is how long a chunk of joined texts grows, and a long text's
	// piece is at most: chunkSize where 0. A request that stops waiting
	// leaves the model finishing the chunk it is on, so a model that reads
	// on the CPU has short ones.
	chunk int
	// sent are the texts and pieces a request went without the model's
	// reading (by hash): what a request sent unread before.
	sent sentSet
	// queue are the requests waiting for turn, the least left to read
	// first
	queue queue
	// reading are the texts and pieces requests read now: where turn
	// lets more than one read at once, a request with one of them waits
	// for that read instead of reading it beside it (subagents started
	// at once send one system prompt each)
	reading readingSet
}

func (b *backend) size() int {
	if b.chunk > 0 {
		return b.chunk
	}
	return chunkSize
}

// Bounded is ctx for a request that doesn't wait for every text it has
// to be read: masking's default, which waits a short while and lets the
// rules mask alone after it (internal/masking). Of what a request before
// it already went without the model's reading (an agent's history,
// resumed or never read, sent again on every turn), it reads backlogCap
// at most, after what is new, and answers with nothing found for the
// rest. Before, each request read such a history oldest first on every
// core, and a long one kept the CPU busy for as long as requests came
// while what was new in them waited behind it (xinvxueyuan and shensmart
// on Discord, 10-28: 13 cores of a 32-core Linux box).
func Bounded(ctx context.Context) context.Context { return context.WithValue(ctx, boundedKey{}, true) }

type boundedKey struct{}

// IsBounded says whether ctx is Bounded.
func IsBounded(ctx context.Context) bool { v, _ := ctx.Value(boundedKey{}).(bool); return v }

// backlogCap is how much of what was sent unread before a Bounded request
// reads: one chunk of AstrLink Guard's, so a backlog is still read, a
// little a request.
const backlogCap = guardChunk

// sentSet is a set of hashes, the newest sentMax or more of those added.
type sentSet struct {
	mu        sync.Mutex
	cur, prev map[[32]byte]bool
}

const sentMax = 50000

// mark adds ks, and says which were in the set already.
func (s *sentSet) mark(ks [][32]byte) map[[32]byte]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	was := map[[32]byte]bool{}
	for _, k := range ks {
		if s.cur[k] || s.prev[k] {
			was[k] = true
		}
	}
	for _, k := range ks {
		if s.cur == nil || len(s.cur) >= sentMax {
			s.prev, s.cur = s.cur, map[[32]byte]bool{}
		}
		s.cur[k] = true
	}
	return was
}

// pf is Privacy Filter's, run by pf-serve.
var pf = &backend{turn: turn, waiting: &waiting, cache: cache, disk: &disk, path: cachePath,
	classify: classify, heldBack: heldBack}

func (b *backend) find(ctx context.Context, texts []string) ([][]redact.Found, error) {
	b.loadCache()
	out, miss := b.known(texts, nil)
	if len(miss) == 0 {
		return out, nil
	}
	// what a request goes without the model's reading, or with it, is
	// sent now: what was sent unread before is the backlog
	lenient := IsBounded(ctx)
	var ks [][32]byte
	for _, u := range b.missing(texts, miss, nil) {
		ks = append(ks, sha256.Sum256([]byte(u)))
	}
	backlog := b.sent.mark(ks)
	// a request that waits for every text but has a bound (masking's
	// strict wait) stops waiting once only the backlog is left, and the
	// backlog is read on after it, to that bound (afterSent)
	until, early := ctx.Deadline()
	early = early && !lenient
	answered := false // early, by find's goroutine
	// how much of the texts this request has left for the model to read
	left := func(got map[[32]byte][]redact.Found, miss []int, read int) int {
		n, old := 0, 0
		for _, u := range b.missing(texts, miss, got) {
			if backlog[sha256.Sum256([]byte(u))] {
				old += len(u)
			} else {
				n += len(u)
			}
		}
		switch {
		case lenient:
			return n + min(old, max(0, backlogCap-read))
		case early && !answered:
			// it waits for what it adds alone: it goes before the
			// backlog an earlier request reads on into
			return n
		}
		return n + old
	}
	if ctx.Err() != nil {
		return b.partial(ctx, texts)
	}
	if err := b.heldBack(); err != nil {
		return nil, err
	}
	if !b.takeTurn(ctx, left(nil, miss, 0)) {
		return b.partial(ctx, texts)
	}
	type result struct {
		out [][]redact.Found
		err error
	}
	done := make(chan result, 1)
	go func() {
		held := true
		defer func() {
			if held {
				<-b.turn
			}
		}()
		// the request is answered once; after an early answer the reading
		// goes on under rctx, the request's bound without the request
		answer := func(r result) {
			if !answered {
				answered = true
				done <- r
			}
		}
		rctx := ctx
		after, stop := context.WithDeadline(context.WithoutCancel(ctx), until)
		defer stop()
		// what this call's chunks found, by hash, whatever the cache
		// lets go of meanwhile
		got := map[[32]byte][]redact.Found{}
		// read again: the requests before this one in turn had most of a
		// history in common with it, and asked already
		out, miss := b.known(texts, got)
		read := 0 // of the backlog
		for c := 0; len(miss) > 0; c++ {
			if c > 0 {
				// a caller that stopped waiting leaves the chunks after
				// this one: the turn isn't taken again for them (a
				// select on a free turn and an ended ctx picks either)
				if rctx.Err() != nil {
					answer(result{nil, errWaitEnded})
					return
				}
				// give way to a request waiting for the turn with less
				// to read than this one has left: it goes before the
				// next chunk
				n := left(got, miss, read)
				<-b.turn
				held = false
				if !b.takeTurn(rctx, n) {
					answer(result{nil, errWaitEnded})
					return
				}
				held = true
			}
			// the one ahead may have failed: those queued behind it
			// don't each try again, minutes apiece
			if err := b.heldBack(); err != nil {
				answer(result{nil, err})
				return
			}
			units := b.missing(texts, miss, got)
			if early && !answered && !slices.ContainsFunc(units, func(u string) bool { return !backlog[sha256.Sum256([]byte(u))] }) {
				answer(result{slices.Clone(out), &Partial{Missing: len(miss), Of: len(texts), Err: afterSent}})
				rctx = after
			}
			if lenient {
				// the backlog past its cap is left: its texts go with
				// what the rules find
				var keep []string
				for _, u := range units {
					if !backlog[sha256.Sum256([]byte(u))] || read < backlogCap {
						keep = append(keep, u)
					}
				}
				if len(keep) == 0 {
					break
				}
				units = keep
			}
			chunks, parts, wait, release := b.reading.claim(units, b.size())
			if len(chunks) == 0 && wait != nil {
				// another request reads what is left: what it finds
				// comes through the cache
				select {
				case <-wait:
				case <-rctx.Done():
					answer(result{nil, errWaitEnded})
					return
				}
				miss = b.still(texts, miss, got, out)
				continue
			}
			if len(chunks) == 0 {
				answer(result{nil, fmt.Errorf("the local masking model has no text to read for %d texts", len(miss))})
				return
			}
			found, err := classifySafely(b.classify, chunks[:1])
			if err != nil {
				release()
				answer(result{nil, err})
				return
			}
			b.cache.mu.Lock()
			for _, p := range parts[0] {
				k := sha256.Sum256([]byte(p.s))
				if backlog[k] {
					read += len(p.s)
				}
				got[k] = spread(p.s, within(found[0], p.at, p.at+len(p.s)))
				b.cache.put(k, got[k])
			}
			b.cache.mu.Unlock()
			release()
			b.saveSoon()
			miss = b.still(texts, miss, got, out)
		}
		answer(result{out, nil})
	}()
	select {
	case r := <-done:
		if r.err == errWaitEnded {
			return b.partial(ctx, texts)
		}
		return r.out, r.err
	case <-ctx.Done():
		return b.partial(ctx, texts)
	}
}

// classifySafely is classify(texts), with a panic in it this request's
// error: it runs on a goroutine of the request's find, where a panic
// would end magpie and every request it serves (#4553, a stray byte in
// an embeddings input on Windows). The request is held back, as for a
// model that failed, and the model isn't counted as failed for the
// requests after it.
func classifySafely(classify func([]string) ([][]redact.Found, error), texts []string) (out [][]redact.Found, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("pii: the local model panicked reading a request: %v\n%s", r, debug.Stack())
			out, err = nil, fmt.Errorf("the local masking model failed reading this request (%v)", r)
		}
	}()
	return classify(texts)
}

// still is which of texts' miss are still missing, with got (this
// call's answers) and what others' requests found meanwhile; out gets
// what is known of the rest.
func (b *backend) still(texts []string, miss []int, got map[[32]byte][]redact.Found, out [][]redact.Found) []int {
	sub := make([]string, len(miss))
	for j, i := range miss {
		sub[j] = texts[i]
	}
	o, m := b.known(sub, got)
	for j, i := range miss {
		out[i] = o[j]
	}
	var left []int
	for _, j := range m {
		left = append(left, miss[j])
	}
	return left
}

// readingSet are the texts and pieces requests read now, by hash, each
// with what is closed when its read ends. Its zero value is empty.
type readingSet struct {
	mu sync.Mutex
	m  map[[32]byte]chan struct{}
}

// claim leaves out of units those another request reads now, and
// claims the first chunk of the rest for this one: the rest's chunks and
// parts (joinAt, at size), what is closed when a read of one left out
// ends (nil when none was), and release, which ends this one's claim
// once what it found is in the cache.
func (r *readingSet) claim(units []string, size int) (chunks []string, parts [][]part, wait <-chan struct{}, release func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var rest []string
	for _, u := range units {
		if c, ok := r.m[sha256.Sum256([]byte(u))]; ok {
			if wait == nil {
				wait = c
			}
			continue
		}
		rest = append(rest, u)
	}
	chunks, parts = joinAt(rest, size)
	if len(chunks) == 0 {
		return nil, nil, wait, func() {}
	}
	if r.m == nil {
		r.m = map[[32]byte]chan struct{}{}
	}
	c := make(chan struct{})
	var ks [][32]byte
	for _, p := range parts[0] {
		k := sha256.Sum256([]byte(p.s))
		if _, ok := r.m[k]; !ok {
			r.m[k] = c
			ks = append(ks, k)
		}
	}
	return chunks, parts, wait, func() {
		r.mu.Lock()
		for _, k := range ks {
			delete(r.m, k)
		}
		r.mu.Unlock()
		close(c)
	}
}

// errWaitEnded is find's own word that its caller stopped waiting.
var errWaitEnded = errors.New("the wait ended")

// afterSent is a Partial's Err for a request that waits for every text
// (masking's strict wait) answered once all that is left is what an
// earlier request already sent without the model's reading. Such a text
// goes as it went on the same route (redact's sentSpans): what the model
// finds in it now changes nothing in it, and only makes known a value to
// mask in text after it (learnLate). So the request goes, and the model
// reads on to the request's bound, keeping what it finds for the next
// one. Before, a resumed or never-read history was read on every request
// until the strict wait's 15s ran out, newest first: on a 4-core Linux
// CPU a 60-turn Claude Code session resumed waited 15s, 15s, 15s and 7.8s
// on its first four requests, each for text the vendor already had
// (gaubeebangeel on X, 11-13: first tokens at 30-40s with masking on,
// 10-15s off). On another route (the user picked another provider) such
// a text has the model's finds of now, and goes with the rules' alone
// until the model has read it.
var afterSent = errors.New("what is left was sent before without the model's reading, and is read after the request")

// Partial is Find's error when its caller stopped waiting (ctx ended)
// before the model had read every text. What Find returns with it is
// what was known of each text by then, read before or meanwhile, by
// this request or another's; a text the model hadn't read has nil, as
// one it found nothing in has. Before, such a wait gave nothing at all,
// not even for texts read turns ago, and a caller holding a request back
// for the model held it back whole (xRayySummers on X: a burst of
// requests with text the model hadn't read queued for up to 290s, and
// each that stopped waiting was "masking failed, so nothing was sent").
type Partial struct {
	// Missing of Of texts the model hadn't read
	Missing, Of int
	Err         error
}

func (p *Partial) Error() string {
	return fmt.Sprintf("the local masking model read %d of %d texts in time: %v", p.Of-p.Missing, p.Of, p.Err)
}

func (p *Partial) Unwrap() error { return p.Err }

// partial is what is known of texts now that ctx ended, and a Partial
// that says how much isn't.
func (b *backend) partial(ctx context.Context, texts []string) ([][]redact.Found, error) {
	out, miss := b.known(texts, nil)
	return out, &Partial{Missing: len(miss), Of: len(texts), Err: waitErr(ctx)}
}

// takeTurn waits for the model's turn while ctx lives, counted as
// waiting, for a request with left bytes still to read; false when ctx
// ended first. Of the requests waiting, the one with the least left takes
// the turn next (the first to come of those with as little): a request of
// a few lines goes before the next chunk of a long one, and of every
// other long one waiting. Before, they took it in the order they came,
// and a short request waited a chunk of each long one ahead of it: on a
// 4-core CPU, a second or more apiece, past masking's default wait of 2s
// with two ahead (wangwu25685333 on X, 10-29: requests of 1.4K tokens
// waited 8.5s behind a fresh session's 31.5K, and their client gave up at
// 20s).
func (b *backend) takeTurn(ctx context.Context, left int) bool {
	b.waiting.Add(1)
	defer b.waiting.Add(-1)
	w := b.queue.add(left)
	defer b.queue.remove(w)
	for {
		next, changed := b.queue.next(w)
		if !next {
			select {
			case <-changed:
				continue
			case <-ctx.Done():
				return false
			}
		}
		select {
		case b.turn <- struct{}{}:
			return true
		case <-changed:
		case <-ctx.Done():
			return false
		}
	}
}

// queue are the requests waiting for a backend's turn. Its zero value is
// empty.
type queue struct {
	mu      sync.Mutex
	seq     uint64
	waiters map[*waiter]bool
	// changed is closed when a request comes or goes, and made again
	changed chan struct{}
}

// waiter is a request in a queue: left bytes to read, seq-th to come.
type waiter struct {
	left int
	seq  uint64
}

// add puts a request with left bytes to read in q.
func (q *queue) add(left int) *waiter {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.seq++
	w := &waiter{left, q.seq}
	if q.waiters == nil {
		q.waiters = map[*waiter]bool{}
	}
	q.waiters[w] = true
	q.changedLocked()
	return w
}

// remove takes w out of q.
func (q *queue) remove(w *waiter) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.waiters, w)
	q.changedLocked()
}

// changedLocked wakes those waiting on q to look again; q.mu is held.
func (q *queue) changedLocked() {
	if q.changed != nil {
		close(q.changed)
	}
	q.changed = make(chan struct{})
}

// next says whether w is the one to take the turn next, and gives what
// is closed when that may have changed.
func (q *queue) next(w *waiter) (bool, <-chan struct{}) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for o := range q.waiters {
		if o.left < w.left || o.left == w.left && o.seq < w.seq {
			return false, q.changed
		}
	}
	return true, q.changed
}

// waitErr is a request's that stopped waiting for the model.
func waitErr(ctx context.Context) error {
	return fmt.Errorf("stopped waiting for the local masking model: %w", context.Cause(ctx))
}

// known is what is known of texts, from got (this call's answers) and the
// cache, and which texts it isn't. A text kept whole is taken as it is;
// a long one is put together from its pieces, once all of them are known,
// and kept whole then too.
func (b *backend) known(texts []string, got map[[32]byte][]redact.Found) (out [][]redact.Found, miss []int) {
	out = make([][]redact.Found, len(texts))
	b.cache.mu.Lock()
	defer b.cache.mu.Unlock()
	look := func(s string) ([]redact.Found, bool) {
		k := sha256.Sum256([]byte(s))
		if fs, ok := got[k]; ok {
			return fs, true
		}
		if fs, ok := b.cache.get(k); ok && fits(fs, s) {
			return fs, true
		}
		return nil, false
	}
	for i, t := range texts {
		if fs, ok := look(t); ok {
			out[i] = fs
			continue
		}
		ps := cut(t, b.size())
		if len(ps) < 2 {
			miss = append(miss, i)
			continue
		}
		var all []redact.Found
		ok := true
		for _, p := range ps {
			fs, have := look(t[p.at:p.end])
			if !have {
				ok = false
				break
			}
			for _, f := range fs {
				all = append(all, redact.Found{Start: f.Start + p.at, End: f.End + p.at, Label: f.Label, Score: f.Score})
			}
		}
		if !ok {
			miss = append(miss, i)
			continue
		}
		out[i] = spread(t, merge(all))
		b.cache.put(sha256.Sum256([]byte(t)), out[i])
	}
	return out, miss
}

// fits says every span of fs is inside t: one read from a damaged file
// that isn't is asked about again.
func fits(fs []redact.Found, t string) bool {
	for _, f := range fs {
		if f.Start < 0 || f.End > len(t) || f.Start >= f.End {
			return false
		}
	}
	return true
}

// chunkSize is how long a chunk of joined texts grows, by default
// (Privacy Filter's: backend.chunk).
const chunkSize = 12 << 10

// guardChunk is AstrLink Guard's: about 1,200 tokens, three of its
// windows, a second on 4 cores where 12 KB took several.
const guardChunk = 4 << 10

// pieceSize is how long a piece of a long text is at most, and
// pieceOverlap how much of the piece before it each piece after the first
// reads again, so a name or a secret cut by one piece's end is whole in
// the next. 12 KB is 1.6s of an RX 470/580's time, 0.4s of an M4 Pro's,
// well inside classifyTimeout; a 480 KB text whole was 90s on the RX
// 470/580, past the gateway's 2 minutes on its CPU, and kept nothing when
// cut off (#3682).
const (
	pieceSize    = chunkSize
	pieceOverlap = 1 << 10
)

// span is a piece of a text: t[at:end].
type span struct{ at, end int }

// pieces cuts t into pieces of at most pieceSize, each after the first
// beginning pieceOverlap before the one before ends. A piece ends after a
// line's end in its second half, else after a space there (which one is
// the text's own choice: landmark), else at a rune's start; the next
// begins after a line's end in the first half of the overlap, else after
// a space there, else at a rune's start. A text of pieceSize or less is
// one piece; an empty one none.
func pieces(t string) []span { return cut(t, pieceSize) }

// cut is pieces, of at most size each.
func cut(t string, size int) []span {
	if t == "" {
		return nil
	}
	if len(t) <= size {
		return []span{{0, len(t)}}
	}
	var out []span
	for at := 0; ; {
		end := at + size
		if end >= len(t) {
			return append(out, span{at, len(t)})
		}
		half := at + size/2
		if i := landmark(t, half, end, '\n'); i >= 0 {
			end = i
		} else if i := landmark(t, half, end, ' '); i >= 0 {
			end = i
		} else {
			for end > half && !utf8.RuneStart(t[end]) {
				end--
			}
		}
		out = append(out, span{at, end})
		next := end - pieceOverlap
		if i := strings.IndexByte(t[next:end-pieceOverlap/2], '\n'); i >= 0 {
			next += i + 1
		} else if i := strings.IndexByte(t[next:end-pieceOverlap/2], ' '); i >= 0 {
			next += i + 1
		} else {
			// to end at most: text that isn't UTF-8 can have no rune's
			// start up to it, or to the text's end (a run of stray
			// continuation bytes read past it and panicked, #4553)
			for next < end && !utf8.RuneStart(t[next]) {
				next++
			}
		}
		at = next
	}
}

// landmark is where in t[from:to] a piece ends after c: of the places
// after a c there, the one whose landmarkLen bytes before it hash least;
// -1 where there is no c. The choice is the text's own, not its distance
// from the piece's start, so a text edited before it (a line added to an
// agent's memory, a name in its workspace changed) is cut at the same
// places after the edit once one end meets an end of before, and those
// pieces are known by hash. Before, a piece ended after the last c
// there, and an edit that moved what followed by a few bytes moved every
// piece after it: of a 40 KB system prompt's 13 pieces, a fresh session
// of the same agent had 5 read again on average and all 13 at worst for
// a line of it edited (wangwu25685333 on X, 10-29: 98.8s of a
// 31.5K-token Hermes request on a 4-core CPU). Now 1.6 and 3 of 18.
func landmark(t string, from, to int, c byte) int {
	best, at := uint64(0), -1
	for i := from; i < to; i++ {
		if t[i] != c {
			continue
		}
		e := i + 1
		h := uint64(14695981039346656037) // FNV-1a
		for _, b := range []byte(t[max(0, e-landmarkLen):e]) {
			h ^= uint64(b)
			h *= 1099511628211
		}
		if at < 0 || h < best {
			best, at = h, e
		}
	}
	return at
}

// landmarkLen is how much of the text before a place landmark hashes:
// enough that two lines alike but for a word or a number differ.
const landmarkLen = 48

// spread is fs with each value they found (minSpread bytes or more) found
// wherever else it is in t, as sure as the surest of its finds there, and
// of its label. The model is less sure of a value the more often it has
// read it in a text: an address on 400 lines alike was found on every
// line, at 0.90 on some and 0.50 on most, and masking (0.8) left 346 of
// them as they were (wangwu25685333 on X, 10-29: "a few of 400",
// thought to be where the text was cut; the 400 were left alike with the
// pieces cut elsewhere). A value found but not sure anywhere stays as
// unsure as it was.
func spread(t string, fs []redact.Found) []redact.Found {
	type find struct {
		label string
		score float64
	}
	best := map[string]find{}
	for _, f := range fs {
		v := t[f.Start:f.End]
		if len(v) < minSpread {
			continue
		}
		if b, ok := best[v]; !ok || f.Score > b.score {
			best[v] = find{f.Label, f.Score}
		}
	}
	if len(best) == 0 {
		return fs
	}
	out := slices.Clone(fs)
	more := false
	for v, b := range best {
		for at := 0; ; {
			i := strings.Index(t[at:], v)
			if i < 0 {
				break
			}
			out = append(out, redact.Found{Start: at + i, End: at + i + len(v), Label: b.label, Score: b.score})
			more = true
			at += i + len(v)
		}
	}
	if !more {
		return fs
	}
	return merge(out)
}

// minSpread is the shortest value spread finds again: what redact matches
// a value known before at (minKnown), so a short number or a name's
// first word isn't found all over a text.
const minSpread = 8

// merge puts a long text's spans from its pieces together: spans that
// overlap (one found in two pieces, or cut by a piece's end in one and
// whole in the next) become one, its label the longer's, and spans of one
// label that touch become one. Sorted, with none twice.
func merge(fs []redact.Found) []redact.Found {
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].Start != fs[j].Start {
			return fs[i].Start < fs[j].Start
		}
		return fs[i].End > fs[j].End
	})
	var out []redact.Found
	for _, f := range fs {
		if n := len(out); n > 0 {
			last := &out[n-1]
			if f.Start < last.End || f.Start == last.End && f.Label == last.Label {
				if f.End-f.Start > last.End-last.Start {
					last.Label = f.Label
				}
				last.End = max(last.End, f.End)
				// as sure as the surer: what either would mask is masked
				last.Score = max(last.Score, f.Score)
				continue
			}
		}
		out = append(out, f)
	}
	return out
}

// missing are what of texts[miss] neither got nor the cache has, to ask
// the model about: the texts whole, and the long ones' pieces, each once,
// the last text's first: an agent's history is oldest first, and what is
// new in a request is at its end.
func (b *backend) missing(texts []string, miss []int, got map[[32]byte][]redact.Found) []string {
	var out []string
	seen := map[[32]byte]bool{}
	b.cache.mu.Lock()
	defer b.cache.mu.Unlock()
	for j := len(miss) - 1; j >= 0; j-- {
		t := texts[miss[j]]
		ps := cut(t, b.size())
		for q := len(ps) - 1; q >= 0; q-- {
			p := ps[q]
			s := t[p.at:p.end]
			k := sha256.Sum256([]byte(s))
			if seen[k] {
				continue
			}
			seen[k] = true
			if _, ok := got[k]; ok {
				continue
			}
			if fs, ok := b.cache.get(k); ok && fits(fs, s) {
				continue
			}
			out = append(out, s)
		}
		if t == "" {
			// as before: an empty text is asked about, and kept
			k := sha256.Sum256(nil)
			if !seen[k] {
				seen[k] = true
				out = append(out, "")
			}
		}
	}
	return out
}

// sep is put between joined texts: a paragraph's end, so the model reads
// each as a text of its own.
const sep = "\n\n"

// part is a text or piece in a chunk: s, at at.
type part struct {
	s  string
	at int
}

// join puts texts into chunks, and says where in which each is.
func join(texts []string) (chunks []string, parts [][]part) { return joinAt(texts, chunkSize) }

// joinAt is join, its chunks up to size.
func joinAt(texts []string, size int) (chunks []string, parts [][]part) {
	cur, curParts := "", []part(nil)
	flush := func() {
		if curParts != nil {
			chunks, parts = append(chunks, cur), append(parts, curParts)
			cur, curParts = "", nil
		}
	}
	for _, t := range texts {
		if curParts != nil && len(cur)+len(sep)+len(t) > size {
			flush()
		}
		if curParts != nil {
			cur += sep
		}
		curParts = append(curParts, part{t, len(cur)})
		cur += t
	}
	flush()
	return chunks, parts
}

// within are the spans of fs inside [a, b), made relative to a; one that
// runs past either end, over into the text beside it, is cut at the end.
func within(fs []redact.Found, a, b int) []redact.Found {
	var out []redact.Found
	for _, f := range fs {
		s, e := max(f.Start, a), min(f.End, b)
		if s < e {
			out = append(out, redact.Found{Start: s - a, End: e - a, Label: f.Label, Score: f.Score})
		}
	}
	return out
}

// cache keeps what was found in the texts seen last, by their hashes.
var cache = newLRU(20000)

type lru struct {
	mu  sync.Mutex
	max int
	ll  *list.List
	m   map[[32]byte]*list.Element
}

type entry struct {
	k  [32]byte
	fs []redact.Found
}

func newLRU(n int) *lru { return &lru{max: n, ll: list.New(), m: map[[32]byte]*list.Element{}} }

func (c *lru) get(k [32]byte) ([]redact.Found, bool) {
	e, ok := c.m[k]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(e)
	return e.Value.(*entry).fs, true
}

func (c *lru) put(k [32]byte, fs []redact.Found) {
	if e, ok := c.m[k]; ok {
		c.ll.MoveToFront(e)
		e.Value.(*entry).fs = fs
		return
	}
	c.m[k] = c.ll.PushFront(&entry{k, fs})
	for c.ll.Len() > c.max {
		e := c.ll.Back()
		c.ll.Remove(e)
		delete(c.m, e.Value.(*entry).k)
	}
}
