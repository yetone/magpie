package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/masking"
	"github.com/yetone/magpie/internal/provider"
)

// An agent's request that magpie holds before routing it went nowhere a
// user could see (heyusaku6 on X): Codex thought for over ten minutes on a
// routing group's model, then on two others, the Routing page showed none
// of them, and only a restart of Codex and magpie ended it. Codex waits for
// a reply's headers with no limit of its own, and a request shows on the
// Routing page once a route is begun for it, so whatever held it before
// that (a lock, a wait with no end) held it in silence.
//
// What held it isn't known. So a request with no route begun for it is
// shown on the Routing page as routing once it has waited unroutedShow,
// and is answered with an error once it has waited unroutedMost, with
// magpie's log saying where every goroutine stood. Its context ends then
// too, so what waits on it lets go.
//
// Two minutes of a spinner before that error was a torment of its own
// (sharwowwow on X). Most of what comes before a route is a wait magpie
// knows of and that may rightly be long: the body still arriving (an
// image over a slow link), masking (the local model), the routing
// group's decision model, an image described for a model that can't see,
// a Codex reset spent, the plugins' gateway hooks and JS middleware, a
// slot or a minute's room (noteWait, notePhase). The rest is magpie's own
// work: parsing, the catalog, the plan. A request that has spent
// unroutedUnknown in none of those waits, added up however they came, is
// answered then with the same error; one in those waits still has
// unroutedMost. 30s is three times the slowest such work a report has
// measured (a cold catalog sync, ~8.5s) and leaves a loaded machine room.
//
// Masking's time, its wait for the local model's turn with it, counts
// toward neither (noteMasking). The model is slow but bounded: each piece
// of text it reads has classifyTimeout, a load gpuLoadTimeout or
// loadTimeout, and one that fails holds requests back at once
// (internal/pii). A 480 KB request on an RX 470/580 took 90s of the
// model's work, more on its CPU, and was answered 504 at 2 minutes, its
// work thrown away, however often it was sent again (#3682). What isn't
// masking still has unroutedMost, with bounded upload credit below, so
// a hook or a describe that hangs after masking is still answered.
var unroutedShow, unroutedUnknown, unroutedMost = 15 * time.Second, 30 * time.Second, 2 * time.Minute

// Uploads earn time at 32 KiB/s, only for time actually spent reading
// wire bytes. A large conversation over a slow link can finish (#4775),
// while a trickle still runs out of the ordinary two-minute hold. The
// socket's idle deadline and the decoded body size limit also still hold.
// Even compressed input that produces no decoded bytes can earn no more
// than eight minutes, bounding a non-masking hold to ten minutes overall.
// The grace is a var only for tests, which scale it with unroutedMost; it
// is counted in whole seconds.
const unroutedUploadRate = 32 << 10

var unroutedUploadGrace = 8 * time.Minute

// unroutedWatched is whether a request to path is an agent's turn, which
// is given a route once magpie knows where it goes: Chat, Responses,
// Messages, Gemini and Codex's own backend. A compaction or a model list
// has no route of its own, and an MCP or a search call is not a turn.
func unroutedWatched(path string) bool {
	switch path {
	case "/v1/chat/completions", "/chat/completions", "/v1/responses", "/responses",
		"/v1/messages", "/messages", CodexPath + "/responses":
		return true
	}
	return strings.HasPrefix(path, "/v1beta/models/") && (strings.HasSuffix(path, ":generateContent") || strings.HasSuffix(path, ":streamGenerateContent"))
}

// unroutedProto is the API path's client speaks, for its error.
func unroutedProto(path string) provider.Protocol {
	switch {
	case strings.HasSuffix(path, "/messages"):
		return provider.Anthropic
	case strings.HasPrefix(path, "/v1beta/"):
		return provider.Gemini
	case strings.HasSuffix(path, "/responses"):
		return provider.Responses
	}
	return provider.Chat
}

// errUnrouted ends a request's context when magpie stopped holding it.
var errUnrouted = errors.New("magpie held the request too long before routing it")

// heldWriter is the client's writer while magpie may still answer for its
// handler: what the handler writes goes through it, and once magpie has
// answered (gaveUp) the handler's writes go nowhere. The handler's headers
// are its own until it writes, so magpie's answer never shares a map
// another goroutine is filling.
type heldWriter struct {
	w      http.ResponseWriter
	mu     sync.Mutex
	hdr    http.Header
	wrote  bool // the handler began its reply
	gaveUp bool // magpie answered for it
}

func newHeldWriter(w http.ResponseWriter) *heldWriter {
	return &heldWriter{w: w, hdr: http.Header{}}
}

func (h *heldWriter) Header() http.Header {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.wrote {
		return h.w.Header()
	}
	return h.hdr
}

// begin is the handler's reply starting: its headers go to the client's.
// Called with mu held; false once magpie has answered.
func (h *heldWriter) begin() bool {
	if h.gaveUp {
		return false
	}
	if !h.wrote {
		h.wrote = true
		out := h.w.Header()
		for k, vs := range h.hdr {
			out[k] = vs
		}
	}
	return true
}

func (h *heldWriter) WriteHeader(code int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.begin() {
		h.w.WriteHeader(code)
	}
}

func (h *heldWriter) Write(b []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.begin() {
		return 0, http.ErrHandlerTimeout
	}
	return h.w.Write(b)
}

func (h *heldWriter) Flush() { _ = h.FlushError() }

// FlushError is http.ResponseController's Flush, which looks for it
// before it unwraps.
func (h *heldWriter) FlushError() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.begin() {
		return http.ErrHandlerTimeout
	}
	return http.NewResponseController(h.w).Flush()
}

// Unwrap is for http.ResponseController's deadlines.
func (h *heldWriter) Unwrap() http.ResponseWriter { return h.w }

// answer writes magpie's own reply, whole, unless the handler began one.
// Its length is said, so the client has all of it while the handler may
// still be held: a reply of no length ends only when the handler does.
func (h *heldWriter) answer(proto provider.Protocol, status int, msg string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.wrote || h.gaveUp {
		return false
	}
	h.gaveUp = true
	rec := httptest.NewRecorder()
	writeError(rec, proto, status, msg)
	out := h.w.Header()
	for k, vs := range rec.Header() {
		out[k] = vs
	}
	out.Set("Content-Length", strconv.Itoa(rec.Body.Len()))
	out.Set("Connection", "close")
	h.w.WriteHeader(status)
	h.w.Write(rec.Body.Bytes())
	_ = http.NewResponseController(h.w).Flush()
	return true
}

// unroutedHold is one watched request's watch: the route that shows it
// while it waits, and what ends it.
type unroutedHold struct {
	s *Server
	r *http.Request
	f *inflight
	// agent is who it is shown as (callerFrom), read as it comes: the
	// timers never read its headers, which withCaller and decodedBody
	// change meanwhile
	agent  string
	w      *heldWriter
	cancel context.CancelCauseFunc
	asked  atomic.Value // string: the model it asked for, once read
	shown  *Route       // under f.mu
	ended  bool         // under f.mu: answered by magpie
	over   bool         // under f.mu: its handler returned
	// masking counts the maskings under way for it: one held there waits
	// for Settings → Privacy's local model, and its error says so
	masking atomic.Int32
	// the body as it arrives (waitedBody): the bytes read, when the last
	// came (unix nanoseconds), whether a read met its end (or an error),
	// and stopped once magpie answered for it while it was still
	// arriving, so its handler reads no more of it
	bodyRead    atomic.Int64
	bodyLast    atomic.Int64
	bodyEnded   atomic.Bool
	bodyStopped atomic.Bool

	// the waits magpie knows it is in (noteWait), under wmu: how many are
	// under way, since when one has been, the time spent in one before
	// that, and whether unknown fired in one and looks again at its end
	wmu     sync.Mutex
	waits   int
	since   time.Time
	excused time.Duration
	due     bool
	unknown *time.Timer
	// each kind of known wait it was in, in the order first met, for the
	// report of where it was held (heldReport)
	kinds []*waitKind

	// the same for the maskings under way (noteMasking), whose time
	// counts toward unroutedMost no more: how many, since when, the time
	// spent masking before that, and whether most fired while masking
	mwaits   int
	msince   time.Time
	mexcused time.Duration
	mdue     bool
	most     *time.Timer
}

type unroutedKey struct{}

// noteAsked tells ctx's watch the model its request asked for, for the
// route that shows it.
func noteAsked(ctx context.Context, model string) {
	if u, _ := ctx.Value(unroutedKey{}).(*unroutedHold); u != nil {
		u.asked.Store(model)
	}
}

// noteMasking tells ctx's watch its request is being masked, until the
// func it answers is called: a wait it knows of, whose time counts
// toward neither unroutedUnknown nor unroutedMost.
func noteMasking(ctx context.Context) func() {
	u, _ := ctx.Value(unroutedKey{}).(*unroutedHold)
	if u == nil {
		return func() {}
	}
	u.masking.Add(1)
	waited := u.wait("masking")
	u.wmu.Lock()
	if u.mwaits == 0 {
		u.msince = time.Now()
	}
	u.mwaits++
	u.wmu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			u.masking.Add(-1)
			waited()
			u.masked()
		})
	}
}

// masked ends a masking: the last to end adds its time to mexcused, and
// if most fired meanwhile it is set for what is left of it.
func (u *unroutedHold) masked() {
	u.wmu.Lock()
	defer u.wmu.Unlock()
	if u.mwaits--; u.mwaits > 0 {
		return
	}
	now := time.Now()
	u.mexcused += now.Sub(u.msince)
	if u.mdue {
		u.mdue = false
		u.most.Reset(max(unroutedMost-u.unmaskedLocked(now), 0))
	}
}

// unmaskedLocked is how long the request has been held other than
// masking and the upload time earned by received bytes; wmu is held.
func (u *unroutedHold) unmaskedLocked(now time.Time) time.Duration {
	d := now.Sub(u.f.start) - u.mexcused
	if u.mwaits > 0 {
		d -= now.Sub(u.msince)
	}
	for _, k := range u.kinds {
		if k.kind == "body" {
			spent := k.spent
			if k.n > 0 {
				spent += now.Sub(k.since)
			}
			// Clamp before multiplying: compressed wire bytes need not
			// be bounded by the decoded size cap, and may overflow time.
			read := min(u.bodyRead.Load(), int64(unroutedUploadGrace/time.Second)*unroutedUploadRate)
			earned := time.Duration(read) * time.Second / unroutedUploadRate
			d -= min(spent, earned)
			break
		}
	}
	return d
}

// checkMost is most's timer: a request held unroutedMost other than
// masking and earned upload time is answered for; one masking now is
// looked at again when that ends, and one with time left, when that is up,
// but no sooner than unroutedRecheck. While an upload earns at least as
// fast as it is read, its held time stands still, so what is left of it
// doesn't shrink: a sliver left would be looked at again every sliver for
// as long as the body kept coming.
func (u *unroutedHold) checkMost() {
	u.wmu.Lock()
	if u.mwaits > 0 {
		u.mdue = true
		u.wmu.Unlock()
		return
	}
	if left := unroutedMost - u.unmaskedLocked(time.Now()); left > 0 {
		u.most.Reset(max(left, unroutedRecheck()))
		u.wmu.Unlock()
		return
	}
	u.wmu.Unlock()
	u.giveUp(false)
}

// unroutedRecheck is the least checkMost waits before it looks again: a
// second, or an eighth of unroutedMost when a test makes that shorter. A
// hold is answered at most that late.
func unroutedRecheck() time.Duration { return min(time.Second, unroutedMost/8) }

// noteWait tells ctx's watch its request is in a wait magpie knows of
// until the func it answers is called: the time it spends there counts
// toward unroutedMost alone, not unroutedUnknown.
// kind names the wait in the report of a request held too long
// (heldReport): "body", "body room", "hooks" and the rest.
func noteWait(ctx context.Context, kind string) (done func()) {
	u, _ := ctx.Value(unroutedKey{}).(*unroutedHold)
	if u == nil {
		return func() {}
	}
	return u.wait(kind)
}

// waitKind is the time a request spent in one kind of known wait: how
// many of it are under way, since when, and the time before that.
type waitKind struct {
	kind  string
	n     int
	since time.Time
	spent time.Duration
}

// wait begins a known wait of kind, and answers the func that ends it,
// once. A kind of "" is counted but not named: a phase whose time its own
// wait names already.
func (u *unroutedHold) wait(kind string) func() {
	u.wmu.Lock()
	now := time.Now()
	if u.waits == 0 {
		u.since = now
	}
	u.waits++
	var k *waitKind
	if kind != "" {
		for _, x := range u.kinds {
			if x.kind == kind {
				k = x
			}
		}
		if k == nil {
			k = &waitKind{kind: kind}
			u.kinds = append(u.kinds, k)
		}
		if k.n++; k.n == 1 {
			k.since = now
		}
	}
	u.wmu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			if k != nil {
				u.wmu.Lock()
				if k.n--; k.n == 0 {
					k.spent += time.Since(k.since)
				}
				u.wmu.Unlock()
			}
			u.waited()
		})
	}
}

// waited ends a known wait: the last to end adds its time to excused,
// and if unknown fired meanwhile it is set for what is left of it.
func (u *unroutedHold) waited() {
	u.wmu.Lock()
	defer u.wmu.Unlock()
	if u.waits--; u.waits > 0 {
		return
	}
	now := time.Now()
	u.excused += now.Sub(u.since)
	if u.due {
		u.due = false
		u.unknown.Reset(max(unroutedUnknown-u.unknownLocked(now), 0))
	}
}

// unknownLocked is how long the request has been held in no known wait;
// wmu is held.
func (u *unroutedHold) unknownLocked(now time.Time) time.Duration {
	d := now.Sub(u.f.start) - u.excused
	if u.waits > 0 {
		d -= now.Sub(u.since)
	}
	return d
}

// excusedFor is how long the request has been held in a known wait.
func (u *unroutedHold) excusedFor() time.Duration {
	u.wmu.Lock()
	defer u.wmu.Unlock()
	d := u.excused
	if u.waits > 0 {
		d += time.Since(u.since)
	}
	return d
}

// checkUnknown is unknown's timer: a request held unroutedUnknown in no
// known wait is answered for; one in a wait now is looked at again when
// it ends, and one with time left, when that is up.
func (u *unroutedHold) checkUnknown() {
	u.wmu.Lock()
	if u.waits > 0 {
		u.due = true
		u.wmu.Unlock()
		return
	}
	if left := unroutedUnknown - u.unknownLocked(time.Now()); left > 0 {
		u.unknown.Reset(left)
		u.wmu.Unlock()
		return
	}
	u.wmu.Unlock()
	u.giveUp(true)
}

// waitedBody is a watched request's body: the time spent waiting for it
// to arrive is a known wait, however much of it is read where.
type waitedBody struct {
	io.ReadCloser
	u *unroutedHold
}

func (b waitedBody) Read(p []byte) (int, error) {
	if b.u.bodyStopped.Load() {
		return 0, errBodyStopped
	}
	done := b.u.wait("body")
	defer done()
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.u.bodyRead.Add(int64(n))
		b.u.bodyLast.Store(time.Now().UnixNano())
	}
	if err != nil {
		b.u.bodyEnded.Store(true)
	}
	return n, err
}

// errBodyStopped is a body read after magpie answered for its request
// while the body was still arriving.
var errBodyStopped = errors.New("magpie stopped reading this request's body: it was still arriving when magpie answered for it")

// inBody is whether u's request is reading its body now.
func (u *unroutedHold) inBody() bool {
	u.wmu.Lock()
	defer u.wmu.Unlock()
	for _, k := range u.kinds {
		if k.kind == "body" {
			return k.n > 0
		}
	}
	return false
}

// bodyArriving is whether the handler has begun reading the body, is
// between two of its reads, and its end hasn't come: less has come than
// the request's own Content-Length says, or, for a body of no said
// length (chunked), no read has met its end yet. The hold can run out in
// the gap between two reads of a trickled body, and a read's absence
// alone answered such a request 504, as if magpie held it of its own (a
// full run raced one out there, #4511); a body short of its length is
// still arriving, and the time is the client's. Not before its first read: a request waiting for body
// room (bodyRoom.take) is held by magpie, whose 503 or 504 says so, and
// its client may have sent the whole body already. A chunked body's
// decoder spends its time between reads too (empty gzip members, #4775
// review), and judged by the read alone it was answered 504 there, as
// magpie's own hold. A body read whole and an empty one are not arriving.
func (u *unroutedHold) bodyArriving() bool {
	d := u.r.ContentLength
	if d == 0 || u.bodyEnded.Load() || d > 0 && u.bodyRead.Load() >= d {
		return false
	}
	u.wmu.Lock()
	defer u.wmu.Unlock()
	read := false
	for _, k := range u.kinds {
		switch k.kind {
		case "body":
			read = true
		case "body room":
			if k.n > 0 {
				return false
			}
		}
	}
	return read
}

// bodySaid is how much of u's request's body had come, and when the last
// of it did: "12 KB of 3 MB, the last 2s ago".
func (u *unroutedHold) bodySaid() string {
	n, of := u.bodyRead.Load(), ""
	if d := u.r.ContentLength; d > 0 {
		of = " of " + byteWord(d)
	}
	if n == 0 {
		return "none" + of
	}
	return fmt.Sprintf("%s%s, the last %s ago", byteWord(n), of, time.Since(time.Unix(0, u.bodyLast.Load())).Round(100*time.Millisecond))
}

// byteWord is n bytes in B, KB or MB.
func byteWord(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

// unroutedBodyMsg is the error of a request whose body was still arriving
// when it had been held for held: the time went to the client sending it,
// not to magpie, and it says so.
func unroutedBodyMsg(held time.Duration, body string) string {
	return fmt.Sprintf("magpie stopped this request after %s because its body was still arriving from the client (read: %s), before it could be routed to any provider. The upload exhausted its time allowance (32 KiB/s with a two-minute grace period, at most eight additional minutes). Check the connection or proxy to magpie, or compact the conversation and reduce inline images before retrying. "+heldSay, held, body, heldWhere())
}

// watchUnrouted watches r, counted as f, until the func it answers is
// called as its handler returns.
func (s *Server) watchUnrouted(w http.ResponseWriter, r *http.Request, f *inflight) (http.ResponseWriter, *http.Request, func()) {
	if f == nil || !unroutedWatched(r.URL.Path) {
		return w, r, func() {}
	}
	ctx, cancel := context.WithCancelCause(r.Context())
	u := &unroutedHold{s: s, f: f, w: newHeldWriter(w), cancel: cancel, agent: callerFrom(r).agent}
	r = r.WithContext(context.WithValue(ctx, unroutedKey{}, u))
	if r.Body != nil && r.Body != http.NoBody {
		r.Body = waitedBody{r.Body, u}
	}
	u.r = r
	f.mu.Lock()
	f.unrouted = u
	f.mu.Unlock()
	u.wmu.Lock()
	u.unknown = time.AfterFunc(unroutedUnknown, u.checkUnknown)
	u.most = time.AfterFunc(unroutedMost, u.checkMost)
	u.wmu.Unlock()
	show := time.AfterFunc(unroutedShow, u.show)
	return u.w, r, func() {
		show.Stop()
		u.wmu.Lock()
		u.unknown.Stop()
		u.most.Stop()
		u.wmu.Unlock()
		f.mu.Lock()
		// a timer that fired as the handler returned does nothing now
		u.over = true
		shown := u.shown
		if !u.ended {
			u.shown = nil
		}
		f.mu.Unlock()
		if shown != nil && !u.ended {
			// it was answered with no route of its own: it is the
			// usage log's, as before
			s.trace.drop(shown)
		}
		cancel(context.Canceled)
	}
}

// show begins the route that shows the request on the Routing page, as
// routing, while nothing else does.
func (u *unroutedHold) show() {
	f := u.f
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.begun) > 0 || u.shown != nil || u.ended || u.over {
		return
	}
	u.showLocked()
}

// showLocked begins the route that shows it; f.mu is held.
func (u *unroutedHold) showLocked() {
	asked, _ := u.asked.Load().(string)
	rt := Route{Time: u.f.start, Agent: u.agent, Session: u.f.session, Model: asked, Provider: "magpie"}
	if rt.Agent == "" {
		rt.Agent = u.f.agent
	}
	if u.f.redacting() {
		rt.Phase = phaseRedact
	}
	rt.callerKey = access.Caller(u.r.Context()).KeyID
	u.shown = u.s.trace.begin(rt)
}

// routed is the request's own route begun: the one that showed it goes.
func (u *unroutedHold) routed() (drop *Route) {
	if u.ended {
		return nil
	}
	drop, u.shown = u.shown, nil
	return drop
}

// giveUp answers the request with an error, when no route was begun for it
// and its handler wrote nothing, ends its context, and says on its route
// and in the log what held it. unknown: it was held unroutedUnknown in no
// wait magpie knows of, rather than unroutedMost other than masking and
// earned upload time.
func (u *unroutedHold) giveUp(unknown bool) {
	f := u.f
	f.mu.Lock()
	if len(f.begun) > 0 || u.ended || u.over {
		f.mu.Unlock()
		return
	}
	held := time.Since(f.start).Round(time.Second)
	msg, where, status := unroutedMsg(held, u.masking.Load() > 0), "", http.StatusGatewayTimeout
	switch {
	case unknown:
		where = fmt.Sprintf(" %s of it in no wait magpie knows of", unroutedUnknown)
	case u.masking.Load() > 0:
		where = " masking it"
	case u.inBody() || u.bodyArriving():
		// the client was still sending it (heatonz on Discord: 2m0s
		// "without routing" with the body half in): a 408 says whose
		// the time was, and an SDK sends it again at once
		where = " its body still arriving (" + u.bodySaid() + ")"
		msg, status = unroutedBodyMsg(held, u.bodySaid()), http.StatusRequestTimeout
	}
	if !u.w.answer(unroutedProto(u.r.URL.Path), status, msg) {
		f.mu.Unlock()
		return // the handler is answering
	}
	if status == http.StatusRequestTimeout {
		// and the handler reads no more of it: its read ends now, not at
		// the next idle limit, and the connection goes with the answer
		u.bodyStopped.Store(true)
		_ = http.NewResponseController(u.w.w).SetReadDeadline(time.Now())
	}
	u.ended = true
	if u.shown == nil {
		u.showLocked()
	}
	shown := u.shown
	f.mu.Unlock()
	u.s.trace.update(shown, func(r *Route) {
		r.Done, r.Status, r.Error, r.Millis = true, status, msg, time.Since(r.Time).Milliseconds()
	})
	// the report is taken while it is still held: once its context ends,
	// what held it lets go, and the stacks would show it gone
	asked, _ := u.asked.Load().(string)
	who := cmpOr(u.agent, cmpOr(f.agent, "?"))
	where = cmpOr(where, " not masking it")
	report := u.s.heldReport(u, held, status, who, asked, where)
	u.cancel(errUnrouted)
	name, err := writeHeld(report)
	saved := ""
	if err != nil {
		saved = "; its report could not be saved: " + err.Error()
	} else {
		saved = "; its report is " + filepath.Join(HeldDir(), name)
		u.s.trace.update(shown, func(r *Route) { r.Held = name })
	}
	log.Printf("gateway: %s %s for %s from %s held %s before routing,%s%s; stopped with %d%s%s", u.r.Method, u.r.URL.Path, cmpOr(asked, "?"), who, held, where, u.waitsSaid(), status, saved, unroutedStacks())
}

// unroutedMsg is the error of a request held for held before routing:
// one held while it was masked, with Settings → Privacy's local model on,
// waited for that model (#3682), and is told so, and how to send without
// waiting for it. Masking's time no longer counts toward unroutedMost,
// so that is a masking begun as the rest of the wait ran out.
func unroutedMsg(held time.Duration, masking bool) string {
	if masking && modelOn() {
		why := ""
		if b := modelBusy(); b != "" {
			why = " (" + b + ")"
		}
		return fmt.Sprintf("magpie held this request for %s waiting for the local masking model (Settings → Privacy) to mask it%s, so it was stopped rather than sent masked by the rules alone. Send it again. To send requests without waiting for the model, turn it off in Settings → Privacy.", held, why)
	}
	if masking {
		return fmt.Sprintf("magpie held this request for %s while masking it (Settings → Privacy), before routing it to any provider, so it was stopped rather than left waiting. Send it again. "+heldSay, held, heldWhere())
	}
	return fmt.Sprintf("magpie held this request for %s without routing it to any provider, so it was stopped rather than left waiting. Send it again. "+heldSay, held, heldWhere())
}

// heldSay is where the error of a request held too long sends its user:
// the report magpie saved of it (heldReport), which a desktop app's user
// can reach, where its log goes only to a stderr nobody sees.
const heldSay = "If it keeps happening, report it with the report magpie saved of where it was held: Copy Report on this request on magpie's Routing page, or the newest file in %s."

// modelOn and modelBusy are internal/masking's, a test's
// to stand in for.
var modelOn, modelBusy = masking.ModelOn, masking.ModelBusy

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// unroutedDumped is when the goroutines were last written to the log for
// a held request: one dump says where, and a burst of held requests
// writes it once.
var unroutedDumped atomic.Int64

// unroutedStacks is every goroutine's stack, alike ones counted once, for
// the log at most once in ten minutes; "" otherwise.
func unroutedStacks() string {
	now := time.Now().UnixNano()
	last := unroutedDumped.Load()
	if last != 0 && now-last < int64(10*time.Minute) || !unroutedDumped.CompareAndSwap(last, now) {
		return ""
	}
	var b bytes.Buffer
	_ = pprof.Lookup("goroutine").WriteTo(&b, 1)
	const most = 256 << 10
	if b.Len() > most {
		b.Truncate(most)
	}
	return "; where every goroutine stood:\n" + b.String()
}
