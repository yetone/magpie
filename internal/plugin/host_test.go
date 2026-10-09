package plugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHost is a host whose stdout the test writes, so its real read and
// dispatch run without Bun. What magpie writes to the child (fetch, credit,
// abort) is drained into f.sent, so a test can see the control traffic without
// a writer goroutine parked on a pipe.
type fakeHost struct {
	*host
	pw    *io.PipeWriter
	outMu sync.Mutex
	sent  []map[string]any
}

func newFakeHost(t *testing.T) *fakeHost {
	return newFakeHostLine(t, 0)
}

// newFakeHostLine is newFakeHost with the child's line cap lowered, for the
// too-long-line case. The cap is per host, so no test mutates a shared value
// while another test's reader is running.
func newFakeHostLine(t *testing.T, maxLine int) *fakeHost {
	t.Helper()
	pr, pw := io.Pipe()
	hctx, hcancel := context.WithCancel(context.Background())
	h := &host{
		cmd: &exec.Cmd{}, in: discardCloser{},
		calls:    map[int64]*call{},
		dead:     make(chan struct{}),
		slots:    make(chan struct{}, callLimit()),
		streams:  make(chan struct{}, streamLimit()),
		out:      make(chan writeReq, callLimit()),
		ctrl:     map[int64]*ctrl{},
		ctrlWake: make(chan struct{}, 1),
		ctx:      hctx,
		cancel:   hcancel,
		maxLine:  maxLine,
	}
	f := &fakeHost{host: h, pw: pw}
	go h.read(pr)
	go h.ctrlLoop()
	go f.drain()
	t.Cleanup(func() {
		_ = pw.CloseWithError(io.EOF)
		hcancel()
	})
	return f
}

// drain records what magpie wrote to the child, until the host is gone.
func (f *fakeHost) drain() {
	for {
		select {
		case <-f.host.dead:
			return
		case req := <-f.out:
			f.host.tookWrite()
			var v map[string]any
			if json.Unmarshal(req.b, &v) == nil {
				f.outMu.Lock()
				f.sent = append(f.sent, v)
				f.outMu.Unlock()
			}
			if req.done != nil {
				req.done <- nil
			}
		}
	}
}

// wrote is every message magpie wrote to the child so far.
func (f *fakeHost) wrote() []map[string]any {
	f.outMu.Lock()
	defer f.outMu.Unlock()
	return append([]map[string]any(nil), f.sent...)
}

// methods are the methods magpie wrote, in order.
func (f *fakeHost) methods() []string {
	var out []string
	for _, m := range f.wrote() {
		s, _ := m["method"].(string)
		out = append(out, s)
	}
	return out
}

// credited is how many bytes magpie gave back as credit so far.
func (f *fakeHost) credited() int {
	var n int
	for _, m := range f.wrote() {
		if m["method"] != "credit" {
			continue
		}
		if p, ok := m["params"].(map[string]any); ok {
			if v, ok := p["n"].(float64); ok {
				n += int(v)
			}
		}
	}
	return n
}

// line is one message the host would have written on its stdout.
func (f *fakeHost) line(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	_, _ = f.pw.Write(append(b, '\n'))
}

func (f *fakeHost) head(id int64) {
	f.line(map[string]any{"id": id, "event": "head", "status": 200, "headers": map[string]string{}})
}

func (f *fakeHost) chunk(id int64, data string) {
	f.line(map[string]any{"id": id, "event": "chunk", "data": base64.StdEncoding.EncodeToString([]byte(data))})
}

func (f *fakeHost) answer(id int64, why string) {
	if why == "" {
		f.line(map[string]any{"id": id, "result": nil})
		return
	}
	f.line(map[string]any{"id": id, "error": map[string]string{"message": why}})
}

// next is the next message queued for the call, as Fetch takes its head.
func (f *fakeHost) next(t *testing.T, c *call) message {
	t.Helper()
	for {
		if m, ok := c.pop(); ok {
			return m
		}
		select {
		case <-c.wake:
		case <-time.After(5 * time.Second):
			t.Fatal("no message arrived")
		}
	}
}

// reply writes a stream's head and waits for the test to take it, as Fetch
// takes its head, then writes the rest of the reply in the background.
func reply(t *testing.T, f *fakeHost, id int64, c *call, rest func()) message {
	t.Helper()
	popped := make(chan struct{})
	go func() {
		f.head(id)
		<-popped
		rest()
	}()
	h := f.next(t, c)
	close(popped)
	return h
}

// readBody is the reply body Fetch hands out for the call.
func readBody(f *fakeHost, id int64, c *call, ctx context.Context) *body {
	return &body{h: f.host, id: id, c: c, ctx: ctx, closed: make(chan struct{})}
}

// begin accepts the synthetic fetch before the test dispatches its reply.
func (f *fakeHost) begin(t *testing.T, stream bool) (int64, *call) {
	t.Helper()
	id, c, err := f.host.begin(stream)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	c.transition(callAccepted, nil, nil)
	return id, c
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("%s didn't happen", what)
}

// registered is whether the call is still the host's to answer.
func (f *fakeHost) registered(id int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.calls[id]
	return ok
}

// queued is how many chunks the call holds for its reader.
func (c *call) queuedChunks() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.queue)
}

// owed is how many decoded bytes the child sent and the reader has not yet
// given back.
func (c *call) owedBytes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.owed
}

// slotsFree is how many admission slots are free.
func (f *fakeHost) slotsFree() int { return cap(f.host.slots) - len(f.host.slots) }

// chunkAt is the i-th chunk of a stream, "" apart from its place.
func chunkAt(i int) string { return strconvItoa(i) + "\n" }

// chunks is the first n chunks of a stream, as one body.
func chunks(n int) string {
	var b strings.Builder
	for i := range n {
		b.WriteString(chunkAt(i))
	}
	return b.String()
}

func strconvItoa(i int) string {
	if i == 0 {
		return "0"
	}
	var d []byte
	for i > 0 {
		d = append([]byte{byte('0' + i%10)}, d...)
		i /= 10
	}
	return string(d)
}

type discardCloser struct{}

func (discardCloser) Write(p []byte) (int, error) { return len(p), nil }
func (discardCloser) Close() error                { return nil }

// failingIn is a child's stdin whose writes fail, as a pipe to a dead child.
type failingIn struct{}

func (failingIn) Write(p []byte) (int, error) { return 0, errors.New("broken pipe") }
func (failingIn) Close() error                { return nil }

// TestHostWriterFailureWakesWaiters: a stdin write that fails ends the host, so
// a call settling with an abort owed gets its slot back instead of waiting for
// a write result that will never come, and later enqueues return rather than
// park. The host's stdout staying open must not be needed to notice.
func TestHostWriterFailureWakesWaiters(t *testing.T) {
	hctx, hcancel := context.WithCancel(context.Background())
	h := &host{
		in: failingIn{}, calls: map[int64]*call{},
		dead:     make(chan struct{}),
		slots:    make(chan struct{}, callLimit()),
		streams:  make(chan struct{}, streamLimit()),
		out:      make(chan writeReq, callLimit()),
		ctrl:     map[int64]*ctrl{},
		ctrlWake: make(chan struct{}, 1),
		ctx:      hctx,
		cancel:   hcancel,
	}
	go h.writerLoop()
	go h.ctrlLoop()

	_, c, err := h.begin(true)
	if err != nil {
		t.Fatal(err)
	}
	c.transition(callAccepted, nil, nil)
	c.settle(context.Canceled) // an abort is owed: its slot waits on the write

	waitFor(t, "the failed host freed the abort's slot", func() bool {
		return len(h.slots) == 0 && len(h.streams) == 0
	})
	select {
	case <-h.dead:
	default:
		t.Fatal("a failed write did not end the host")
	}
	done := make(chan error, 1)
	go func() { done <- h.enqueue(context.Background(), map[string]any{"id": 1}) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("enqueue parked after the host's write failed")
	}
}

// TestHostWriteFailureWakesRPCAndHeadWaiters: with the child's stdin broken but
// its stdout still open, an RPC waiting on a context that never ends and a
// Fetch waiting for its head both return rather than wait for a host that is
// gone.
func TestHostWriteFailureWakesRPCAndHeadWaiters(t *testing.T) {
	pr, pw := io.Pipe()
	hctx, hcancel := context.WithCancel(context.Background())
	h := &host{
		cmd: &exec.Cmd{}, in: failingIn{},
		calls:    map[int64]*call{},
		dead:     make(chan struct{}),
		slots:    make(chan struct{}, callLimit()),
		streams:  make(chan struct{}, streamLimit()),
		out:      make(chan writeReq, callLimit()),
		ctrl:     map[int64]*ctrl{},
		ctrlWake: make(chan struct{}, 1),
		ctx:      hctx,
		cancel:   hcancel,
	}
	go h.read(pr) // the child's stdout stays open: only stdin is broken
	go h.writerLoop()
	go h.ctrlLoop()
	t.Cleanup(func() { _ = pw.CloseWithError(io.EOF) })
	_, c, err := h.begin(true)
	if err != nil {
		t.Fatal(err)
	}
	c.transition(callAccepted, nil, nil)

	// an RPC whose context never ends, waiting for an answer that will not come
	rpc := make(chan error, 1)
	go func() { rpc <- h.call(context.Background(), "prompt", map[string]any{}, nil) }()
	select {
	case err := <-rpc:
		if err == nil {
			t.Fatal("an RPC succeeded on a host whose stdin is broken")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an RPC waiter was left waiting after the write failed")
	}

	// and a Fetch waiting for its head
	head := make(chan error, 1)
	go func() {
		_, _, err := c.headRead(context.Background())
		head <- err
	}()
	select {
	case err := <-head:
		if err == nil {
			t.Fatal("a head read succeeded on a host whose stdin is broken")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a head waiter was left waiting after the write failed")
	}
}

// TestHostStreamRefusesRepeatedHeadAndUnknownEvents: a second head, or an event
// the host does not know, is a broken child — refused rather than queued for
// free, so a run of zero-cost lines cannot grow the queue without bound.
func TestHostStreamRefusesRepeatedHeadAndUnknownEvents(t *testing.T) {
	t.Run("second head", func(t *testing.T) {
		f := newFakeHost(t)
		_, c := f.begin(t, true)
		if !c.push(message{Event: "head"}) {
			t.Fatal("the first head was refused")
		}
		if c.push(message{Event: "head"}) {
			t.Fatal("a second head was queued")
		}
		if got := c.givenUp(); !errors.Is(got, errProtocolOverrun) {
			t.Fatalf("givenUp = %v, want errProtocolOverrun", got)
		}
		if n := c.queuedChunks(); n != 0 {
			t.Fatalf("%d chunks left after a repeated head", n)
		}
	})
	t.Run("unknown event", func(t *testing.T) {
		f := newFakeHost(t)
		_, c := f.begin(t, true)
		if c.push(message{Event: "surprise", Data: "YQ=="}) {
			t.Fatal("an unknown event was queued")
		}
		if got := c.givenUp(); !errors.Is(got, errProtocolOverrun) {
			t.Fatalf("givenUp = %v, want errProtocolOverrun", got)
		}
	})
}

// TestHostReadRejectsAnUnboundedLine: a child that never ends a line fails the
// host rather than making it allocate without bound.
func TestHostReadRejectsAnUnboundedLine(t *testing.T) {
	f := newFakeHostLine(t, 1024)
	if _, err := f.pw.Write(bytes.Repeat([]byte("x"), 4096)); err != nil {
		t.Fatalf("writing the long line: %v", err)
	}
	_ = f.pw.Close() // the line ends, too long: the host must fail rather than grow it
	waitFor(t, "the host failed on a too-long line", func() bool {
		select {
		case <-f.host.dead:
			return true
		default:
			return false
		}
	})
}

// TestHostFailClearsIncompleteBodies: a host that fails lets go of an
// incomplete body's buffer and slot at once, so a call nothing reads or closes
// cannot keep the budget. A body whose answer already came is left for its
// reader.
func TestHostFailClearsIncompleteBodies(t *testing.T) {
	t.Run("incomplete", func(t *testing.T) {
		f := newFakeHost(t)
		id, c := f.begin(t, true)
		if !c.push(message{Event: "head"}) {
			t.Fatal("the head was refused")
		}
		if !c.push(message{Event: "chunk", Data: base64.StdEncoding.EncodeToString([]byte("hi"))}) {
			t.Fatal("the chunk was refused")
		}
		if f.slotsFree() == cap(f.host.slots) {
			t.Fatal("the call holds no slot before the host fails")
		}
		f.host.fail(errHostGone)
		waitFor(t, "the incomplete body was let go", func() bool {
			return c.queuedChunks() == 0 && f.slotsFree() == cap(f.host.slots)
		})
		if f.registered(id) {
			t.Fatal("the failed host still holds the call")
		}
	})
	t.Run("finished keeps its data", func(t *testing.T) {
		f := newFakeHost(t)
		id, c := f.begin(t, true)
		b := readBody(f, id, c, context.Background())
		reply(t, f, id, c, func() {
			f.chunk(id, "hi")
			f.answer(id, "")
		})
		waitFor(t, "the answered call left the host", func() bool { return !f.registered(id) })
		f.host.fail(errHostGone)
		got, err := io.ReadAll(b)
		if string(got) != "hi" || err != nil {
			t.Fatalf("body = %q, %v, want the queued chunk hi and no error", got, err)
		}
	})
}

func TestHostShutdownPreservesDeliveredAnswer(t *testing.T) {
	for _, why := range []string{"", "vendor rejected the request"} {
		t.Run(fmt.Sprintf("error=%q", why), func(t *testing.T) {
			f := newFakeHost(t)
			id, c := f.begin(t, true)
			b := readBody(f, id, c, context.Background())
			f.host.dispatch(message{ID: id, Event: "head"})
			f.next(t, c)
			f.host.dispatch(message{ID: id, Event: "chunk", Data: base64.StdEncoding.EncodeToString([]byte("hi"))})
			answer := message{ID: id}
			if why != "" {
				answer = errAnswer(why)
				answer.ID = id
			}
			f.host.dispatch(answer)
			if c.state != callDraining || len(f.slots) != 1 {
				t.Fatal("terminal did not retain draining capacity")
			}
			f.host.fail(errHostGone)
			if c.state != callDraining || c.result().Error != answer.Error || c.queuedChunks() != 1 || len(f.slots) != 1 {
				t.Fatal("shutdown changed the committed terminal, queue or capacity")
			}
			got, err := io.ReadAll(b)
			if string(got) != "hi" {
				t.Fatalf("body = %q, want hi", got)
			}
			if why == "" && err != nil || why != "" && (err == nil || err.Error() != why) {
				t.Fatalf("terminal error = %v, want %q", err, why)
			}
			if c.state != callReleased || len(f.slots) != 0 {
				t.Fatal("draining did not return capacity")
			}
		})
	}
}

func TestHostShutdownWithoutAnswerEndsRead(t *testing.T) {
	f := newFakeHost(t)
	id, c := f.begin(t, true)
	b := readBody(f, id, c, context.Background())
	f.host.fail(errHostGone)
	<-c.done
	errs := make(chan error, 1)
	go func() {
		_, err := b.Read(make([]byte, 8))
		errs <- err
	}()
	select {
	case err := <-errs:
		if !errors.Is(err, errHostGone) {
			t.Fatalf("read = %v, want errHostGone", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a read without an answer didn't end after shutdown")
	}
}

// TestHostStreamBoundsTheHeadEnvelope: a head's headers are capped by magpie's
// own envelope — entries and serialized bytes — so a vendor's unbounded header
// set is not a free zero-charge line. A head just under both is taken.
func TestHostStreamBoundsTheHeadEnvelope(t *testing.T) {
	headers := func(n, size int) map[string]string {
		h := map[string]string{}
		for i := range n {
			h[fmt.Sprintf("x-header-%03d", i)] = strings.Repeat("v", size)
		}
		return h
	}
	t.Run("legal", func(t *testing.T) {
		f := newFakeHost(t)
		_, c := f.begin(t, true)
		if !c.push(message{Event: "head", Headers: headers(200, 64)}) {
			t.Fatal("a head within the envelope was refused")
		}
	})
	t.Run("too many entries", func(t *testing.T) {
		f := newFakeHost(t)
		_, c := f.begin(t, true)
		if c.push(message{Event: "head", Headers: headers(maxHeadEntries+1, 1)}) {
			t.Fatal("a head past the entry cap was taken")
		}
		if got := c.givenUp(); !errors.Is(got, errProtocolOverrun) {
			t.Fatalf("givenUp = %v, want errProtocolOverrun", got)
		}
	})
	t.Run("too many bytes", func(t *testing.T) {
		f := newFakeHost(t)
		_, c := f.begin(t, true)
		if c.push(message{Event: "head", Headers: headers(maxHeadEntries, 512)}) {
			t.Fatal("a head past the byte cap was taken")
		}
		if got := c.givenUp(); !errors.Is(got, errProtocolOverrun) {
			t.Fatalf("givenUp = %v, want errProtocolOverrun", got)
		}
	})
}

// TestHostStreamHeadChunksFinal: a whole reply — head, chunks, final — is read
// back byte for byte, in order, and every delivered byte is credited back so
// the child may send more.
func TestHostStreamHeadChunksFinal(t *testing.T) {
	f := newFakeHost(t)
	id, c := f.begin(t, true)
	b := readBody(f, id, c, context.Background())
	if h := reply(t, f, id, c, func() {
		for i := range 50 {
			f.chunk(id, chunkAt(i))
		}
	}); h.Status != 200 {
		t.Fatalf("head = %+v", h)
	}
	want := chunks(50)
	got := make([]byte, len(want))
	if _, err := io.ReadFull(b, got); err != nil {
		t.Fatalf("reading: %v", err)
	}
	if string(got) != want {
		t.Fatalf("stream = %q, want %q", got, want)
	}
	// every delivered byte is credited back, plus a frame's overhead as each
	// frame is done, so the child is never left short of what it sent
	wantCredit := len(want) + 49*frameOverhead
	waitFor(t, "the credits were given back", func() bool { return f.credited() == wantCredit })
	if got := c.owedBytes(); got != frameOverhead {
		t.Fatalf("%d bytes still owed after a full read", got)
	}
	f.answer(id, "")
	if n, err := b.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatalf("final read = %d, %v, want 0, EOF", n, err)
	}
}

// TestHostStreamCreditBoundsTheQueue: a child that sends past the window it was
// given broke the protocol — the call is given up on, its queue dropped, and
// the child told to stop, once.
func TestHostStreamCreditBoundsTheQueue(t *testing.T) {
	oldWindow := streamWindow
	streamWindow = 8 + frameOverhead
	t.Cleanup(func() { streamWindow = oldWindow })

	f := newFakeHost(t)
	id, c := f.begin(t, true)
	b := readBody(f, id, c, context.Background())
	reply(t, f, id, c, func() {})

	// a chunk within the window is taken, one past it is not
	if !c.push(message{Event: "chunk", Data: base64.StdEncoding.EncodeToString([]byte("12345678"))}) {
		t.Fatal("a chunk within the window was refused")
	}
	if c.push(message{Event: "chunk", Data: base64.StdEncoding.EncodeToString([]byte("x"))}) {
		t.Fatal("a chunk past the window was taken")
	}
	if got := c.givenUp(); !errors.Is(got, errProtocolOverrun) {
		t.Fatalf("givenUp = %v, want errProtocolOverrun", got)
	}
	if n := c.queuedChunks(); n != 0 {
		t.Fatalf("%d chunks left after a protocol overrun", n)
	}
	if _, err := io.ReadAll(b); !errors.Is(err, errProtocolOverrun) {
		t.Fatalf("read = %v, want errProtocolOverrun", err)
	}
	waitFor(t, "the overrun was aborted once", func() bool {
		got := f.methods()
		return len(got) == 1 && got[0] == "abort"
	})
	// the call is the host's until its abort's write is known, which the
	// child may read before the writer hears it went
	waitFor(t, "the given-up call was released", func() bool { return !f.registered(id) })
}

// TestHostStreamCreditIsPerDeliveredByte: reading part of a chunk credits only
// what it took, so a reader that stops leaves the child paused with the rest.
func TestHostStreamCreditIsPerDeliveredByte(t *testing.T) {
	f := newFakeHost(t)
	id, c := f.begin(t, true)
	b := readBody(f, id, c, context.Background())
	reply(t, f, id, c, func() {
		f.chunk(id, "0123456789")
	})
	waitFor(t, "the chunk was queued", func() bool { return c.queuedChunks() == 1 })
	if n, err := b.Read(make([]byte, 4)); n != 4 || err != nil {
		t.Fatalf("read = %d, %v, want 4 bytes", n, err)
	}
	waitFor(t, "the 4 bytes were credited", func() bool { return f.credited() == 4 })
	// the other six bytes and the frame's overhead are still owed: the child
	// is paused until the rest is read
	if got, want := c.owedBytes(), 10+frameOverhead-4; got != want {
		t.Fatalf("owed = %d, want %d", got, want)
	}
	if n, err := b.Read(make([]byte, 6)); n != 6 || err != nil {
		t.Fatalf("reading the rest: %d, %v", n, err)
	}
	waitFor(t, "the rest was credited", func() bool { return f.credited() >= 10 })
	f.answer(id, "")
	if _, err := io.ReadAll(b); err != nil {
		t.Fatal(err)
	}
}

// TestHostAdmissionLimitFailsTheNewCall: the host admits its budget of calls;
// the one past it fails clearly rather than a live reply being cut.
func TestHostAdmissionLimitFailsTheNewCall(t *testing.T) {
	oldWindow, oldBudget := streamWindow, streamBudget
	oldReserve := rpcReserve
	streamWindow, streamBudget, rpcReserve = 1<<20, 2<<20, 0 // exactly two streaming calls
	t.Cleanup(func() { streamWindow, streamBudget, rpcReserve = oldWindow, oldBudget, oldReserve })

	f := newFakeHost(t)
	for i := range 2 {
		if _, _, err := f.host.begin(true); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if _, _, err := f.host.begin(true); !errors.Is(err, errTooManyCalls) {
		t.Fatalf("the call past the budget = %v, want errTooManyCalls", err)
	}
	// a slot back is a call admitted again
	f.host.calls[1].release()
	if _, _, err := f.host.begin(true); err != nil {
		t.Fatalf("a call after one freed its slot: %v", err)
	}
}

// TestHostCancelAfterHeadStopsTheFetch: a context that ends after the head,
// with nothing reading or closing the body, still stops the upstream fetch.
func TestHostCancelAfterHeadStopsTheFetch(t *testing.T) {
	f := newFakeHost(t)
	id, c := f.begin(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	b := readBody(f, id, c, ctx)
	c.watchCtx(ctx)
	reply(t, f, id, c, func() {})

	cancel()
	waitFor(t, "the fetch was aborted", func() bool { return !f.registered(id) })
	if got := f.methods(); len(got) != 1 || got[0] != "abort" {
		t.Fatalf("control = %v, want just an abort", got)
	}
	if _, err := b.Read(make([]byte, 8)); !errors.Is(err, context.Canceled) {
		t.Fatalf("read = %v, want canceled", err)
	}
	if free := f.slotsFree(); free != cap(f.host.slots) {
		t.Fatalf("%d slots free, want all %d", free, cap(f.host.slots))
	}
}

// TestHostFetchNotSentNoAbort: a context that ends before the fetch reaches the
// writer leaves nothing to abort, and gives the slot straight back.
func TestHostFetchNotSentNoAbort(t *testing.T) {
	f := newFakeHost(t)
	_, c, err := f.host.begin(true)
	if err != nil {
		t.Fatal(err)
	}
	c.settle(context.Canceled)
	waitFor(t, "the call left the host", func() bool { return len(f.host.calls) == 0 })
	if got := f.methods(); len(got) != 0 {
		t.Fatalf("control = %v, want nothing: the child never saw the fetch", got)
	}
	if free := f.slotsFree(); free != cap(f.host.slots) {
		t.Fatalf("%d slots free, want all", free)
	}
}

// TestHostAbortFollowsTheFetch: the fetch is written before the abort that
// follows it, so a cancelled call never starts an upstream it cannot stop.
func TestHostAbortFollowsTheFetch(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("blocked-abort-fail=%t", fail), func(t *testing.T) {
			h := failureTestHost(t)
			w := &blockedAbortWriter{entered: make(chan struct{}), release: make(chan struct{}), methods: make(chan string, 4)}
			h.in = w
			writerDone := make(chan struct{})
			go func() { h.writerLoop(); close(writerDone) }()
			id, c, err := h.begin(true)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.enqueueCall(context.Background(), map[string]any{"id": id, "method": "fetch"}, nil, c); err != nil {
				t.Fatal(err)
			}
			c.settle(context.Canceled)
			c.settle(errBodyClosed)
			pumped := make(chan struct{})
			go func() { h.pumpCtrl(); close(pumped) }()
			<-w.entered
			h.mu.Lock()
			state, registered := c.state, h.calls[id] == c
			h.mu.Unlock()
			h.ctrlMu.Lock()
			controls := len(h.ctrl)
			h.ctrlMu.Unlock()
			if state != callAborting || !registered || controls != 0 || len(h.slots) != 1 {
				t.Fatal("in-flight abort lost ownership or returned capacity before acknowledgement")
			}
			if fail {
				h.fail(errHostGone)
				if len(h.slots) != 0 || len(h.streams) != 0 {
					t.Fatal("host failure relied on pending control map membership")
				}
			}
			close(w.release)
			<-pumped
			if len(h.slots) != 0 || len(h.streams) != 0 {
				t.Fatal("abort acknowledgement retained capacity")
			}
			h.fail(errHostGone)
			<-writerDone
			if first, second := <-w.methods, <-w.methods; first != "fetch" || second != "abort" || len(w.methods) != 0 {
				t.Fatalf("writes = %s, %s; want one fetch then one abort", first, second)
			}
		})
	}
	f := newFakeHost(t)
	_, c, err := f.host.begin(true)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.host.enqueueCall(context.Background(), map[string]any{"id": c.id, "method": "fetch", "params": map[string]any{}}, nil, c); err != nil {
		t.Fatal(err)
	}
	c.settle(context.Canceled)
	waitFor(t, "the abort was written", func() bool {
		for _, m := range f.methods() {
			if m == "abort" {
				return true
			}
		}
		return false
	})
	if got := f.methods(); len(got) != 2 || got[0] != "fetch" || got[1] != "abort" {
		t.Fatalf("control = %v, want fetch then abort", got)
	}
}

type blockedAbortWriter struct {
	entered chan struct{}
	release chan struct{}
	methods chan string
}

func (w *blockedAbortWriter) Write(data []byte) (int, error) {
	var frame struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(data, &frame); err != nil {
		return 0, err
	}
	w.methods <- frame.Method
	if frame.Method == "abort" {
		close(w.entered)
		<-w.release
	}
	return len(data), nil
}

func (w *blockedAbortWriter) Close() error { return nil }

// TestHostBlockedWriterHonorsCtx: when the child's stdin queue is full, an
// enqueue gives up on its context instead of parking forever.
func TestHostBlockedWriterHonorsCtx(t *testing.T) {
	h := &host{out: make(chan writeReq, 1), dead: make(chan struct{})}
	h.out <- writeReq{b: []byte("{}\n")} // the queue is full, and no writer is draining it
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.enqueue(ctx, map[string]any{"id": 1}) }()
	select {
	case err := <-done:
		t.Fatalf("enqueue returned before cancel: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("enqueue = %v, want canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a full stdin queue held the caller past its cancel")
	}
}

// TestHostFinalBeforeDrainHoldsTheSlot: the final answer does not mean the body
// is consumed — the call keeps its slot until the body reaches EOF or Close.
func TestHostFinalBeforeDrainHoldsTheSlot(t *testing.T) {
	f := newFakeHost(t)
	id, c := f.begin(t, true)
	b := readBody(f, id, c, context.Background())
	reply(t, f, id, c, func() {
		for i := range 20 {
			f.chunk(id, chunkAt(i))
		}
		f.answer(id, "")
	})
	waitFor(t, "the answered call left the host's hands", func() bool { return !f.registered(id) })
	if free := f.slotsFree(); free == cap(f.host.slots) {
		t.Fatalf("the slot came back before the body was read")
	}
	if _, err := io.ReadAll(b); err != nil {
		t.Fatalf("reading: %v", err)
	}
	waitFor(t, "the slot came back at EOF", func() bool { return f.slotsFree() == cap(f.host.slots) })
}

// TestHostStreamKeepsChunkOrderAndAnswer: chunks before an answer that failed
// are still read, in order, then the error.
func TestHostStreamKeepsChunkOrderAndAnswer(t *testing.T) {
	f := newFakeHost(t)
	id, c := f.begin(t, true)
	b := readBody(f, id, c, context.Background())
	reply(t, f, id, c, func() {
		f.chunk(id, "one")
		f.chunk(id, "two")
		f.answer(id, "the vendor exploded")
	})
	got, err := io.ReadAll(b)
	if err == nil || !strings.Contains(err.Error(), "the vendor exploded") {
		t.Fatalf("error = %v, want the plugin's", err)
	}
	if string(got) != "onetwo" {
		t.Fatalf("stream = %q, want %q", got, "onetwo")
	}
}

// TestHostBodyCancelEndsAStalledRead: ctx ending must end a read waiting on the
// plugin, and give the call up with it.
func TestHostBodyCancelEndsAStalledRead(t *testing.T) {
	f := newFakeHost(t)
	id, c := f.begin(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	b := readBody(f, id, c, ctx)
	reply(t, f, id, c, func() {}) // the head: no chunks ever come
	c.watchCtx(ctx)

	errs := make(chan error, 1)
	go func() {
		_, err := b.Read(make([]byte, 8))
		errs <- err
	}()
	select {
	case err := <-errs:
		t.Fatalf("the read ended before cancel: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-errs:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("read = %v, want canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel didn't end a read waiting on the plugin")
	}
	waitFor(t, "the abort was acknowledged after cancel", func() bool { return !f.registered(id) })
}

// TestHostBodyCloseEndsAnIdleRead: closing a body nothing was ever queued for
// must end a read parked on it, and give the call up.
func TestHostBodyCloseEndsAnIdleRead(t *testing.T) {
	f := newFakeHost(t)
	id, c := f.begin(t, true)
	b := readBody(f, id, c, context.Background())
	reply(t, f, id, c, func() {})
	errs := make(chan error, 1)
	go func() {
		_, err := b.Read(make([]byte, 8))
		errs <- err
	}()
	time.Sleep(20 * time.Millisecond)
	if err := b.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case err := <-errs:
		if !errors.Is(err, errBodyClosed) {
			t.Fatalf("a read parked at close = %v, want errBodyClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closing the body didn't end a read parked on it")
	}
	if _, err := b.Read(make([]byte, 8)); !errors.Is(err, errBodyClosed) {
		t.Fatalf("a read after close = %v, want errBodyClosed", err)
	}
	waitFor(t, "the call left the host", func() bool { return !f.registered(id) })
}

// TestHostPushAfterCloseIsNotQueued: a chunk the reader is already gone for is
// not queued again, whatever dispatch still holds.
func TestHostPushAfterCloseIsNotQueued(t *testing.T) {
	f := newFakeHost(t)
	id, c := f.begin(t, true)
	b := readBody(f, id, c, context.Background())
	reply(t, f, id, c, func() {})
	if err := b.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if c.push(message{Event: "chunk", Data: base64.StdEncoding.EncodeToString([]byte("hi"))}) {
		t.Fatal("a chunk was queued for a body already given up on")
	}
	if n := c.queuedChunks(); n != 0 {
		t.Fatalf("%d chunks queued for a closed body", n)
	}
}

// TestHostBodyZeroLengthRead: a read that asks for nothing reads nothing, and
// takes nothing from the call.
func TestHostBodyZeroLengthRead(t *testing.T) {
	f := newFakeHost(t)
	id, c := f.begin(t, true)
	b := readBody(f, id, c, context.Background())
	reply(t, f, id, c, func() {
		f.chunk(id, "hi")
		f.answer(id, "")
	})
	waitFor(t, "the chunk was queued", func() bool { return c.queuedChunks() == 1 })
	if n, err := b.Read(nil); n != 0 || err != nil {
		t.Fatalf("zero-length read = %d, %v, want 0, nil", n, err)
	}
	if n := c.queuedChunks(); n != 1 {
		t.Fatalf("a zero-length read took a chunk: %d left", n)
	}
	got, err := io.ReadAll(b)
	if err != nil || string(got) != "hi" {
		t.Fatalf("body = %q, %v, want hi", got, err)
	}
}

// TestHostShutdownEndsAStalledRead: the host quitting answers its pending call
// with why, so a read waiting on it ends rather than waiting for ever.
func TestHostShutdownEndsAStalledRead(t *testing.T) {
	f := newFakeHost(t)
	id, c := f.begin(t, true)
	b := readBody(f, id, c, context.Background())
	reply(t, f, id, c, func() {})
	errs := make(chan error, 1)
	go func() {
		_, err := b.Read(make([]byte, 8))
		errs <- err
	}()
	time.Sleep(20 * time.Millisecond)
	_ = f.pw.CloseWithError(io.EOF) // the child's stdout ends: it quit
	select {
	case err := <-errs:
		if err == nil || (!errors.Is(err, errHostGone) && !strings.Contains(err.Error(), "plugin host quit")) {
			t.Fatalf("read = %v, want the host's", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the host quitting didn't end a read waiting on it")
	}
}

// TestHostHeadKeepsTheAnswerWhenItCameWithTheHead: a head read can find the
// call's answer already there and take the head out of the queue with it. The
// answer must reach the body all the same, an error staying an error.
func TestHostHeadKeepsTheAnswerWhenItCameWithTheHead(t *testing.T) {
	for _, tc := range []struct {
		name string
		why  string
	}{
		{"answered", ""},
		{"failed", "the vendor exploded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeHost(t)
			id, c := f.begin(t, true)
			type read struct {
				head   message
				answer *message
				err    error
			}
			ctx := context.Background()
			out := make(chan read, 1)
			go func() {
				h, a, err := c.headRead(ctx)
				out <- read{h, a, err}
			}()
			// the window the race makes: the answer is there, and a head is
			// queued with no wake for it
			time.Sleep(20 * time.Millisecond)
			c.mu.Lock()
			c.queue = append(c.queue, message{Status: 200})
			c.mu.Unlock()
			c.closed.Store(true)
			if tc.why == "" {
				c.done <- message{}
			} else {
				c.done <- errAnswer(tc.why)
			}
			r := <-out
			if r.err != nil || r.head.Status != 200 {
				t.Fatalf("head = %+v, %v", r.head, r.err)
			}
			if r.answer == nil {
				t.Fatal("the answer was lost: the body would never end")
			}
			b := &body{h: f.host, id: id, c: c, ctx: ctx, answer: r.answer, closed: make(chan struct{})}
			got, err := io.ReadAll(b)
			if len(got) != 0 {
				t.Fatalf("body = %q, want nothing", got)
			}
			switch {
			case tc.why == "":
				if err != nil {
					t.Fatalf("body ended with %v, want a clean end", err)
				}
			case err == nil || !strings.Contains(err.Error(), tc.why):
				t.Fatalf("body ended with %v, want the plugin's %q", err, tc.why)
			}
		})
	}
}

// TestHostBodyReadStopsAtCloseWithBufferedData: what is left of a chunk, and
// what is queued behind it, is not read after the body is closed.
func TestHostBodyReadStopsAtCloseWithBufferedData(t *testing.T) {
	f := newFakeHost(t)
	id, c := f.begin(t, true)
	b := readBody(f, id, c, context.Background())
	reply(t, f, id, c, func() {
		f.chunk(id, "first")
		f.chunk(id, "second")
		f.answer(id, "")
	})
	if n, err := b.Read(make([]byte, 1)); n != 1 || err != nil {
		t.Fatalf("read = %d, %v, want one byte", n, err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if n, err := b.Read(make([]byte, 16)); n != 0 || !errors.Is(err, errBodyClosed) {
		t.Fatalf("read after close = %d, %v, want 0, errBodyClosed", n, err)
	}
}

// TestHostBodyCloseDuringRead: a close under a read ends it, and leaves no race
// on what the read holds.
type blockedDoneContext struct {
	context.Context
	entered chan struct{}
	release chan struct{}
}

func (ctx *blockedDoneContext) Done() <-chan struct{} {
	close(ctx.entered)
	<-ctx.release
	return nil
}

func TestHostBodyCloseDuringRead(t *testing.T) {
	f := newFakeHost(t)
	id, c := f.begin(t, true)
	ctx := &blockedDoneContext{Context: context.Background(), entered: make(chan struct{}), release: make(chan struct{})}
	b := readBody(f, id, c, ctx)
	done := make(chan error, 1)
	go func() {
		_, err := b.Read(make([]byte, 8))
		done <- err
	}()
	<-ctx.entered
	_ = b.Close()
	close(ctx.release)
	select {
	case err := <-done:
		if !errors.Is(err, errBodyClosed) {
			t.Fatalf("read = %v, want errBodyClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a read didn't end after the body closed")
	}
	waitFor(t, "exactly one abort acknowledged", func() bool {
		return len(f.slots) == 0 && len(f.methods()) == 1 && f.methods()[0] == "abort"
	})
}

// TestHostAbortIsQueuedOncePerCall: whoever takes the call out of the host's
// hands tells the child, so a call is aborted once however many of its paths
// get there — and an answered one not at all.
func TestHostAbortIsQueuedOncePerCall(t *testing.T) {
	t.Run("overrun then close", func(t *testing.T) {
		oldWindow := streamWindow
		streamWindow = 8
		t.Cleanup(func() { streamWindow = oldWindow })

		f := newFakeHost(t)
		id, c := f.begin(t, true)
		b := readBody(f, id, c, context.Background())
		reply(t, f, id, c, func() {})
		c.push(message{Event: "chunk", Data: base64.StdEncoding.EncodeToString([]byte("12345678"))})
		c.push(message{Event: "chunk", Data: base64.StdEncoding.EncodeToString([]byte("x"))}) // past the window
		if _, err := io.ReadAll(b); !errors.Is(err, errProtocolOverrun) {
			t.Fatalf("read = %v, want the protocol overrun", err)
		}
		if err := b.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		waitFor(t, "the call was aborted once", func() bool {
			got := f.methods()
			return len(got) == 1 && got[0] == "abort"
		})
	})

	t.Run("answered then close", func(t *testing.T) {
		f := newFakeHost(t)
		id, c := f.begin(t, true)
		b := readBody(f, id, c, context.Background())
		reply(t, f, id, c, func() {
			f.chunk(id, "hi")
			f.answer(id, "")
		})
		if got, err := io.ReadAll(b); err != nil || string(got) != "hi" {
			t.Fatalf("body = %q, %v", got, err)
		}
		if f.registered(id) {
			t.Fatal("an answered call is still the host's")
		}
		if err := b.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		for _, m := range f.methods() {
			if m == "abort" {
				t.Fatal("an answered call was aborted")
			}
		}
	})
}

// TestHostStreamRefusesBadFrames: a chunk that is not base64 of one to maxFrame
// bytes — empty, malformed or oversized — is refused before it is queued, so it
// cannot spend the window wrong or flood the queue. A well-formed frame of any
// padding is taken.
func TestHostStreamRefusesBadFrames(t *testing.T) {
	for _, data := range []string{"YQ==", "YWI=", "YWJj", "YWJjZA=="} {
		f := newFakeHost(t)
		_, c := f.begin(t, true)
		if !c.push(message{Event: "chunk", Data: data}) {
			t.Fatalf("the frame %q was refused", data)
		}
	}
	for _, tc := range []struct{ name, data string }{
		{"empty", ""},
		{"not base64", "Y!Jj"},
		{"not a whole group", "YQ="},
		{"padding in the middle", "YQ==YQ=="},
		{"oversized", base64.StdEncoding.EncodeToString(make([]byte, maxFrame+1))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeHost(t)
			_, c := f.begin(t, true)
			if c.push(message{Event: "chunk", Data: tc.data}) {
				t.Fatal("a bad frame was queued")
			}
			if got := c.givenUp(); !errors.Is(got, errProtocolOverrun) {
				t.Fatalf("givenUp = %v, want errProtocolOverrun", got)
			}
			if n := c.queuedChunks(); n != 0 {
				t.Fatalf("%d chunks queued for a bad frame", n)
			}
		})
	}
}

// TestHostStreamFrameOverheadBoundsTheQueue: tiny frames cannot fill a window
// with metadata — each costs its bytes plus frameOverhead, so a window of N
// frame costs holds at most N of them, whatever their size.
func TestHostStreamFrameOverheadBoundsTheQueue(t *testing.T) {
	oldWindow := streamWindow
	streamWindow = 8 * (1 + frameOverhead) // room for exactly eight 1-byte frames
	t.Cleanup(func() { streamWindow = oldWindow })

	f := newFakeHost(t)
	_, c := f.begin(t, true)
	for i := range 8 {
		if !c.push(message{Event: "chunk", Data: "YQ=="}) { // one byte
			t.Fatalf("frame %d was refused within the window", i)
		}
	}
	if c.push(message{Event: "chunk", Data: "YQ=="}) {
		t.Fatal("a ninth tiny frame fit a window of eight frame costs")
	}
	// a child that sends past its window broke the protocol: the queue is
	// dropped rather than grown, and the call is given up on
	if got := c.givenUp(); !errors.Is(got, errProtocolOverrun) {
		t.Fatalf("givenUp = %v, want errProtocolOverrun", got)
	}
	if got := c.queuedChunks(); got != 0 {
		t.Fatalf("%d frames left after a protocol overrun", got)
	}
}

// errAnswer is the answer a plugin's fetch that failed gives.
func errAnswer(why string) message {
	return message{Error: &struct {
		Message string `json:"message"`
	}{why}}
}

var _ = base64.StdEncoding

func failureTestHost(t *testing.T) *host {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h := &host{
		in: discardCloser{}, calls: map[int64]*call{}, dead: make(chan struct{}),
		slots: make(chan struct{}, callLimit()), streams: make(chan struct{}, streamLimit()),
		out: make(chan writeReq, callLimit()), ctrl: map[int64]*ctrl{},
		ctrlWake: make(chan struct{}, 1), ctx: ctx, cancel: cancel,
	}
	t.Cleanup(func() { h.fail(errHostGone) })
	return h
}

func TestHostFailureRejectsLateWork(t *testing.T) {
	t.Run("admission", func(t *testing.T) {
		h := failureTestHost(t)
		h.fail(errHostGone)
		for _, stream := range []bool{false, true} {
			id, c, err := h.begin(stream)
			if !errors.Is(err, errHostGone) || id != 0 || c != nil {
				t.Fatalf("begin(%t) = %d, %v, %v, want no call and errHostGone", stream, id, c, err)
			}
		}
		if len(h.slots) != 0 || len(h.streams) != 0 || len(h.calls) != 0 {
			t.Fatal("failed host retained admission")
		}
	})
	t.Run("enqueue", func(t *testing.T) {
		h := failureTestHost(t)
		h.fail(errHostGone)
		for range 100 {
			if err := h.enqueue(context.Background(), map[string]any{"body": "late payload"}); !errors.Is(err, errHostGone) {
				t.Fatalf("enqueue = %v, want errHostGone", err)
			}
		}
		if len(h.out) != 0 {
			t.Fatalf("failed host retained %d writes", len(h.out))
		}
	})
	t.Run("control", func(t *testing.T) {
		h := failureTestHost(t)
		_, c, err := h.begin(true)
		if err != nil {
			t.Fatal(err)
		}
		c.transition(callAccepted, nil, nil)
		h.abort(c)
		h.fail(errHostGone)
		h.abort(c)
		h.credit(c, 1)
		if len(h.ctrl) != 0 || len(h.slots) != 0 || len(h.streams) != 0 {
			t.Fatalf("failed host retained controls=%d slots=%d streams=%d", len(h.ctrl), len(h.slots), len(h.streams))
		}
	})
}

func TestHostFailureRacesAdmissionAndCancel(t *testing.T) {
	for _, order := range []string{"failure-before-admission", "cancellation-before-enqueue", "enqueue-before-cancellation", "terminal-before-failure", "failure-before-terminal"} {
		t.Run(order, func(t *testing.T) {
			h := failureTestHost(t)
			if order == "failure-before-admission" {
				h.fail(errHostGone)
				if _, _, err := h.begin(true); !errors.Is(err, errHostGone) {
					t.Fatal(err)
				}
				return
			}
			id, c, err := h.begin(true)
			if err != nil {
				t.Fatal(err)
			}
			fetch := map[string]any{"id": id, "method": "fetch"}
			if order == "cancellation-before-enqueue" {
				c.settle(context.Canceled)
				if err := h.enqueueCall(context.Background(), fetch, nil, c); !errors.Is(err, context.Canceled) {
					t.Fatalf("late enqueue: %v", err)
				}
				if len(h.out) != 0 || len(h.ctrl) != 0 || len(h.slots) != 0 {
					t.Fatal("unsent fetch retained work")
				}
				return
			}
			if err := h.enqueueCall(context.Background(), fetch, nil, c); err != nil {
				t.Fatal(err)
			}
			switch order {
			case "enqueue-before-cancellation":
				c.settle(context.Canceled)
				c.settle(errBodyClosed)
				if c.state != callAborting || len(h.slots) != 1 || len(h.ctrl) != 1 {
					t.Fatal("accepted cancellation did not retain one abort")
				}
				h.fail(errHostGone)
				if !errors.Is(c.givenUp(), context.Canceled) {
					t.Fatal("failure replaced first outcome")
				}
			case "terminal-before-failure":
				h.dispatch(message{ID: id, Result: json.RawMessage(`null`)})
				terminal := c.result()
				h.fail(errHostGone)
				c.transition(callHostFailed, errHostGone, nil)
				if c.state != callDraining || c.result() != terminal || len(h.slots) != 1 {
					t.Fatal("failure replaced terminal or returned capacity")
				}
				c.release()
			case "failure-before-terminal":
				h.fail(errHostGone)
				h.dispatch(message{ID: id, Result: json.RawMessage(`null`)})
				if !errors.Is(c.givenUp(), errHostGone) || c.result().Error == nil {
					t.Fatal("late answer replaced host failure")
				}
			}
			if c.state != callReleased || len(h.slots) != 0 || len(h.streams) != 0 {
				t.Fatal("lifecycle did not return capacity")
			}
		})
	}
	for iteration := range 200 {
		h := failureTestHost(t)
		start := make(chan struct{})
		finished := make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			defer close(finished)
			<-start
			id, c, err := h.begin(true)
			if err != nil {
				return
			}
			c.watchCtx(ctx)
			_ = h.enqueueCall(ctx, map[string]any{"id": id, "method": "fetch", "params": map[string]any{"body": "late payload"}}, nil, c)
			c.settle(context.Canceled)
			h.credit(c, 1)
		}()
		close(start)
		cancel()
		h.fail(errHostGone)
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d: cancelled fetch did not return", iteration)
		}
		if len(h.slots) != 0 || len(h.streams) != 0 || len(h.calls) != 0 || len(h.ctrl) != 0 || len(h.out) != 0 {
			t.Fatalf("iteration %d: slots=%d streams=%d calls=%d controls=%d writes=%d", iteration, len(h.slots), len(h.streams), len(h.calls), len(h.ctrl), len(h.out))
		}
	}
}

func TestHostEnqueueWaitsForRoom(t *testing.T) {
	t.Run("cancel-unsent-owner", func(t *testing.T) {
		h := failureTestHost(t)
		h.out = make(chan writeReq, 1)
		h.out <- writeReq{b: []byte("{}\n")}
		id, c, err := h.begin(true)
		if err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() {
			result <- h.enqueueCall(context.Background(), map[string]any{"id": id, "method": "fetch"}, nil, c)
		}()
		waitFor(t, "unsent fetch waiting for writer room", func() bool {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.outSpace != nil
		})
		c.settle(context.Canceled)
		<-c.wake
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("writer waiter depended on the reader's notification")
		}
		if len(h.out) != 1 || len(h.slots) != 0 || len(h.ctrl) != 0 {
			t.Fatal("cancelled unsent fetch retained admission or queued work")
		}
	})
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("fail=%t", fail), func(t *testing.T) {
			h := failureTestHost(t)
			h.out = make(chan writeReq, 1)
			if err := h.enqueue(context.Background(), map[string]any{"id": 1}); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() { result <- h.enqueue(context.Background(), map[string]any{"id": 2}) }()
			waitFor(t, "enqueue waiting for room", func() bool {
				h.mu.Lock()
				defer h.mu.Unlock()
				return h.outSpace != nil
			})
			if fail {
				h.fail(errHostGone)
			} else {
				go h.writerLoop()
			}
			select {
			case err := <-result:
				if fail && !errors.Is(err, errHostGone) || !fail && err != nil {
					t.Fatalf("enqueue = %v, fail=%t", err, fail)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("enqueue did not wake after room or failure")
			}
		})
	}
}
