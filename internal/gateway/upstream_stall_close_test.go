package gateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type stallClosingConn struct {
	net.Conn
	closing chan struct{}
	release chan struct{}
}

func (c *stallClosingConn) Close() error {
	close(c.closing)
	<-c.release
	return c.Conn.Close()
}

type stallCloseTransport func(*http.Request) (*http.Response, error)

func (f stallCloseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Cancellation wakes RoundTrip before the timer finishes closing the old
// connection. A retry must not reach the pool until that close has finished.
func TestStalledBodyRetryWaitsForConnectionClose(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	c := &stallClosingConn{Conn: a, closing: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(c.release) }) }
	var calls atomic.Int32
	retried := make(chan struct{})
	retryErr := errors.New("retry reached a new connection")
	g := stallGuard{next: stallCloseTransport(func(r *http.Request) (*http.Response, error) {
		defer r.Body.Close()
		if calls.Add(1) > 1 {
			close(retried)
			return nil, retryErr
		}
		httptrace.ContextClientTrace(r.Context()).GotConn(httptrace.GotConnInfo{Conn: c})
		go r.Body.(*stallBody).fire()
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://vendor/", strings.NewReader("body"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := g.RoundTrip(req); done <- err }()
	defer func() {
		release()
		select {
		case err := <-done:
			if !errors.Is(err, retryErr) {
				t.Errorf("retry result: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("request did not finish")
		}
	}()
	select {
	case <-c.closing:
	case <-ctx.Done():
		t.Fatal("stalled connection was not closed")
	}
	select {
	case <-retried:
		t.Fatal("retry began before the stalled connection finished closing")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case <-retried:
	case <-ctx.Done():
		t.Fatal("retry did not begin after close")
	}
}
