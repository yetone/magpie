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
			if s.stopAt > 0 && i >= s.stopAt {
				s.stop(r)
				return
			}
			w.Write(data[i:min(i+s.chunk, len(data))])
			w.(http.Flusher).Flush()
			time.Sleep(s.gap)
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// narrowLine serves h, and gives the client that reaches it, with the
// line's buffers held small. The system hands a writer room in bursts: Linux wakes it once a third
// of its send buffer has drained and opens the receiver's window by as much,
// and with the MBs it tunes both up to on a fast loopback, a body moving at
// 6.4 MB/s went 150–290 ms between two of magpie's writes, which a test's
// watchdog of 300 ms didn't always outlast though the line never stopped (at
// the real 60 s that is a line under ~25 KB/s). Small buffers keep the
// bursts to a chunk or two; what sits in buffers after the body is handed
// over is the server's tail.
func narrowLine(t *testing.T, h http.Handler) (*httptest.Server, *http.Client) {
	const buffer = 128 << 10
	srv := httptest.NewUnstartedServer(h)
	srv.Listener = narrowListener{srv.Listener, buffer}
	srv.Start()
	t.Cleanup(srv.Close)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	dial := tr.DialContext
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := dial(ctx, network, addr)
		if err == nil {
			err = narrow(c, buffer)
		}
		return c, err
	}
	t.Cleanup(tr.CloseIdleConnections)
	return srv, &http.Client{Transport: stallTransport{tr}}
}

type narrowListener struct {
	net.Listener
	buffer int
}

func (l narrowListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		err = narrow(c, l.buffer)
	}
	return c, err
}

// narrow keeps both ways of a connection small: the PUT goes up through the
// client's send buffer and the server's receive buffer, the GET comes down
// through the other two, and on macOS a download through the tuned-up pair
// burst as the upload had (run 37693090857: "took nothing for 300ms (GET)").
func narrow(c net.Conn, buffer int) error {
	tc := c.(*net.TCPConn)
	if err := tc.SetReadBuffer(buffer); err != nil {
		return err
	}
	return tc.SetWriteBuffer(buffer)
}

// A backup that takes far longer than the watchdog to go up and come back
// down, but never stops moving, is written and read in full (#657: 43 MB
// at 305 KB/s took 2.4 minutes, and a sync was given 2).
func TestSlowTransferIsNotCutShort(t *testing.T) {
	// a second, not the 300 ms the other stall tests use: a macOS runner
	// can stop the whole test for a few hundred ms, and here the line never
	// stops, so such a pause is all a shorter watchdog would catch (red
	// on macOS in run 37693090857, near the end of the GET)
	shortStall(t, time.Second, time.Second)
	// the PUT's last MBs cross the line twice the watchdog after magpie has
	// handed them all over
	s := &slowDAV{chunk: 256 << 10, gap: 40 * time.Millisecond, tail: 2 * stallAfter}
	srv, client := narrowLine(t, s)
	d, err := newDAV(Config{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	d.client = client
	// ~24 MB at 6.4 MB/s: nearly 4 s each way
	data := bytes.Repeat([]byte("magpie skill "), (24<<20)/13)
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
	if up < 3*stallAfter || down < 3*stallAfter {
		t.Fatalf("the transfers (%s up, %s down) weren't slow enough to test anything", up, down)
	}
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
