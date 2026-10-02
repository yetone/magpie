package gateway

import (
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Bound a single body and its read time without limiting how long a client
// may wait for a provider slot or how many streams a team gateway may serve.
const (
	defaultBodyLimit   = 128 << 20
	defaultBodyTimeout = 60 * time.Second
)

type requestLimits struct {
	body        int64
	readTimeout time.Duration
}

func (s *Server) requestBody(w http.ResponseWriter, r *http.Request, from provider.Protocol) ([]byte, bool) {
	return s.readRequestBody(w, r, from, nil, 0)
}

func (s *Server) readRequestBody(w http.ResponseWriter, r *http.Request, from provider.Protocol, decode func(*http.Request) (io.ReadCloser, error), limit int64) ([]byte, bool) {
	limits := s.requestLimits
	if limit > 0 {
		limits.body = limit
	}
	body, status, err := readBoundedRequestBody(w, r, limits, decode)
	if err != nil {
		writeError(w, from, status, err.Error())
		return nil, false
	}
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

// Read one bounded body; callers choose their protocol's error envelope.
func readBoundedRequestBody(w http.ResponseWriter, r *http.Request, limits requestLimits, decode func(*http.Request) (io.ReadCloser, error)) ([]byte, int, error) {
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
		return reject(http.StatusRequestEntityTooLarge, errors.New("request body exceeds gateway limit"))
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
	original := r.Body
	r.Body = &idleBody{original, controller, limits.readTimeout}
	defer func() { r.Body = original }()
	rd := r.Body
	if decode != nil {
		var err error
		rd, err = decode(r)
		if err != nil {
			return readFailure(err)
		}
		defer rd.Close()
	}
	// Limit decoded bytes as well, without allocating from Content-Length.
	body, err := io.ReadAll(io.LimitReader(rd, limits.body+1))
	if int64(len(body)) > limits.body {
		return reject(http.StatusRequestEntityTooLarge, errors.New("request body exceeds gateway limit"))
	}
	if err != nil {
		return readFailure(err)
	}
	return body, 0, nil
}
