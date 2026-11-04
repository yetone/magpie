package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"weak"

	"github.com/yetone/magpie/internal/gwmw"
	"github.com/yetone/magpie/internal/settings"
)

// unroutedLimits sets when a request held before routing shows, and when
// it is answered for.
func unroutedLimits(t *testing.T, show, most time.Duration) {
	t.Helper()
	s, m := unroutedShow, unroutedMost
	unroutedShow, unroutedMost = show, most
	t.Cleanup(func() { unroutedShow, unroutedMost = s, m })
}

// unknownLimit sets how long a request may be held before routing in no
// wait magpie knows of.
func unknownLimit(t *testing.T, d time.Duration) {
	t.Helper()
	was := unroutedUnknown
	unroutedUnknown = d
	t.Cleanup(func() { unroutedUnknown = was })
}

// watchedHold is the watch of s's one request held before routing, once
// it is there.
func watchedHold(t *testing.T, s *Server) *unroutedHold {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s.active.mu.Lock()
		var u *unroutedHold
		for _, f := range s.active.m {
			f.mu.Lock()
			if f.unrouted != nil {
				u = f.unrouted
			}
			f.mu.Unlock()
		}
		s.active.mu.Unlock()
		if u != nil {
			return u
		}
		if time.Now().After(deadline) {
			t.Fatal("no request was held before routing")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// unroutedReply is a reply postWhole got.
type unroutedReply struct {
	code int
	body string
	err  error
}

// heldPast waits until u's request has been held in known waits for d,
// and answers nil then; or answers its reply, if it is answered first.
func heldPast(t *testing.T, u *unroutedHold, d time.Duration, got chan unroutedReply) *unroutedReply {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for u.excusedFor() < d {
		select {
		case r := <-got:
			return &r
		case <-time.After(5 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("held in known waits for %s only", u.excusedFor())
		}
	}
	return nil
}

// posted posts body to srv in the background, its reply to the channel.
func posted(srv *httptest.Server, body io.Reader) chan unroutedReply {
	got := make(chan unroutedReply, 1)
	go func() {
		code, b, err := postWhole(srv, body)
		got <- unroutedReply{code, b, err}
	}()
	return got
}

// postWhole posts body to srv's Chat API and answers its reply.
func postWhole(srv *httptest.Server, body io.Reader) (int, string, error) {
	c := &http.Client{Timeout: 30 * time.Second}
	resp, err := c.Post(srv.URL+"/v1/chat/completions", "application/json", body)
	if err != nil {
		return 0, "", err
	}
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(b), err
}

const unroutedHi = `{"model":"probe/text","messages":[{"role":"user","content":"hi"}]}`

// sharwowwow on X: two minutes of a spinner before the 504. A request
// held before routing in no wait magpie knows of (here a lock routing
// needs, held and never let go) is answered once it has been held
// unroutedUnknown, long before unroutedMost.
func TestUnroutedUnknownHoldAnsweredSooner(t *testing.T) {
	unroutedLimits(t, 20*time.Millisecond, time.Hour)
	unknownLimit(t, 300*time.Millisecond)
	s, _ := slowEyed(t, 0)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	sticks.Lock()
	var once sync.Once
	unlock := func() { once.Do(sticks.Unlock) }
	t.Cleanup(unlock)

	got := posted(srv, strings.NewReader(unroutedHi))
	var r unroutedReply
	select {
	case r = <-got:
	case <-time.After(20 * time.Second):
		unlock()
		t.Fatal("a request held in no known wait was not answered before unroutedMost")
	}
	unlock()
	if r.err != nil || r.code != http.StatusGatewayTimeout || !strings.Contains(r.body, "without routing it to any provider") {
		t.Fatalf("held request answered %d %q %v", r.code, r.body, r.err)
	}
	heldSaved(t, s)
}

// heldSaved waits for the report of the request s answered 504 for
// being held before routing: the 504 goes first and the report is saved
// after it, so a test that returns on the 504 alone has its HOME removed
// under the write ("TempDir RemoveAll cleanup: … magpie/held: directory
// not empty" on mote, 10-29).
func heldSaved(t *testing.T, s *Server) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		// any route: once let go, the request may have begun one of
		// its own beside the one shown while it was held
		for _, r := range routesOf(s, "probe/text") {
			if r.Held != "" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the held request's report was never saved: %+v", routesOf(s, "probe/text"))
		}
	}
}

// A body still arriving (an image over a slow link) is a known wait: a
// request whose body takes longer than unroutedUnknown is routed and
// answered, not cut off.
func TestUnroutedSlowBodyNotCutOff(t *testing.T) {
	unroutedLimits(t, 20*time.Millisecond, time.Hour)
	unknownLimit(t, 100*time.Millisecond)
	s, _ := slowEyed(t, 0)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })
	got := posted(srv, pr)
	half := len(unroutedHi) / 2
	if _, err := pw.Write([]byte(unroutedHi[:half])); err != nil {
		t.Fatal(err)
	}
	if r := heldPast(t, watchedHold(t, s), 3*unroutedUnknown, got); r != nil {
		t.Fatalf("answered %d %q %v while its body was still arriving", r.code, r.body, r.err)
	}
	if _, err := pw.Write([]byte(unroutedHi[half:])); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	r := <-got
	if r.err != nil || r.code != http.StatusOK {
		t.Fatalf("a slow body was answered %d %q %v", r.code, r.body, r.err)
	}
}

// gatedMasker is masking that waits for the local model until gate is
// closed, then sends the body as it came.
type gatedMasker struct {
	waitingMasker
	gate chan struct{}
}

func (m gatedMasker) JSONHitsContext(ctx context.Context, b []byte) ([]byte, []gwmw.Hit) {
	select {
	case <-m.gate:
	case <-ctx.Done():
	}
	return b, nil
}

func (m gatedMasker) TextHitsContext(ctx context.Context, s string) (string, []gwmw.Hit) {
	return s, nil
}

func (m gatedMasker) JSONHits(b []byte) ([]byte, []gwmw.Hit) {
	return m.JSONHitsContext(context.Background(), b)
}
func (m gatedMasker) TextHits(s string) (string, []gwmw.Hit) { return s, nil }
func (m gatedMasker) JSON(b []byte) ([]byte, int)            { b, _ = m.JSONHits(b); return b, 0 }
func (m gatedMasker) Text(s string) (string, int)            { return s, 0 }

// Masking (the local model loading) is a known wait: a request masked
// for longer than unroutedUnknown is routed once masked, not cut off.
func TestUnroutedMaskingNotCutOff(t *testing.T) {
	unroutedLimits(t, 20*time.Millisecond, time.Hour)
	unknownLimit(t, 100*time.Millisecond)
	s, _ := slowEyed(t, 0)
	if err := settings.Save(settings.Settings{Redact: true}); err != nil {
		t.Fatal(err)
	}
	withoutMasker(t)
	m := gatedMasker{gate: make(chan struct{})}
	var once sync.Once
	open := func() { once.Do(func() { close(m.gate) }) }
	t.Cleanup(open)
	t.Cleanup(gwmw.Mask(m))
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	got := posted(srv, strings.NewReader(unroutedHi))
	u := watchedHold(t, s)
	if r := heldPast(t, u, 3*unroutedUnknown, got); r != nil {
		t.Fatalf("answered %d %q %v while it was masked", r.code, r.body, r.err)
	}
	if u.masking.Load() == 0 {
		t.Fatal("held past the limit, but not while masking")
	}
	open()
	r := <-got
	if r.err != nil || r.code != http.StatusOK {
		t.Fatalf("a request masked past the limit was answered %d %q %v", r.code, r.body, r.err)
	}
}

// A request waiting in a key's queue (MaxConcurrency) past
// unroutedUnknown is not cut off: the queue is a known wait.
func TestUnroutedSlotWaitNotCutOff(t *testing.T) {
	unroutedLimits(t, 20*time.Millisecond, time.Hour)
	unknownLimit(t, 100*time.Millisecond)
	s, up := slowEyed(t, 0)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	codes := make(chan int, 2)
	go func() {
		code, _, _ := postWhole(srv, strings.NewReader(`{"model":"probe/text","messages":[{"role":"user","content":"hold"}]}`))
		codes <- code
	}()
	<-up.held // the one slot is taken
	go func() {
		code, _, _ := postWhole(srv, strings.NewReader(unroutedHi))
		codes <- code
	}()
	deadline := time.Now().Add(20 * time.Second)
	for {
		queued := false
		s.active.mu.Lock()
		for _, f := range s.active.m {
			if p, _ := f.phase.Load().(string); p == "slot" && time.Since(f.start) > 3*unroutedUnknown {
				queued = true
			}
		}
		s.active.mu.Unlock()
		if queued {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the second request never waited in the queue past the limit")
		}
		time.Sleep(5 * time.Millisecond)
	}
	up.open()
	for range 2 {
		if code := <-codes; code != http.StatusOK {
			t.Fatalf("a request queued past the limit was answered %d", code)
		}
	}
}

// heldSight holds pastedImage before it is routed: its image's
// description is in flight and never ends, as a describe stuck on
// something that never lets go would be. What it holds the request on is
// returned, closed to let it go with text.
func heldSight(t *testing.T, s *Server) (release func(text string)) {
	t.Helper()
	if _, ok := seer(); !ok {
		t.Fatal("no model to describe images with")
	}
	e := &sight{done: make(chan struct{})}
	s.sightMu.Lock()
	s.keepSightLocked(sightKey("data:image/png;base64,aGVsbG8="), e)
	s.sightMu.Unlock()
	closed := false
	t.Cleanup(func() {
		if !closed {
			close(e.done)
		}
	})
	return func(text string) {
		e.text = text
		closed = true
		close(e.done)
	}
}

// routesOf is the trace's routes for model now.
func routesOf(s *Server, model string) []Route {
	var out []Route
	for _, r := range s.Trace(context.Background(), 0, 0).Routes {
		if r.Model == model {
			out = append(out, r)
		}
	}
	return out
}

// heyusaku6 on X: Codex thought for over ten minutes on a model served
// through magpie, the Routing page showed nothing, and only restarting
// both ended it. A request held before any route is begun for it shows on
// the Routing page as routing, and is answered with an error in bounded
// time, whole, rather than left to wait with nothing said.
func TestUnroutedRequestShownThenAnswered(t *testing.T) {
	unroutedLimits(t, 100*time.Millisecond, time.Second)
	s, _ := slowEyed(t, 0)
	heldSight(t, s)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	type reply struct {
		code int
		body string
		err  error
	}
	got := make(chan reply, 1)
	go func() {
		c := &http.Client{Timeout: 20 * time.Second}
		resp, err := c.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(pastedImage))
		if err != nil {
			got <- reply{err: err}
			return
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		got <- reply{resp.StatusCode, string(b), err}
	}()

	// shown as routing while it waits
	deadline := time.Now().Add(10 * time.Second)
	for {
		rs := routesOf(s, "probe/text")
		if len(rs) == 1 && !rs[0].Done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the held request never showed on the Routing page: %+v", rs)
		}
		time.Sleep(10 * time.Millisecond)
	}

	var r reply
	select {
	case r = <-got:
	case <-time.After(15 * time.Second):
		t.Fatal("the held request was never answered")
	}
	if r.err != nil || r.code != http.StatusGatewayTimeout || !strings.Contains(r.body, "without routing it to any provider") {
		t.Fatalf("held request answered %d %q %v", r.code, r.body, r.err)
	}
	rs := routesOf(s, "probe/text")
	if len(rs) != 1 || !rs[0].Done || rs[0].Status != http.StatusGatewayTimeout || !strings.Contains(rs[0].Error, "without routing") {
		t.Fatalf("the held request's route: %+v", rs)
	}
	heldSaved(t, s)
}

// One shown while it waited gives way to its own route once routed: the
// Routing page has the request once, and its reply is the provider's.
func TestUnroutedShownGivesWayToItsRoute(t *testing.T) {
	unroutedLimits(t, 50*time.Millisecond, time.Minute)
	s, u := slowEyed(t, 0)
	u.open()
	release := heldSight(t, s)
	done := make(chan int, 1)
	go func() {
		code, _ := postAs(t, s, "", pastedImage)
		done <- code
	}()
	deadline := time.Now().Add(10 * time.Second)
	for len(routesOf(s, "probe/text")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the held request never showed on the Routing page")
		}
		time.Sleep(10 * time.Millisecond)
	}
	release("HELLO")
	select {
	case code := <-done:
		if code != 200 {
			t.Fatalf("routed after its wait: %d", code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("never answered once let go")
	}
	rs := routesOf(s, "probe/text")
	if len(rs) != 1 || rs[0].Provider != "probe" || rs[0].Status != 200 {
		t.Fatalf("routes for the request: %+v", rs)
	}
}

// A request answered before it was shown leaves nothing on the trace of
// its wait, and a timer that fires as its handler returns adds nothing.
func TestUnroutedQuickRequestLeavesNoTrace(t *testing.T) {
	unroutedLimits(t, time.Millisecond, time.Minute)
	s, _ := slowEyed(t, 0)
	for range 20 {
		code, _ := postAs(t, s, "", `{"model":"nowhere/none","messages":[{"role":"user","content":"hi"}]}`)
		if code < 400 {
			t.Fatalf("an unknown model answered %d", code)
		}
	}
	time.Sleep(50 * time.Millisecond)
	for _, r := range s.Trace(context.Background(), 0, 0).Routes {
		if r.Provider == "magpie" && !r.Done {
			t.Fatalf("a route left showing a request that was answered: %+v", r)
		}
	}
}

// #3682: masking's time counts toward unroutedMost no more, while what
// comes after it still does. A request masked for well past unroutedMost
// isn't answered; then held by an image's description that never ends,
// it is answered 504 once that wait has had unroutedMost.
func TestUnroutedMostLeavesOutMasking(t *testing.T) {
	unroutedLimits(t, 20*time.Millisecond, 300*time.Millisecond)
	s, _ := slowEyed(t, 0)
	heldSight(t, s)
	if err := settings.Save(settings.Settings{Redact: true}); err != nil {
		t.Fatal(err)
	}
	withoutMasker(t)
	m := gatedMasker{gate: make(chan struct{})}
	var once sync.Once
	open := func() { once.Do(func() { close(m.gate) }) }
	t.Cleanup(open)
	t.Cleanup(gwmw.Mask(m))
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	got := posted(srv, strings.NewReader(pastedImage))
	u := watchedHold(t, s)
	if r := heldPast(t, u, 3*unroutedMost, got); r != nil {
		t.Fatalf("answered %d %q %v while it was masked", r.code, r.body, r.err)
	}
	if u.masking.Load() == 0 {
		t.Fatal("held past unroutedMost, but not while masking")
	}
	open()
	select {
	case r := <-got:
		if r.code != http.StatusGatewayTimeout || !strings.Contains(r.body, "without routing") {
			t.Fatalf("held after masking: %d %q %v", r.code, r.body, r.err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("a request held after masking was never answered")
	}
	heldSaved(t, s)
}

// A request another magpie passed on is shown, while it waits to be
// routed, as the agent that made it there (callerFrom), read once as it
// comes: the show's timer never reads its headers, which withCaller and
// decodedBody change meanwhile (#3956's ubuntu race run).
func TestUnroutedShownAsItsCaller(t *testing.T) {
	unroutedLimits(t, 20*time.Millisecond, time.Hour)
	s, _ := slowEyed(t, 0)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })
	got := make(chan unroutedReply, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", pr)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "magpie/0.1.0")
		req.Header.Set(AgentHeader, "pi")
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			got <- unroutedReply{err: err}
			return
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		got <- unroutedReply{resp.StatusCode, string(b), nil}
	}()
	if _, err := pw.Write([]byte(unroutedHi[:10])); err != nil {
		t.Fatal(err)
	}
	u := watchedHold(t, s)
	var agent string
	for deadline := time.Now().Add(10 * time.Second); ; {
		u.f.mu.Lock()
		if u.shown != nil {
			agent = u.shown.Agent
		}
		shown := u.shown != nil
		u.f.mu.Unlock()
		if shown {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the held request was never shown")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if agent != "pi" {
		t.Errorf("shown as %q while held, want pi, the agent another magpie passed it on for", agent)
	}
	if _, err := pw.Write([]byte(unroutedHi[10:])); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	if r := <-got; r.err != nil || r.code != http.StatusOK {
		t.Fatalf("answered %d %q %v", r.code, r.body, r.err)
	}
}

// Jason on Discord: a Codex turn on ChatGPT Pro accounts, held while it
// was masked, showed on the Routing page as "magpie · 0 on" over "Nothing
// could take ." — the route that shows a held request had no model,
// because Codex's own path never told the watch what it asked for, as
// the Chat, Responses, Messages and Gemini paths do. It names it.
func TestUnroutedCodexPathShowsItsModel(t *testing.T) {
	unroutedLimits(t, 20*time.Millisecond, time.Hour)
	s, _ := slowEyed(t, 0)
	if err := settings.Save(settings.Settings{Redact: true}); err != nil {
		t.Fatal(err)
	}
	withoutMasker(t)
	m := gatedMasker{gate: make(chan struct{})}
	var once sync.Once
	open := func() { once.Do(func() { close(m.gate) }) }
	t.Cleanup(open)
	t.Cleanup(gwmw.Mask(m))
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	t.Cleanup(open) // before srv.Close, which waits on the held request
	got := make(chan int, 1)
	go func() {
		c := &http.Client{Timeout: 30 * time.Second}
		resp, err := c.Post(srv.URL+CodexPath+"/responses", "application/json", strings.NewReader(`{"model":"probe/text","input":"hi"}`))
		if err != nil {
			got <- 0
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		got <- resp.StatusCode
	}()
	u := watchedHold(t, s)
	deadline := time.Now().Add(10 * time.Second)
	for {
		u.f.mu.Lock()
		shown := u.shown
		u.f.mu.Unlock()
		if shown != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the held Codex request never showed on the Routing page")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if rs := routesOf(s, "probe/text"); len(rs) != 1 || rs[0].Done || rs[0].Provider != "magpie" {
		t.Fatalf("the held Codex request is shown without the model it asked for: %+v", s.Trace(context.Background(), 0, 0).Routes)
	}
	open()
	select {
	case <-got:
	case <-time.After(15 * time.Second):
		t.Fatal("never answered once masked")
	}
}

// TestUnroutedTimersLetGoOfRequest: a stopped timer is not gone. The
// runtime keeps it, and its func, in the timer heap of the P that set it
// until its time comes or that P tidies its heap, so a timer that reached
// the request kept its body up to unroutedMost after the handler returned
// (#5250). The test holds the stopped timers as that heap does.
func TestUnroutedTimersLetGoOfRequest(t *testing.T) {
	body := make([]byte, 64<<10)
	gone := weak.Make(&body[0])
	f := &inflight{}
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	body = nil
	_, r, unwatch := (&Server{}).watchUnrouted(httptest.NewRecorder(), r, f)
	f.mu.Lock()
	u := f.unrouted
	f.mu.Unlock()
	u.wmu.Lock()
	timers := []*time.Timer{u.unknown, u.most}
	u.wmu.Unlock()
	unwatch()
	u, r, f = nil, nil, nil
	runtime.GC()
	if gone.Value() != nil {
		t.Fatal("a stopped unrouted timer still reaches its request's body")
	}
	runtime.KeepAlive(timers)
}
