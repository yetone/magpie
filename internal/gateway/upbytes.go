package gateway

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
)

// upBytes is what a request's tries sent up to the vendor, kept in the
// request's context as a tool call's mcpUse is: forwardOnce adds a try's
// bytes where it already has them (sent), and the request's ledger row
// reads the sum, so the row says what a session's upstream traffic was
// and a hand-on's re-send stands beside Tries (#4456).
type upBytes struct {
	wire  int // the bytes that went on the wire, tries summed
	plain int // the last try's body before compression
	// down is the bytes of the vendor's replies read, tries summed: added
	// as a body is read, which can be on another goroutine than the
	// request's (a stream's pump), so atomic
	down atomic.Int64
	// saved is what DeepSeek's image files kept out of the tries' bodies,
	// less what uploading them cost this request (withDeepSeekFiles)
	saved int
	// images is how many inline images went by file instead, the most of
	// any one try: a retry sends the same ones again
	images int
	// last is what sentUp last counted, the try's body as HTTP sends it;
	// socket says that try went over the Responses WebSocket instead
	// (overSocket), which counted what it was sent in its place
	last   int
	socket bool
	// ws is what the socket was written for that try so far (wroteSocket)
	ws int
}

type upBytesKey struct{}

func upBytesOf(ctx context.Context) *upBytes {
	u, _ := ctx.Value(upBytesKey{}).(*upBytes)
	return u
}

// sentUp is that a try went to the vendor: wire the bytes it sent
// (len(body), or what CodexCompressed left on req), plain the body before
// that. Read and written on the request's own goroutine — the ledger row
// is assembled after the last try came back — so no lock.
func sentUp(ctx context.Context, wire, plain int) {
	if u := upBytesOf(ctx); u != nil {
		u.wire += wire
		u.plain = plain
		u.last, u.socket, u.ws = wire, false, 0
	}
}

// wroteSocket counts a message the Responses WebSocket was sent for the
// try sentUp last counted: a kept socket's delta, or the whole turn on a
// new one.
func wroteSocket(ctx context.Context, n int) {
	if u := upBytesOf(ctx); u != nil {
		u.ws += n
	}
}

// sentOnSocket settles that try once the socket is done with it. A turn
// the socket answered (only) sent what the socket was written, not the
// body sentUp counted: #5781's matrix0487 saw 13 MB a row while a kept
// socket was sent the new input alone. One that went over HTTP after
// all sent both.
func sentOnSocket(ctx context.Context, only bool) {
	u := upBytesOf(ctx)
	if u == nil {
		return
	}
	if only {
		u.wire += u.ws - u.last
		u.last = u.ws
	} else {
		u.wire += u.ws
		u.last += u.ws
	}
	u.socket, u.ws = only, 0
}

// gotDown counts res's body for the request's ledger row as it is read
// (upBytes.down), and gives res back. A body is counted as Go's client
// hands it over: after the transport undid a gzip it asked for, so a
// gzipped reply counts more than its wire bytes. A 101's body is the
// connection itself and is left as it is.
func gotDown(ctx context.Context, res *http.Response) *http.Response {
	u := upBytesOf(ctx)
	if u == nil || res == nil || res.Body == nil || res.Body == http.NoBody || res.StatusCode == http.StatusSwitchingProtocols {
		return res
	}
	res.Body = &downCount{ReadCloser: res.Body, u: u}
	return res
}

type downCount struct {
	io.ReadCloser
	u *upBytes
}

func (d *downCount) Read(b []byte) (int, error) {
	n, err := d.ReadCloser.Read(b)
	d.u.down.Add(int64(n))
	return n, err
}

// downOf is the reply bytes counted so far (upBytes.down).
func (u *upBytes) downOf() int { return int(u.down.Load()) }
