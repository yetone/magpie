package pii

import (
	"fmt"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/redact"
)

// helperRunner runs one plugin model's detector (external.go), a program
// that speaks pf-serve's protocol: started on the first request, ended
// after idleAfter with none, and held back for retryAfter once it failed,
// as srv does for Privacy Filter, whose GPU fallback it has no need of.
// turn is the backend's, held by the one request on the pipe. mu guards
// the fields below it and is never held while the detector loads or
// answers; startMu lets one start run at a time.
type helperRunner struct {
	turn chan struct{}

	startMu sync.Mutex
	mu      sync.Mutex
	h       *helper
	// run is closed by stop: a load under way gives up, and a request
	// that took it before fails rather than start the detector again
	run     chan struct{}
	idle    *time.Timer
	idleSeq uint64
	loading bool
	// failed is why the last request got no answer, until one does;
	// lastErr is that error, fails how many in a row got none, retryAt
	// when the detector is tried again
	failed  string
	lastErr error
	fails   int
	retryAt time.Time
}

// currentRun is the run a request starts on, made the first time.
func (r *helperRunner) currentRun() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.run == nil {
		r.run = make(chan struct{})
	}
	return r.run
}

// load is the detector of run, started with start if none runs.
func (r *helperRunner) load(run <-chan struct{}, start func(<-chan struct{}) (*helper, error)) (*helper, error) {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	r.mu.Lock()
	switch {
	case run != r.run:
		r.mu.Unlock()
		return nil, errStopped
	case r.h != nil:
		h := r.h
		r.mu.Unlock()
		return h, nil
	}
	if err := r.heldBackLocked(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	r.loading = true
	r.mu.Unlock()

	h, err := start(run)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.run != run {
		if h != nil {
			h.kill()
		}
		return nil, errStopped
	}
	r.loading = false
	if err != nil {
		r.failLocked(err)
		return nil, err
	}
	r.h = h
	return h, nil
}

// complete records how h answered a request of run: one that answered
// ends after idleAfter with no other, one that failed is ended at once.
func (r *helperRunner) complete(run <-chan struct{}, h *helper, err error) error {
	r.mu.Lock()
	if run != r.run {
		r.mu.Unlock()
		return errStopped
	}
	var gone *helper
	if r.h == h {
		if err == nil {
			r.failed, r.lastErr, r.fails, r.retryAt = "", nil, 0, time.Time{}
			r.resetIdleLocked(h)
		} else {
			gone = r.detachLocked()
			r.failLocked(err)
		}
	}
	r.mu.Unlock()
	if gone != nil {
		gone.kill()
	}
	return err
}

// stop ends the detector and a load under way at once, and a request on
// it fails; turned on again, it is tried at once.
func (r *helperRunner) stop() {
	r.mu.Lock()
	h := r.detachLocked()
	if r.run != nil {
		close(r.run)
		r.run = nil
	}
	r.loading = false
	r.failed, r.lastErr, r.fails, r.retryAt = "", nil, 0, time.Time{}
	r.mu.Unlock()
	if h != nil {
		h.kill()
	}
}

func (r *helperRunner) detachLocked() *helper {
	if r.idle != nil {
		r.idle.Stop()
		r.idle = nil
	}
	h := r.h
	r.h = nil
	return h
}

// resetIdleLocked ends h after idleAfter with no request. The timer takes
// the turn only when it is free: a request on it sets the timer again
// when it is answered.
func (r *helperRunner) resetIdleLocked(h *helper) {
	if r.idle != nil {
		r.idle.Stop()
	}
	r.idleSeq++
	seq := r.idleSeq
	r.idle = time.AfterFunc(idleAfter, func() {
		select {
		case r.turn <- struct{}{}:
		default:
			return
		}
		defer func() { <-r.turn }()
		r.mu.Lock()
		var gone *helper
		if r.h == h && r.idleSeq == seq {
			gone = r.detachLocked()
		}
		r.mu.Unlock()
		end(gone)
	})
}

// warm starts the detector in the background, so the first request
// doesn't wait for it to load.
func (r *helperRunner) warm(run <-chan struct{}, classify func([]string) ([][]redact.Found, error)) {
	go func() {
		select {
		case <-run:
			return
		case r.turn <- struct{}{}:
		}
		defer func() { <-r.turn }()
		classifySafely(classify, []string{"warm"})
	}()
}

func (r *helperRunner) fillStatusLocked(s *Status) {
	if h := r.h; h != nil {
		select {
		case <-h.exited:
		default:
			s.Running, s.Device, s.GPU, s.LoadMS, s.GPUError = true, h.hello.Device, h.hello.GPU, h.hello.MS, h.hello.GPUError
		}
	}
	s.Failed, s.Loading = r.failed, r.loading
}

func (r *helperRunner) failLocked(err error) {
	r.failed, r.lastErr = err.Error(), err
	r.fails++
	r.retryAt = time.Now().Add(retryAfter(r.fails))
}

// heldBack is the error of a request that needs the detector while it
// isn't tried again yet, after it failed; nil when it may be tried.
func (r *helperRunner) heldBack() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.heldBackLocked()
}

func (r *helperRunner) heldBackLocked() error {
	if r.lastErr == nil || !time.Now().Before(r.retryAt) {
		return nil
	}
	return fmt.Errorf("%w; it is tried again in %s, and until then a request it hasn't read is held back at once. To send requests masked by the rules alone, turn the model off in Settings → Privacy",
		r.lastErr, time.Until(r.retryAt).Round(time.Second))
}
