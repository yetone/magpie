package gateway

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// lateListener is a listener that was closed before the server's
// shutdown: the WSL ones are, as serveWSL closes them when the gateway's
// context ends (#7729). Its second close says the network connection is
// closed, and the server's Serve stays in Accept until then, as the WSL
// listener's own Serve is not back when the shutdown closes it again.
type lateListener struct {
	net.Listener
	mu      sync.Mutex
	closed  bool
	release chan struct{}
	once    sync.Once
}

func (l *lateListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		return c, nil
	}
	<-l.release
	return nil, net.ErrClosed
}

func (l *lateListener) Close() error {
	l.mu.Lock()
	already := l.closed
	l.closed = true
	l.mu.Unlock()
	if !already {
		return l.Listener.Close()
	}
	l.once.Do(func() { close(l.release) })
	return net.ErrClosed
}

// A listener closed before the shutdown is not a cut request. On Windows
// serveWSL closes the listeners it opened on the WSL networks when the
// gateway's context ends, and the shutdown closes them again: its second
// close says the network connection is closed, and the drain reads that
// as a timeout and says the requests in flight were cut — every Windows
// quit said so, with no request in flight (#7729).
func TestShutdownIgnoresAListenerClosedBefore(t *testing.T) {
	fresh(t)
	logged := &bytes.Buffer{}
	old := log.Writer()
	log.SetOutput(logged)
	t.Cleanup(func() { log.SetOutput(old) })

	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := &lateListener{Listener: raw, release: make(chan struct{})}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	// the server serves: its Serve is running and holds the listener
	res, err := (&http.Client{Timeout: 10 * time.Second}).Get("http://" + raw.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	// the gateway's own end closes the listener, as serveWSL closes the
	// WSL ones when the context ends
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	s := &Server{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	s.shutdownServer(srv, ctx)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the drain took %v", d)
	}
	if strings.Contains(logged.String(), "requests still in flight") {
		t.Fatalf("a listener closed before the shutdown was read as a cut request: %s", logged.String())
	}
}

// A request still answering when the drain is over is cut, and the log
// says so: what the drain reads as a cut is the request that is cut.
func TestShutdownCutsARequestStillAnswering(t *testing.T) {
	fresh(t)
	logged := &bytes.Buffer{}
	old := log.Writer()
	log.SetOutput(logged)
	t.Cleanup(func() { log.SetOutput(old) })

	started := make(chan struct{})
	cut := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(cut)
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	go func() {
		res, err := (&http.Client{}).Get("http://" + ln.Addr().String() + "/")
		if err == nil {
			res.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the request never reached the server")
	}

	s := &Server{}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	s.shutdownServer(srv, ctx)
	if !strings.Contains(logged.String(), "requests still in flight") {
		t.Fatalf("a request cut at the end of the drain was not logged: %s", logged.String())
	}
	select {
	case <-cut:
	case <-time.After(10 * time.Second):
		t.Fatal("the request was left answering after the drain")
	}
}
