package netproxy

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type http1Key struct{}

// WithHTTP1 is ctx for requests made on behalf of a provider the user
// added and asked to be spoken HTTP/1.1 to (kepner_53726 on Discord: a
// company gateway whose HTTP/2 connections broke mid-reply with "tls: bad
// record MAC", while HTTP/1.1 to it held): a request through a transport
// Dispatch made to the host of one of bases goes over HTTP/1.1 only,
// through a transport kept for HTTP/1.1. Any other host is spoken to as
// before. With (a request made for another provider) drops it.
func WithHTTP1(ctx context.Context, bases ...string) context.Context {
	hosts := map[string]bool{}
	for _, b := range bases {
		u, err := url.Parse(strings.TrimSpace(b))
		if err != nil || u.Hostname() == "" {
			continue
		}
		hosts[hostKey(u)] = true
	}
	if len(hosts) == 0 {
		return ctx
	}
	return context.WithValue(ctx, http1Key{}, hosts)
}

func http1Of(ctx context.Context) map[string]bool {
	if ctx == nil {
		return nil
	}
	h, _ := ctx.Value(http1Key{}).(map[string]bool)
	return h
}

// http1Asked says req's provider asked for HTTP/1.1 to its host
// (WithHTTP1).
func http1Asked(req *http.Request) bool {
	hosts := http1Of(req.Context())
	return len(hosts) > 0 && req.URL != nil && hosts[hostKey(req.URL)]
}

// h2Broken is how long a host:port whose HTTP/2 connection carried a TLS
// record that didn't decrypt (recordError) is spoken HTTP/1.1 to.
const h2Broken = 6 * time.Hour

var h1For struct {
	sync.Mutex
	at map[string]time.Time // host:port → when its HTTP/2 broke
}

// h2BrokenFor says the HTTP/2 connection to req's host:port broke so
// within h2Broken.
func h2BrokenFor(req *http.Request) bool {
	if req.URL == nil || !strings.EqualFold(req.URL.Scheme, "https") {
		return false
	}
	k := hostKey(req.URL)
	h1For.Lock()
	defer h1For.Unlock()
	at, ok := h1For.at[k]
	if ok && time.Since(at) > h2Broken {
		delete(h1For.at, k)
		return false
	}
	return ok
}

func markH2Broken(req *http.Request) {
	if req.URL == nil {
		return
	}
	h1For.Lock()
	defer h1For.Unlock()
	if h1For.at == nil {
		h1For.at = map[string]time.Time{}
	}
	h1For.at[hostKey(req.URL)] = time.Now()
}

// recordError says err is TLS finding the bytes it read are no record of
// its connection: one that fails its MAC ("bad record MAC"), or a header
// that is no record's ("received record with version eaf1", an oversized
// length). The connection is dead after it, and Go's transport drops it;
// what is said here is that the stream under the TLS was mangled, by the
// far side or by a box between, which HTTP/2's one connection for every
// request to a host met far more often than HTTP/1.1's.
func recordError(err error) bool {
	if err == nil {
		return false
	}
	var rh tls.RecordHeaderError
	if errors.As(err, &rh) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "bad record MAC") || strings.Contains(s, "received record with version") ||
		strings.Contains(s, "oversized record received")
}

// h2Watch is the body of a reply that came over HTTP/2: a read that fails
// with a TLS record error (recordError) has its host spoken HTTP/1.1 to
// for h2Broken, so the requests after it don't go out on another HTTP/2
// connection to break the same way.
type h2Watch struct {
	io.ReadCloser
	req *http.Request
}

func (b h2Watch) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && err != io.EOF && recordError(err) {
		markH2Broken(b.req)
	}
	return n, err
}
