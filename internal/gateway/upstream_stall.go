package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"
)

// bodyStall is how long the sending of a request body may make no
// progress before that try is given up on: the time between one read of
// the body finishing and the next beginning, which is when the transport
// writes what it read, and, over HTTP/2, waits for the vendor's flow
// control window to open. A vendor that answers the connection's health
// ping but never sends WINDOW_UPDATE leaves the sender in that wait for
// good: the ping is answered, so the connection is healthy, and the
// header wait never starts as the request isn't sent (@imty42 on X:
// net.send 535971ms, goroutine in http2clientStream.awaitFlowControl,
// "499 after 8m56s"). A read of the body that is itself slow is not
// counted: the clock runs while the transport holds a chunk, not while
// the body produces one. A body going out slowly still moves a chunk
// (16KB to 1MB) well within this, down to about 200 bytes a second.
var bodyStall = 90 * time.Second

// stalledWords mark errUploadStalled in a failure's message.
const stalledWords = "request body stalled"

// errUploadStalled is a try ended because its body's sending made no
// progress for bodyStall. The vendor never had the whole request, so
// nothing was done for it and no account is at fault (failure).
type errUploadStalled struct{ after time.Duration }

func (e errUploadStalled) Error() string {
	return fmt.Sprintf("%s: nothing more could be sent for %s, as the vendor's connection stopped taking data (its flow-control window stayed shut); the connection was dropped, and the vendor never had the whole request", stalledWords, e.after.Round(time.Millisecond))
}

// stallGuard bounds every request's body sending (bodyStall): a try whose
// body makes no progress is cancelled, the connection it was on is closed
// so no request is put on it again, and the request is sent once more on
// a new connection when its body can be had again (GetBody), else it ends
// with errUploadStalled. It never limits the wait for a reply: a long
// stream, or a vendor that thinks, is the header wait's and the
// connection health check's.
type stallGuard struct{ next http.RoundTripper }

func (g stallGuard) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := g.try(r)
	stalled, ok := err.(errUploadStalled)
	if !ok || r.GetBody == nil {
		return res, err
	}
	// once more, on a new connection. The one just closed can still be
	// the pool's for a moment, and a request put on it fails as closed:
	// that is asked again, a few times, not counted as the vendor's
	for n := 0; r.Context().Err() == nil; n++ {
		body, gerr := r.GetBody()
		if gerr != nil {
			return nil, stalled
		}
		again := r.Clone(r.Context())
		again.Body = body
		res, err = g.try(again)
		if err == nil || n >= 3 || !errors.Is(err, net.ErrClosed) && !strings.Contains(err.Error(), "use of closed network connection") {
			return res, err
		}
		select {
		case <-r.Context().Done():
		case <-time.After(20 * time.Millisecond):
		}
	}
	return nil, stalled
}

func (g stallGuard) try(r *http.Request) (*http.Response, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return g.next.RoundTrip(r)
	}
	ctx, cancel := context.WithCancelCause(r.Context())
	// Go's transports send a request again on another connection (a kept
	// one found closed) with a body from GetBody, which must be watched as
	// this one is, or a retried send is not bounded
	st := &stallTry{}
	st.onStall = func() {
		cancel(errUploadStalled{after: bodyStall})
		st.dropConn()
	}
	sb := st.watch(r.Body)
	trace := &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) { st.setConn(i.Conn) }}
	out := r.WithContext(httptrace.WithClientTrace(ctx, trace))
	out.Body = sb
	if r.GetBody != nil {
		out.GetBody = func() (io.ReadCloser, error) {
			rc, err := r.GetBody()
			if err != nil {
				return nil, err
			}
			return st.watch(rc), nil
		}
	}
	res, err := g.next.RoundTrip(out)
	if err != nil {
		st.stop()
		cause := context.Cause(ctx)
		cancel(nil)
		if s, ok := cause.(errUploadStalled); ok {
			return nil, s
		}
		return nil, err
	}
	if res.StatusCode == http.StatusSwitchingProtocols {
		return res, nil
	}
	// the reply's body outlives this call: its context ends with it
	res.Body = &cancelOnClose{ReadCloser: res.Body, cancel: func() { st.stop(); cancel(nil) }}
	return res, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel func()
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// stallTry is one try's watch over its body, or bodies, as a transport
// may take a new one from GetBody, and over the connection it is on.
type stallTry struct {
	onStall func()

	mu   sync.Mutex
	cur  *stallBody
	conn net.Conn
}

func (st *stallTry) watch(rc io.ReadCloser) *stallBody {
	b := &stallBody{ReadCloser: rc, after: bodyStall, st: st}
	st.mu.Lock()
	old := st.cur
	st.cur = b
	st.mu.Unlock()
	if old != nil {
		old.halt()
	}
	return b
}

func (st *stallTry) stop() {
	st.mu.Lock()
	b := st.cur
	st.mu.Unlock()
	if b != nil {
		b.halt()
	}
}

func (st *stallTry) setConn(c net.Conn) {
	st.mu.Lock()
	st.conn = c
	st.mu.Unlock()
}

func (st *stallTry) dropConn() {
	st.mu.Lock()
	c := st.conn
	st.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

// stallBody is a request body that times the gaps between its reads.
type stallBody struct {
	io.ReadCloser
	after time.Duration
	st    *stallTry
	// onStall replaces st's, for a test
	onStall func()

	mu    sync.Mutex
	timer *time.Timer
	done  bool
}

func (b *stallBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	if b.timer != nil {
		b.timer.Stop()
	}
	b.mu.Unlock()
	n, err := b.ReadCloser.Read(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	if err != nil || b.done {
		b.done = true
		return n, err
	}
	// the transport now holds a chunk to send; the next read is due soon
	b.timer = time.AfterFunc(b.after, b.fire)
	return n, err
}

func (b *stallBody) fire() {
	b.mu.Lock()
	if b.done {
		b.mu.Unlock()
		return
	}
	b.done = true
	b.mu.Unlock()
	if b.onStall != nil {
		b.onStall()
	} else if b.st != nil {
		b.st.onStall()
	}
}

// halt ends the watch: the body is done, or another has taken its place.
func (b *stallBody) halt() {
	b.mu.Lock()
	b.done = true
	if b.timer != nil {
		b.timer.Stop()
	}
	b.mu.Unlock()
}

// Close ends the watch too: a body its transport has closed isn't being
// sent any more.
func (b *stallBody) Close() error {
	b.halt()
	return b.ReadCloser.Close()
}

// CloseIdleConnections is next's, where it keeps any.
func (g stallGuard) CloseIdleConnections() {
	if c, ok := g.next.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}
