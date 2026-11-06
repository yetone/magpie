package netproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"syscall"
	"testing"
)

// A request under OverHTTP1 goes over HTTP/1.1 to every host, through a
// provider's proxy (With) too; one without it over HTTP/2 as before.
func TestOverHTTP1EveryHost(t *testing.T) {
	clearProxyEnv(t)
	srv, d := h2Server(t, nil)
	other, _ := h2Server(t, nil)
	h1 := OverHTTP1(context.Background())
	for _, u := range []string{srv.URL, other.URL} {
		if got := protoOf(t, d, h1, u); got != "HTTP/1.1" {
			t.Errorf("%s under OverHTTP1 spoken %s", u, got)
		}
	}
	if got := protoOf(t, d, With(h1, ""), srv.URL); got != "HTTP/1.1" {
		t.Errorf("With dropped OverHTTP1: spoken %s", got)
	}
	if got := protoOf(t, d, context.Background(), srv.URL); got != "HTTP/2.0" {
		t.Errorf("not asked, spoken %s", got)
	}
}

// AgainOverHTTP1 tries once more over HTTP/1.1 after a transfer an
// HTTP/2 stream reset broke off, and not after a failure of another kind
// or a context that ended.
func TestAgainOverHTTP1(t *testing.T) {
	clearProxyEnv(t)
	srv, d := h2Server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		io.WriteString(w, strings.Repeat("x", 4096))
		w.(http.Flusher).Flush()
		if r.ProtoMajor == 2 {
			panic(http.ErrAbortHandler)
		}
		io.WriteString(w, strings.Repeat("x", 1000000-4096))
	})
	c := &http.Client{Transport: d}
	var protos []string
	get := func(ctx context.Context) error {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
		res, err := c.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		protos = append(protos, res.Proto)
		_, err = io.Copy(io.Discard, res.Body)
		return err
	}
	if err := AgainOverHTTP1(context.Background(), get); err != nil {
		t.Fatalf("AgainOverHTTP1: %v (protos %q)", err, protos)
	}
	if fmt.Sprint(protos) != "[HTTP/2.0 HTTP/1.1]" {
		t.Fatalf("protos = %q", protos)
	}

	n := 0
	notNet := errors.New("checksum")
	AgainOverHTTP1(context.Background(), func(context.Context) error { n++; return notNet })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	AgainOverHTTP1(ctx, func(context.Context) error { n++; return io.ErrUnexpectedEOF })
	if n != 2 {
		t.Fatalf("tried %d times, want 2 (no second try)", n)
	}
}

func TestBrokeOff(t *testing.T) {
	for _, err := range []error{
		io.ErrUnexpectedEOF,
		fmt.Errorf("read: %w", syscall.ECONNRESET),
		errors.New("stream error: stream ID 1; INTERNAL_ERROR; received from peer"),
		errors.New("http2: server sent GOAWAY and closed the connection"),
		errors.New("http2: client connection lost"),
	} {
		if !BrokeOff(err) {
			t.Errorf("BrokeOff(%v) = false", err)
		}
	}
	for _, err := range []error{nil, context.Canceled, fmt.Errorf("x: %w", context.DeadlineExceeded), errors.New("download magpie: 404 Not Found")} {
		if BrokeOff(err) {
			t.Errorf("BrokeOff(%v) = true", err)
		}
	}
}
