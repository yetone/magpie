// Package stall ends a download that stops sending, and never one that is
// still moving, however slowly. A wall-clock cap on a whole download
// failed a slow link part way while its bytes were still coming (#5572:
// the 83 MB Windows update under ~140 KB/s against 10 minutes).
package stall

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// ErrStalled is a download that got no byte for its Watch's while.
var ErrStalled = errors.New("no data came for a while")

// Watch is one download's idle timer.
type Watch struct {
	after  time.Duration
	t      *time.Timer
	ctx    context.Context
	cancel context.CancelCauseFunc
}

// Start returns a ctx under ctx that ends, its cause ErrStalled, once
// after goes by with no byte read through w.Reader. The timer runs from
// now: a server that never answers ends the same way. Stop it when the
// download is over.
func Start(ctx context.Context, after time.Duration) (context.Context, *Watch) {
	c, cancel := context.WithCancelCause(ctx)
	w := &Watch{after: after, ctx: c, cancel: cancel}
	w.t = time.AfterFunc(after, func() { cancel(ErrStalled) })
	return c, w
}

// Reader is r, each byte read from it putting the stall off again.
func (w *Watch) Reader(r io.Reader) io.Reader { return &reader{r: r, w: w} }

// Stop ends the watch and the ctx Start gave.
func (w *Watch) Stop() {
	w.t.Stop()
	w.cancel(nil)
}

// Err is err, said as a stall (wrapping ErrStalled) when the watch is
// what ended the download.
func (w *Watch) Err(err error) error {
	if err != nil && errors.Is(context.Cause(w.ctx), ErrStalled) {
		return fmt.Errorf("%w (nothing for %s)", ErrStalled, w.after)
	}
	return err
}

type reader struct {
	r io.Reader
	w *Watch
}

func (r *reader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.w.t.Reset(r.w.after)
	}
	return n, err
}
