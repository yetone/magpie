package gateway

import "context"

// upBytes is what a request's tries sent up to the vendor, kept in the
// request's context as a tool call's mcpUse is: forwardOnce adds a try's
// bytes where it already has them (sent), and the request's ledger row
// reads the sum, so the row says what a session's upstream traffic was
// and a hand-on's re-send stands beside Tries (#4456).
type upBytes struct {
	wire  int // the bytes that went on the wire, tries summed
	plain int // the last try's body before compression
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
	}
}
