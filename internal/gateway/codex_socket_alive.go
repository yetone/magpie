package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// A socket to the backend may die without a close: a proxy's TUN or a
// NAT drops the flow, and the connection under the socket, often to a
// proxy on this computer, stays open. Nothing then says so: a write goes
// into the local buffers, or waits once they are full, and a read waits
// for ever (M4n5ter on X: codex/* through magpie often hangs; #7448 waited
// 10 minutes in a kept socket's Write). TCP's keepalive is answered by the
// local proxy, so only the socket's own ping goes the whole way.
//
// So each socket has a reader for its whole life (pump), which a turn
// reads its frames from, and a watch: once nothing came for codexSocketQuiet
// while the reader waits, the socket is pinged, and one whose pong doesn't
// come in codexSocketPong (and the time what was sent since anything came
// takes at codexSocketSlowest) is dead. A live backend answers a ping while
// its model thinks, so a turn that thinks for minutes before its first
// frame (#6163 saw 66 to 155 s) is waited for as before. A write that moves
// no bytes for codexSocketStall is dead too: a slow upload still moves.
//
// A dead socket is closed. Kept, it is let go; one a turn has just taken
// is pinged once the turn is written, and a dead one sends the turn whole
// on a new socket (errCodexSocketAgain), as any kept socket that fails
// before the reply is handed on; a new socket's turn goes over HTTP; and a
// reply under way ends cut, as a dropped connection's does. A socket the
// backend closes while kept is let go then, not at codexKeepIdle.

var (
	// codexSocketQuiet is how long a socket waiting on the backend hears
	// nothing before it is pinged.
	codexSocketQuiet = 30 * time.Second
	// codexSocketPong is how long a ping's pong is waited for, beyond
	// the time what was sent ahead of it takes (codexSocketSlowest).
	codexSocketPong = 20 * time.Second
	// codexSocketStall is how long a write may move no bytes.
	codexSocketStall = 2 * time.Minute
	// codexSocketTick is how often a socket is looked at.
	codexSocketTick = 5 * time.Second
)

// codexAliveBounds are the bounds above as a socket read them when it
// opened.
type codexAliveBounds struct{ quiet, pong, stall, tick time.Duration }

// codexSocketSlowest is the uplink, in bytes a second, the pong's wait
// allows for what was sent ahead of the ping, which the pong comes after.
const codexSocketSlowest = 8 << 10

// codexWireChunk is the most written to the connection at once, so a
// write that moves is told from one that doesn't.
const codexWireChunk = 16 << 10

// codexFrame is a read of the socket: a message or the error that ended it.
type codexFrame struct {
	typ  websocket.MessageType
	data []byte
	err  error
}

// codexWire is the connection under a socket, counting what is written
// to it and when a write last moved.
type codexWire struct {
	rwc     io.ReadWriteCloser
	written atomic.Int64
	// since is when the write under way last moved, 0 for none under way
	since atomic.Int64
}

func (w *codexWire) Read(p []byte) (int, error) { return w.rwc.Read(p) }
func (w *codexWire) Close() error               { return w.rwc.Close() }

func (w *codexWire) Write(p []byte) (n int, err error) {
	defer w.since.Store(0)
	for len(p) > 0 {
		c := p[:min(len(p), codexWireChunk)]
		w.since.Store(time.Now().UnixNano())
		m, err := w.rwc.Write(c)
		n += m
		w.written.Add(int64(m))
		if err != nil {
			return n, err
		}
		p = p[m:]
	}
	return n, nil
}

// stalled says a write has moved nothing for d.
func (w *codexWire) stalled(d time.Duration) bool {
	s := w.since.Load()
	return s != 0 && time.Since(time.Unix(0, s)) >= d
}

// codexWireTrip hands the socket's handshake on to next and the
// connection it upgrades to, as wire, to the socket.
type codexWireTrip struct {
	next http.RoundTripper
	wire *codexWire
}

func (t codexWireTrip) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := t.next.RoundTrip(req)
	if err == nil && res.StatusCode == http.StatusSwitchingProtocols {
		if rwc, ok := res.Body.(io.ReadWriteCloser); ok {
			t.wire.rwc = rwc
			res.Body = t.wire
		}
	}
	return res, err
}

// errCodexSocketDead says the socket's watch found it dead.
var errCodexSocketDead = errors.New("codex websocket: the connection went silent")

// start runs k's reader and its watch until ctx, the socket's, ends.
func (k *codexKept) start(ctx context.Context) {
	k.bounds = codexAliveBounds{codexSocketQuiet, codexSocketPong, codexSocketStall, codexSocketTick}
	k.frames = make(chan codexFrame)
	k.probe = make(chan struct{}, 1)
	k.done = ctx.Done()
	k.hear()
	go k.pump(ctx)
	go k.watch(ctx)
}

// hear notes that the backend was heard from.
func (k *codexKept) hear() {
	if k.wire != nil {
		k.heardAt.Store(k.wire.written.Load())
	}
	k.heard.Store(time.Now().UnixNano())
}

// pump reads k's socket for its whole life, handing each read to the turn
// reading it; one that ends the socket while it is kept lets it go.
func (k *codexKept) pump(ctx context.Context) {
	for {
		k.reading.Store(true)
		typ, data, err := k.c.Read(ctx)
		k.reading.Store(false)
		k.reads.Add(1)
		if err == nil {
			k.hear()
		} else {
			codexKeepDrop(k)
		}
		select {
		case k.frames <- codexFrame{typ, data, err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

// next is k's next read, for a turn whose context is ctx.
func (k *codexKept) next(ctx context.Context) (websocket.MessageType, []byte, error) {
	select {
	case f := <-k.frames:
		if f.err != nil && k.dead.Load() != nil {
			return 0, nil, k.deadErr()
		}
		return f.typ, f.data, f.err
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	case <-k.done:
		if k.dead.Load() != nil {
			return 0, nil, k.deadErr()
		}
		return 0, nil, fmt.Errorf("failed to read: %w", io.ErrClosedPipe)
	}
}

func (k *codexKept) deadErr() error {
	return fmt.Errorf("%w (%s)", errCodexSocketDead, *k.dead.Load())
}

// probed has k's watch ping it now, as a turn sent on it once kept.
func (k *codexKept) probed() {
	select {
	case k.probe <- struct{}{}:
	default:
	}
}

func (k *codexKept) watch(ctx context.Context) {
	t := time.NewTicker(k.bounds.tick)
	defer t.Stop()
	for {
		probe := false
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-k.probe:
			probe = true
		}
		if why := k.check(ctx, probe); why != "" {
			k.die(why)
			return
		}
	}
}

// check is why k is dead, "" while it isn't known to be.
func (k *codexKept) check(ctx context.Context, probe bool) string {
	if k.wire.stalled(k.bounds.stall) {
		return fmt.Sprintf("a write moved nothing for %s", k.bounds.stall)
	}
	if k.writing.Load() || !k.reading.Load() {
		// a ping waits behind a turn's message; with no read under way,
		// as while the client takes its time, no pong would be read
		return ""
	}
	if !probe && time.Since(time.Unix(0, k.heard.Load())) < k.bounds.quiet {
		return ""
	}
	reads, heard := k.reads.Load(), k.heard.Load()
	ahead := max(k.wire.written.Load()-k.heardAt.Load(), 0)
	wait := k.bounds.pong + time.Duration(ahead)*time.Second/codexSocketSlowest
	pctx, cancel := context.WithTimeout(ctx, wait)
	err := k.c.Ping(pctx)
	cancel()
	if err == nil || ctx.Err() != nil {
		return ""
	}
	if k.heard.Load() != heard || k.reads.Load() != reads || !k.reading.Load() {
		// heard from, or the read the pong would come in ended
		return ""
	}
	if pctx.Err() == nil {
		// closed under the ping: the reader says why
		return ""
	}
	return fmt.Sprintf("no pong in %s", wait.Round(time.Millisecond))
}

// die closes k, found dead for why, and lets it go if it is kept.
func (k *codexKept) die(why string) {
	if !k.dead.CompareAndSwap(nil, &why) {
		return
	}
	codexKeepDrop(k)
	k.close()
}
