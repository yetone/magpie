package gateway

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/filelock"
)

// The magpie serving the gateway does work no other magpie on its config
// folder may do at the same time: it renews the saved sign-ins as they
// come due, checks accounts in, moves retiring subscriptions, updates the
// plugins and the rest of what ListenAndServe starts after binding
// (servingWork, WhileServing). On one computer the gateway's port made
// that one magpie: a second couldn't bind it, and a handover (handover.go)
// ends the first one's work before the next serves. Two Kubernetes pods
// on one volume each bind a port of their own, so in a rolling update the
// new pod and the old one both served, and both did that work
// (@justforlxz on X asked for a RollingUpdate with no request dropped).
//
// So the work is held to whichever magpie holds servingLock, a lock the
// system keeps on a file in the config folder: a gateway that finds it
// held answers requests at once, as any magpie may (each refresh and each
// file change is locked across processes: filelock, host.js lockRenew),
// and starts the work once the other lets go, as its context ends (a
// SIGTERM's drain, a quit, a handover) or the process does. A file system
// that has no locks is read as no other magpie, as before there was one.

// servingLockFile is the lock's file in the config folder.
const servingLockFile = "gateway.serving.lock"

// servingRetry is how often a gateway waiting for the lock asks again; a
// var so tests can shorten it.
var servingRetry = time.Second

func servingLockPath() string { return filepath.Join(appdir.Config(), servingLockFile) }

// serving is a gateway's hold on the lock: release lets it go, once.
type serving struct {
	mu       sync.Mutex
	f        *os.File
	released bool
}

// release lets the lock go; a gateway still waiting for it stops.
func (h *serving) release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.released = true
	if h.f != nil {
		filelock.Unlock(h.f)
		h.f.Close()
		h.f = nil
	}
}

// take tries the lock once: held says this gateway may do the work now
// (it holds the lock, or the file system has none), done that it never
// will (released already, or no file to lock: a config folder that can't
// be made has nothing to do the work on either).
func (h *serving) take(path string) (held, done bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.released {
		return false, true
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if errors.Is(err, fs.ErrNotExist) {
		os.MkdirAll(filepath.Dir(path), 0o700)
		f, err = os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	}
	if err != nil {
		log.Printf("gateway: can't open %s (%v): doing the serving magpie's work without it", path, err)
		return true, false
	}
	got, err := filelock.TryLock(f)
	if err != nil {
		// a file system without locks (an NFS volume with no lockd): as
		// before there was a lock
		f.Close()
		log.Printf("gateway: %s can't be locked here (%v): doing the serving magpie's work without it; run one magpie on this folder at a time", path, err)
		return true, false
	}
	if !got {
		f.Close()
		return false, false
	}
	h.f = f
	return true, false
}

// holdServing runs work once this gateway holds the serving lock: at once
// when no other magpie holds it, else when that one lets go, never once
// ctx has ended. The lock is let go by the returned release, which the
// gateway calls as its context ends.
func holdServing(ctx context.Context, path string, work func()) (release func()) {
	h := &serving{}
	if held, done := h.take(path); held || done {
		if held {
			work()
		}
		return h.release
	}
	log.Printf("gateway: another magpie serves from %s (a rolling update's old pod, or another instance): this one answers requests now, and renews sign-ins, checks in and updates plugins once that one ends", filepath.Dir(path))
	go func() {
		t := time.NewTicker(servingRetry)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			held, done := h.take(path)
			if done {
				return
			}
			if held {
				if ctx.Err() != nil {
					h.release()
					return
				}
				log.Printf("gateway: the other magpie on %s let go: this one does its work now", filepath.Dir(path))
				work()
				return
			}
		}
	}()
	return h.release
}
