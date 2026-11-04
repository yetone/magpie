package gateway

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
)

// Bound a single body and its read time without limiting how long a client
// may wait for a provider slot or how many streams a team gateway may serve.
//
// The limit is on the decoded body. Codex sends the whole conversation on
// every turn, its screenshots inline as base64, and zstd-compressed to a
// ChatGPT account, which magpie compresses again on the way out: a long
// session that read many images went past 128 MB decoded and got magpie's
// 413 on every retry, while the same turn is a fraction of that on the
// wire (NeoPengZhile, #1422). 512 MB leaves such a session room and still
// bounds what one request may hold in memory.
const (
	defaultBodyLimit   = 512 << 20
	defaultBodyTimeout = 60 * time.Second
)

// codexBodyMost bounds codexBodyLimit on any machine.
const codexBodyMost = 2 << 30

// codexBodyLimit is the decoded limit of a turn on Codex's own backend
// path (CodexPath, the relay, its WebSocket): a sixteenth of what
// magpie's processes may use (bodyRoomSize's memory), defaultBodyLimit at
// least and codexBodyMost at most. Codex keeps every image it generated
// in the conversation, a ~3 MB PNG as ~4 MB of base64 each, and over
// HTTP sends the conversation whole every turn: a session that generated
// and revised images for a while went past 512 MB and got magpie's 413,
// while Codex signed in to OpenAI directly kept going (dallascao on
// Discord). One of Codex's own models is sent on to its sign-in as it
// came, at about four times the body's size in Go heap at its peak; a
// turn for a magpie model is translated, at several times that, and keeps
// defaultBodyLimit (codexBackend). The bound still holds a zstd bomb: no
// body decodes past it.
func codexBodyLimit() int64 {
	codexLimit.Lock()
	defer codexLimit.Unlock()
	if codexLimit.goos != memGOOS || codexLimit.root != memRoot || codexLimit.n == 0 {
		limit, _ := memLimit(memGOOS, memRoot)
		if limit <= 0 && memOwn() {
			limit = proc.MemTotal()
		}
		codexLimit.goos, codexLimit.root = memGOOS, memRoot
		codexLimit.n = min(max(limit/16, defaultBodyLimit), codexBodyMost)
	}
	return codexLimit.n
}

// codexLimit is codexBodyLimit as last read, for the memory it was read
// from (memGOOS and memRoot change only in tests).
var codexLimit struct {
	sync.Mutex
	goos, root string
	n          int64
}

// codexLimitOf is the decoded limit of a request on Codex's own path to
// s: its requestLimits' when set (a test's), else codexBodyLimit.
func (s *Server) codexLimitOf() int64 {
	if s.requestLimits.body > 0 {
		return s.requestLimits.body
	}
	return codexBodyLimit()
}

// bodyLimitOf is s's decoded limit for every other request.
func (s *Server) bodyLimitOf() int64 {
	if s.requestLimits.body > 0 {
		return s.requestLimits.body
	}
	return defaultBodyLimit
}

type requestLimits struct {
	body        int64
	readTimeout time.Duration
}

func (s *Server) requestBody(w http.ResponseWriter, r *http.Request, from provider.Protocol) ([]byte, bool) {
	return s.readRequestBody(w, r, from, 0)
}

func (s *Server) readRequestBody(w http.ResponseWriter, r *http.Request, from provider.Protocol, limit int64) ([]byte, bool) {
	limits := s.requestLimits
	if limit > 0 {
		limits.body = limit
	}
	// wait for room before reading: a body waiting is not held (bodyRoom)
	most := limits.body
	if most <= 0 {
		most = defaultBodyLimit
	}
	if n := r.ContentLength; n > 0 && n <= most {
		if err := bodyRoom.take(r.Context(), n); err != nil {
			w.Header().Set("Connection", "close") // its body is left unread
			writeError(w, from, http.StatusServiceUnavailable, err.Error())
			return nil, false
		}
	}
	body, status, err := readBoundedRequestBody(w, r, limits)
	if err != nil {
		writeError(w, from, status, err.Error())
		return nil, false
	}
	if err := bodyRoom.take(r.Context(), int64(len(body))); err != nil {
		writeError(w, from, http.StatusServiceUnavailable, err.Error())
		return nil, false
	}
	holdBody(r.Context(), body)
	return body, true
}

// idleBody renews the socket deadline on wire bytes, including reads made by
// decoder initialization. Decompressed output must not keep a stalled socket alive.
type idleBody struct {
	io.ReadCloser
	controller *http.ResponseController
	idle       time.Duration
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		if e := b.controller.SetReadDeadline(time.Now().Add(b.idle)); e != nil && !errors.Is(e, http.ErrNotSupported) {
			return n, e
		}
	}
	return n, err
}

// errBodyEncoding is a Content-Encoding magpie can't decode.
var errBodyEncoding = errors.New("unsupported Content-Encoding")

// decodedBody reads r.Body through its Content-Encoding: Codex sends its
// bodies zstd, to the ChatGPT backend path and to a custom provider's
// /v1/responses alike (#1223), and other clients gzip. The header is
// dropped once decoded, so nothing after this sees or forwards it.
func decodedBody(r *http.Request) (io.ReadCloser, error) {
	var rd io.ReadCloser = io.NopCloser(r.Body)
	switch enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))); enc {
	case "", "identity":
	case "zstd":
		d := bodyDecoders.Get().(*zstd.Decoder)
		if err := d.Reset(r.Body); err != nil {
			d.Reset(nil)
			bodyDecoders.Put(d)
			return nil, err
		}
		rd = &pooledZstd{d}
	case "gzip", "x-gzip":
		g, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, fmt.Errorf("request body is not gzip: %w", err)
		}
		rd = g
	default:
		return nil, fmt.Errorf("%w: magpie can't read a %q body; send it as identity, gzip or zstd", errBodyEncoding, enc)
	}
	r.Header.Del("Content-Encoding")
	return rd, nil
}

// bodyDecoders keeps zstd decoders between requests' bodies. One made for
// each body (concurrent, the package's default) allocated its window and
// block buffers again every time: 1.7 GB of 9.8 GB allocated in 10 minutes
// of three Codex sessions' turns (procmem heap profile). One decodes in
// step with its reader, as the body is read through to its end anyway, and
// is let go with the pool's other idle ones by the garbage collector.
var bodyDecoders = sync.Pool{New: func() any {
	d, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(defaultBodyLimit))
	if err != nil {
		panic(err) // only for options it doesn't know
	}
	return d
}}

// pooledZstd is a body read through a pooled decoder, which goes back to
// the pool at Close; a read after it can't reach the decoder.
type pooledZstd struct{ d *zstd.Decoder }

func (z *pooledZstd) Read(p []byte) (int, error) {
	if z.d == nil {
		return 0, net.ErrClosed
	}
	return z.d.Read(p)
}

func (z *pooledZstd) Close() error {
	if z.d != nil {
		z.d.Reset(nil)
		bodyDecoders.Put(z.d)
		z.d = nil
	}
	return nil
}

// Read one bounded body, decoded; callers choose their protocol's error
// envelope. The limit holds for the decoded bytes too.
func readBoundedRequestBody(w http.ResponseWriter, r *http.Request, limits requestLimits) ([]byte, int, error) {
	controller := http.NewResponseController(w)
	reject := func(status int, err error) ([]byte, int, error) {
		// Do not let net/http drain a rejected body from a stalled client.
		w.Header().Set("Connection", "close")
		controller.SetReadDeadline(time.Now())
		if r.Body != nil {
			r.Body.Close()
		}
		controller.SetReadDeadline(time.Time{})
		return nil, status, err
	}
	if limits.body <= 0 {
		limits.body = defaultBodyLimit
	}
	if limits.readTimeout <= 0 {
		limits.readTimeout = defaultBodyTimeout
	}
	if r.ContentLength > limits.body {
		return reject(http.StatusRequestEntityTooLarge, overLimit(limits.body))
	}
	if err := controller.SetReadDeadline(time.Now().Add(limits.readTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return reject(http.StatusInternalServerError, errors.New("could not set request body deadline"))
	}
	defer controller.SetReadDeadline(time.Time{})
	readFailure := func(err error) ([]byte, int, error) {
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			return reject(http.StatusRequestTimeout, errors.New("request body read timed out"))
		}
		return reject(http.StatusBadRequest, err)
	}
	// the wire length is the body's length only when it is sent as it is
	declared := r.ContentLength
	if enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))); enc != "" && enc != "identity" {
		declared = -1
	}
	original := r.Body
	r.Body = &idleBody{original, controller, limits.readTimeout}
	defer func() { r.Body = original }()
	rd, err := decodedBody(r)
	if errors.Is(err, errBodyEncoding) {
		return reject(http.StatusUnsupportedMediaType, err)
	}
	if err != nil {
		return readFailure(err)
	}
	defer rd.Close()
	// Limit decoded bytes as well.
	body, err := readSized(io.LimitReader(rd, limits.body+1), declared)
	if int64(len(body)) > limits.body {
		return reject(http.StatusRequestEntityTooLarge, overLimit(limits.body))
	}
	if err != nil {
		return readFailure(err)
	}
	return body, 0, nil
}

// sizedReadFirst is the most readSized sets aside before a byte has come:
// a client that says more than it sends makes magpie hold no more than
// this, or twice what it sent.
const sizedReadFirst = 1 << 20

// readSized reads r to its end into a slice the size the client declared,
// when it declared one (declared > 0): io.ReadAll reads into chunks and
// copies them into a slice of their sum, two copies of every body, and an
// agent sends its whole conversation, ~1 MB, on every turn (yg6888 on X:
// ~1 GB). Content-Length is not trusted for the allocation: room grows by
// doubling, from sizedReadFirst, up to what was declared, and straight to
// it once that is within a quarter, so an honest body of 1 MB or less is
// read into one slice of its own size, and a larger one into a final slice
// of its own size, with at most 2.6 times the body allocated in all (a
// doubling that fell just short of it made that 3). A body shorter than it
// said is held in at most two and a half times what it sent. A body longer
// than it said reads on, as io.ReadAll would. The error is io.ReadAll's: nil at io.EOF.
func readSized(r io.Reader, declared int64) ([]byte, error) {
	if declared <= 0 {
		return io.ReadAll(r)
	}
	b := make([]byte, 0, min(declared, sizedReadFirst))
	for {
		if len(b) == cap(b) {
			if int64(len(b)) >= declared {
				// all it said has come: look for its end before growing
				var probe [512]byte
				n, err := r.Read(probe[:])
				if n > 0 {
					b = append(b, probe[:n]...)
				}
				if err == io.EOF {
					return b, nil
				}
				if err != nil {
					return b, err
				}
				continue
			}
			next := min(int64(cap(b))*2, declared)
			if declared-next < next/4 {
				// within a quarter of what it said: a doubling that
				// fell just short would copy the whole body again for
				// its last few bytes (a 516 MB turn read into 512 MB,
				// then all of it into 516 MB, #5281)
				next = declared
			}
			b = append(make([]byte, 0, next), b...)
		}
		n, err := r.Read(b[len(b):cap(b)])
		b = b[:len(b)+n]
		if err == io.EOF {
			return b, nil
		}
		if err != nil {
			return b, err
		}
	}
}

// overLimit says a body is over magpie's own limit, and that it is
// magpie's, not the provider's: an agent shows the message as it comes,
// and a bare "Payload Too Large" read as the model's or the vendor's
// (#1422). writeError adds what to do about it.
func overLimit(limit int64) error {
	return fmt.Errorf("request body is over magpie's gateway limit of %s (decoded); the provider was not asked", sizeWord(limit))
}

// sizeWord is a byte count in MB, or in KB or bytes when it is smaller,
// for limits set low (a test's, a route's own).
func sizeWord(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MB", n>>20)
	case n >= 1<<10 && n%(1<<10) == 0:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}
