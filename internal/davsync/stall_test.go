package davsync

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// shortStall makes the watchdogs short for a test.
func shortStall(t *testing.T, stall, meta time.Duration) {
	s, m := stallAfter, metaLimit
	stallAfter, metaLimit = stall, meta
	t.Cleanup(func() { stallAfter, metaLimit = s, m })
}

// slowDAV is a WebDAV server on a slow line: the backup goes both ways in
// chunks of chunk bytes every gap, or stops after stopAt bytes for good. A
// PUT is answered tail after its body is all in: the time the last of it,
// handed over by magpie, would take to cross the line.
type slowDAV struct {
	chunk  int
	gap    time.Duration
	tail   time.Duration
	stopAt int           // 0: never
	quiet  chan struct{} // closed when the test is done with a stopped server
	mu     sync.Mutex
	stored []byte
}

// stop holds the request without a byte either way until the client gives
// up, or the test ends (a server doesn't see a client gone before it has
// read the body).
func (s *slowDAV) stop(r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-s.quiet:
	}
}

func (s *slowDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPut:
		var got bytes.Buffer
		got.Grow(int(r.ContentLength))
		buf := make([]byte, s.chunk)
		for {
			if s.stopAt > 0 && got.Len() >= s.stopAt {
				s.stop(r) // the line went quiet
				return
			}
			n, err := io.ReadFull(r.Body, buf)
			got.Write(buf[:n])
			if err != nil {
				break
			}
			time.Sleep(s.gap)
		}
		time.Sleep(s.tail)
		s.mu.Lock()
		s.stored = got.Bytes()
		s.mu.Unlock()
		w.Header().Set("ETag", `"v1"`)
		w.WriteHeader(http.StatusCreated)
	case http.MethodHead:
		s.mu.Lock()
		w.Header().Set("Content-Length", strconv.Itoa(len(s.stored)))
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		s.mu.Lock()
		data := s.stored
		s.mu.Unlock()
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Header().Set("ETag", `"v1"`)
		w.WriteHeader(http.StatusOK)
		for i := 0; i < len(data); i += s.chunk {
			if i > 0 { // the answer ends with its last chunk
				time.Sleep(s.gap)
			}
			if s.stopAt > 0 && i >= s.stopAt {
				s.stop(r)
				return
			}
			w.Write(data[i:min(i+s.chunk, len(data))])
			w.(http.Flusher).Flush()
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// pipeLine serves h on in-process pipes, and gives the client that reaches
// it. A pipe holds no bytes of its own: what magpie has handed over is what
// the server has read, so the server's gaps and tail are the line's. Run in
// a synctest bubble, the clock moves only once everything waits, so the
// watchdog keeps its real length, and a test process the system holds
// still (a loaded machine, swapping) doesn't move it: on a TCP line with
// the watchdog scaled down to 300 ms, a test held still that long woke to
// the watchdog's timer before the write it would have seen.
func pipeLine(t *testing.T, h http.Handler) *http.Client {
	l := &pipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
	srv := &http.Server{Handler: h}
	go srv.Serve(l)
	tr := &http.Transport{DialContext: l.dial}
	// a bubble ends with every goroutine in it: a request a failed test
	// left is let run out on the bubble's clock
	t.Cleanup(func() {
		tr.CloseIdleConnections()
		srv.Shutdown(context.Background())
	})
	return &http.Client{Transport: stallTransport{tr}}
}

type pipeListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (l *pipeListener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	c, s := net.Pipe()
	select {
	case l.conns <- s:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

// A backup that takes longer than the watchdog to go up and come back down,
// but never stops moving, is written and read in full (#657: 43 MB at
// 305 KB/s took 2.4 minutes, and a sync was given 2). The watchdog is the
// real one, on the bubble's clock.
func TestSlowTransferIsNotCutShort(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// #657's line; the PUT's last bytes cross it twice the watchdog
		// after magpie has handed them all over
		s := &slowDAV{chunk: 305 << 10, gap: time.Second, tail: 2 * stallAfter}
		d, err := newDAV(Config{URL: "http://dav.test"})
		if err != nil {
			t.Fatal(err)
		}
		d.client = pipeLine(t, s)
		data := bytes.Repeat([]byte("magpie skill "), (43<<20)/13)
		ctx := context.Background()
		start := time.Now()
		if _, err := d.put(ctx, data, ""); err != nil {
			t.Fatalf("slow upload: %v", err)
		}
		up := time.Since(start)
		start = time.Now()
		got, _, err := d.get(ctx, version{})
		if err != nil {
			t.Fatalf("slow download: %v", err)
		}
		down := time.Since(start)
		if !bytes.Equal(got, data) {
			t.Fatalf("read back %d bytes of %d", len(got), len(data))
		}
		if up < 2*time.Minute || down < 2*time.Minute {
			t.Fatalf("the transfers (%s up, %s down) took no longer than a sync used to be given", up, down)
		}
	})
}

// A server that stops in the middle, either way, fails the sync soon after
// it stops, saying so — not after however long a sync may take.
func TestStalledTransferFailsPromptly(t *testing.T) {
	shortStall(t, 300*time.Millisecond, 300*time.Millisecond)
	data := bytes.Repeat([]byte("x"), 32<<20) // more than the system buffers hold
	for _, way := range []string{"up", "down"} {
		t.Run(way, func(t *testing.T) {
			s := &slowDAV{chunk: 64 << 10, gap: 10 * time.Millisecond, quiet: make(chan struct{})}
			if way == "up" {
				s.stopAt = 1 << 20
			} else {
				s.stored = data
				s.stopAt = 1 << 20
			}
			srv := httptest.NewServer(s)
			defer srv.Close()
			defer close(s.quiet)
			d, err := newDAV(Config{URL: srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			start := time.Now()
			if way == "up" {
				_, err = d.put(ctx, data, "")
			} else {
				_, _, err = d.get(ctx, version{})
			}
			took := time.Since(start)
			var st *errStalled
			if !errors.As(err, &st) {
				t.Fatalf("%v after %s, want a stall", err, took)
			}
			if took > 10*time.Second {
				t.Fatalf("a stall took %s to tell", took)
			}
		})
	}
}

// A request that moves only a little (PROPFIND, MKCOL…) has metaLimit in
// all, however it trickles.
func TestMetadataRequestHasALimit(t *testing.T) {
	shortStall(t, 300*time.Millisecond, 300*time.Millisecond)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	d, err := newDAV(Config{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = d.mkcol(context.Background())
	var st *errStalled
	if !errors.As(err, &st) || time.Since(start) > 3*time.Second {
		t.Fatalf("%v after %s, want the limit", err, time.Since(start))
	}
}
