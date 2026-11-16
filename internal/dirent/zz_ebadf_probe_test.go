//go:build darwin && cgo

package dirent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestEBADFProbe is an experiment for #6368, not a test to merge: it
// watches roots that are then removed, as internal/sessions' tests do,
// with file I/O beside it, and counts EBADF with and without WatchRoot.
func TestEBADFProbe(t *testing.T) {
	var out []string
	for _, on := range []bool{true, false, true, false} {
		setWatchRoot(on)
		n, ops, watches := stress(t, 45*time.Second)
		out = append(out, fmt.Sprintf("watchroot=%v: %d EBADF in %d ops, %d watches", on, n, ops, watches))
	}
	setWatchRoot(true)
	t.Errorf("probe result (fails on purpose so it prints):\n%s", fmt.Sprint(out))
}

func stress(t *testing.T, d time.Duration) (int64, int64, int64) {
	var bad, ops, watches atomic.Int64
	var firsts sync.Map
	stop := time.Now().Add(d)
	var wg sync.WaitGroup
	note := func(op string, err error) {
		if errors.Is(err, syscall.EBADF) {
			bad.Add(1)
			firsts.LoadOrStore(op, err.Error())
		}
	}
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; time.Now().Before(stop); i++ {
				dir, err := os.MkdirTemp("", "probe-io-")
				if err != nil {
					continue
				}
				p := filepath.Join(dir, fmt.Sprintf("f%d-%d", w, i))
				note("write", os.WriteFile(p, make([]byte, 4096), 0o600))
				_, err = os.ReadFile(p)
				note("read", err)
				f, err := os.Open(p)
				if err == nil {
					buf := make([]byte, 100)
					_, err = f.Read(buf)
					note("fread", err)
					note("close", f.Close())
				}
				note("removeall", os.RemoveAll(dir))
				ops.Add(1)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for time.Now().Before(stop) {
			root, err := os.MkdirTemp("", "probe-root-")
			if err != nil {
				continue
			}
			sub := filepath.Join(root, "sessions", "a")
			os.MkdirAll(sub, 0o700)
			os.WriteFile(filepath.Join(sub, "x.jsonl"), []byte("{}\n"), 0o600)
			Watch(root)
			if l, ok := Stats(sub); ok {
				l.Done(sub)
			}
			note("watch-removeall", os.RemoveAll(root))
			watches.Add(1)
		}
	}()
	wg.Wait()
	firsts.Range(func(k, v any) bool { t.Logf("first EBADF on %v: %v", k, v); return true })
	return bad.Load(), ops.Load(), watches.Load()
}
